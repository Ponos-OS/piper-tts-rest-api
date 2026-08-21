/* eslint-disable no-console */
/**
 * @fileoverview OpenTelemetry bootstrap for piper-tts-rest-api.
 *
 * This module MUST be loaded as a Node preload via `node --import` so the SDK starts before any user module — and crucially before `node:http` — is resolved. That is the only way `@opentelemetry/instrumentation-http` can monkey-patch `http.createServer` before our route layer captures a reference to the un-patched function.
 *
 * Wired up in `package.json`'s "start" script. A plain `import "./instrumentation.js"` in the `server.ts` is NOT sufficient under ESM: all `import` statements in a module are evaluated before its body runs, so `node:http` would be captured before the SDK started.
 */

/**
 * @note
 * Register `import-in-the-middle` as a Node module-customization hook BEFORE we import anything else. Under ESM, `--import` alone is not enough to make `@opentelemetry/instrumentation-http` patch `node:http`: the instrumentation's monkey-patching mechanism relies on intercepting the import resolution of the target module, which under ESM only happens if a loader hook is registered. `@opentelemetry/instrumentation/hook.mjs` ships a thin wrapper around `import-in-the-middle` that exposes exactly such a hook. We register it via `node:module#register` (stable on Node ≥ 20.6) and *only* on the current module's parent URL so the hook applies to the whole runtime.
 *
 * Without this block, you will see:
 *
 * - No `POST /...` server span from `@opentelemetry/instrumentation-http`.
 * - Incoming `traceparent` headers silently dropped (no context extraction).
 * - Every manual span becoming a root span.
 */
import { register } from "node:module";

register("@opentelemetry/instrumentation/hook.mjs", import.meta.url);

import { diag, DiagConsoleLogger, DiagLogLevel } from "@opentelemetry/api";
import { logs } from "@opentelemetry/api-logs";
import {
  CompositePropagator,
  W3CBaggagePropagator,
  W3CTraceContextPropagator,
} from "@opentelemetry/core";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-http";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { AmqplibInstrumentation } from "@opentelemetry/instrumentation-amqplib";
import { HttpInstrumentation } from "@opentelemetry/instrumentation-http";
import { WinstonInstrumentation } from "@opentelemetry/instrumentation-winston";
import { resourceFromAttributes } from "@opentelemetry/resources";
import {
  BatchLogRecordProcessor,
  LoggerProvider,
} from "@opentelemetry/sdk-logs";
import { NodeSDK } from "@opentelemetry/sdk-node";
import { BatchSpanProcessor } from "@opentelemetry/sdk-trace-base";
import {
  ATTR_SERVICE_NAME,
  ATTR_SERVICE_VERSION,
} from "@opentelemetry/semantic-conventions";

import { config } from "./shared/config.js";

let otelSDK: NodeSDK | null = null;

if (!config.otel.enabled) {
  console.info(
    `${config.service.name} OTEL_ENABLED!=true — OpenTelemetry SDK is disabled.`,
  );
} else {
  // ⚠️ `@opentelemetry/sdk-node` auto-enables a `PeriodicExportingMetricReader` whenever it detects `OTEL_EXPORTER_OTLP_ENDPOINT` in the env, pointing the OTLP metric exporter at `${endpoint}/v1/metrics`. We don't expose a metrics route on our collector, so that pipeline spams the diag channel with `OTLPExporterError: Not Found` errors every export interval. We're not emitting any metrics today, so opt out by default — but allow operators to override via env if they wire metrics up later.
  process.env.OTEL_METRICS_EXPORTER ??= "none";

  diag.setLogger(
    new DiagConsoleLogger(),
    process.env.NODE_ENV === "development"
      ? DiagLogLevel.DEBUG
      : DiagLogLevel.WARN,
  );

  const otelUrl = config.otel.exporterEndpoint?.replace(/\/$/, "");
  const exporter = new OTLPTraceExporter({
    url: otelUrl ? `${otelUrl}/v1/traces` : undefined,
  });
  const resource = resourceFromAttributes({
    [ATTR_SERVICE_NAME]: config.service.name,
    [ATTR_SERVICE_VERSION]: process.env.npm_package_version ?? "unknown",
    "deployment.environment": config.service.env,
  });
  /**
   * @description Plain HTTP instrumentation. Skipping the health check endpoint so we don't drown the collector in noise.
   */
  const httpInstrumentation = new HttpInstrumentation({
    ignoreIncomingRequestHook: (req) => {
      const url = req.url ?? "";
      return url === "/health";
    },
  });
  const amqplibInstrumentation = new AmqplibInstrumentation();
  /**
   * @description auto-instruments the `winston` logger so every log record emitted through it is forwarded to the global `LoggerProvider` (registered below) AND gets `trace_id` / `span_id` / `trace_flags` injected into the record fields based on the currently active OTel context. This is what makes log-trace linking work end-to-end without any manual plumbing inside our `Logger` class.
   */
  const winstonInstrumentation = new WinstonInstrumentation({
    /**
     * Without this, the bridge only injects trace context fields when there is already an `info`-level record being written by a configured transport. With it on, every log goes through the OTel logs pipeline regardless of winston's own transport configuration.
     */
    disableLogSending: config.otel.winston.disableLogSending,
    /**
     * Inject `trace_id` / `span_id` / `trace_flags` into the JSON log lines winston writes to stdout too — useful when running without an OpenTelemetry Collector (local dev, debugging) so you can still grep traces in the terminal.
     */
    logHook: (_span, record) => {
      record["service.name"] = config.service.name;
    },
  });
  /**
   * @description Batch spans in memory and flush them to the exporter periodically (or when the batch fills up) instead of exporting one-by-one. This amortizes network cost and keeps the hot path cheap.
   */
  const batchSpanProcessor = new BatchSpanProcessor(exporter, {
    maxQueueSize: config.otel.batchConfig.maxPendingSpans,
    maxExportBatchSize: config.otel.batchConfig.spansPerExport,
    scheduledDelayMillis: config.otel.batchConfig.flushIntervalMs,
    exportTimeoutMillis: config.otel.batchConfig.exportTimeoutMs,
  });
  /**
   * @description The logs pipeline mirrors the trace pipeline: an OTLP/HTTP exporter pushes log records to the collector, wrapped in a batch processor for the same back-pressure / cost reasons documented on `batchSpanProcessor`.
   *
   * We register the provider as the global one (`logs.setGlobalLoggerProvider`) so `@opentelemetry/instrumentation-winston` — and any future log-emitting instrumentation — can pick it up via `logs.getLogger(...)` without us threading it through manually.
   */
  const logExporter = new OTLPLogExporter({
    url: otelUrl ? `${otelUrl}/v1/logs` : undefined,
  });
  const logRecordProcessor = new BatchLogRecordProcessor(logExporter, {
    maxQueueSize: config.otel.logsBatchConfig.maxPendingLogs,
    maxExportBatchSize: config.otel.logsBatchConfig.logsPerExport,
    scheduledDelayMillis: config.otel.logsBatchConfig.flushIntervalMs,
    exportTimeoutMillis: config.otel.logsBatchConfig.exportTimeoutMs,
  });
  const loggerProvider = new LoggerProvider({
    resource,
    processors: [logRecordProcessor],
  });

  logs.setGlobalLoggerProvider(loggerProvider);

  otelSDK = new NodeSDK({
    resource,
    spanProcessors: [batchSpanProcessor],
    textMapPropagator: new CompositePropagator({
      propagators: [
        new W3CTraceContextPropagator(),
        new W3CBaggagePropagator(),
      ],
    }),
    instrumentations: [
      httpInstrumentation,
      winstonInstrumentation,
      amqplibInstrumentation,
    ],
  });

  otelSDK.start();

  console.info(
    `OpenTelemetry SDK started for service "${config.service.name}".`,
  );

  const shutdown = (): void => {
    Promise.allSettled([
      otelSDK?.shutdown() ?? Promise.resolve(),
      loggerProvider.shutdown(),
    ])
      .then((results) => {
        for (const result of results) {
          if (result.status === "rejected") {
            console.error(
              `OTel shutdown error: ${result.reason instanceof Error ? result.reason.message : String(result.reason)}`,
            );
          }
        }
        console.info(
          `OTel pipelines shut down cleanly for service "${config.service.name}".`,
        );
      })
      .finally(() => process.exit(0));
  };

  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);
}

export { otelSDK };
