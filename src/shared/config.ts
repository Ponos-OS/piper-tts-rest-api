import type { LogLevel, LogMode } from "./utils/index.js";

/**
 * Application configuration values sourced from environment variables.
 */
export const config = {
  /** Service configuration. */
  service: {
    /** Port the HTTP server listens on. */
    port: Number.parseInt(process.env.PORT ?? "3000", 10),

    /**
     * Service name for logging.
     * @default 'piper-tts-rest-api'
     */
    name: process.env.SERVICE_NAME ?? "piper-tts-rest-api",

    /**
     * @type {'production' | 'development' | 'test'}
     * @description Environment the service is running in.
     * @default 'production'
     */
    env: process.env.NODE_ENV ?? "production",
  },

  /**
   * Maximum number of concurrent text-to-speech requests.
   * @default 10
   */
  maxConcurrency: Number.parseInt(process.env.MAX_CONCURRENCY ?? "10", 10),

  /** Path to the Piper model file. */
  piperModelPath:
    process.env.PIPER_MODEL ?? "/app/models/en_US-lessac-medium.onnx",

  /**
   * Maximum allowed request body size is 0.5MB, which is sufficient for at least ~75000 words — more than enough for typical use cases.
   * @default 500_000
   */
  maxBodySizeBytes: Number(process.env.MAX_BODY_SIZE_BYTES ?? 500_000),

  /** Logging configuration. */
  log: {
    /**
     * Logging mode: PLAIN_TEXT for human-readable logs, JSON for structured logs.
     * @default 'PLAIN_TEXT'
     */
    mode: ((process.env.LOGGING_MODE as LogMode | undefined) ??
      "PLAIN_TEXT") as LogMode,

    /**
     * Logging level: error, warn, info, debug, verbose.
     * @default 'info'
     */
    level: ((process.env.LOGGING_LEVEL as LogLevel | undefined) ??
      "info") as LogLevel,
  },

  otel: {
    enabled: process.env.OTEL_ENABLED === "true",
    /** @type {string | undefined} */
    exporterEndpoint: process.env.OTEL_EXPORTER_OTLP_ENDPOINT,
    batchConfig: {
      /**
       * @description Max spans held in the in-memory queue waiting to be exported. Spans beyond this cap are dropped (with a diag warning) — acts as a back-pressure safety valve if the collector is slow/unreachable.
       */
      maxPendingSpans: Number.parseInt(
        process.env.OTEL_BATCH_MAX_PENDING_SPANS ?? "2048",
        10,
      ),
      /**
       * @description Max spans sent in a single export request. The processor flushes immediately once the queue reaches this size, regardless of the scheduled delay. Smaller = more frequent, smaller payloads.
       */
      spansPerExport: Number.parseInt(
        process.env.OTEL_BATCH_SPANS_PER_EXPORT ?? "512",
        10,
      ),
      /**
       * @description Interval at which the processor wakes up and flushes whatever is currently queued, even if `maxExportBatchSize` hasn't been reached. Trade-off: lower = fresher data in the backend, higher = fewer HTTP round-trips.
       */
      flushIntervalMs: Number.parseInt(
        process.env.OTEL_BATCH_FLUSH_INTERVAL_MS ?? "5000",
        10,
      ),
      /**
       * @description Hard deadline for a single export call to the OTLP endpoint. If the collector doesn't respond within this window the export is aborted and the spans in that batch are dropped (not retried).
       */
      exportTimeoutMs: Number.parseInt(
        process.env.OTEL_BATCH_EXPORT_TIMEOUT_MS ?? "30000",
        10,
      ),
    },
    /**
     * @description Same trade-offs as `batchConfig` but for the log record pipeline driven by `BatchLogRecordProcessor`. Logs are typically chattier than spans so it's worth tuning these independently.
     */
    logsBatchConfig: {
      /** Max log records held in memory before back-pressure drops the oldest. */
      maxPendingLogs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_MAX_PENDING ?? "2048",
        10,
      ),
      /** Max log records flushed in a single export request. */
      logsPerExport: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_PER_EXPORT ?? "512",
        10,
      ),
      /** How often the processor flushes whatever is queued. */
      flushIntervalMs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS ?? "5000",
        10,
      ),
      /** Per-export hard deadline before the batch is aborted. */
      exportTimeoutMs: Number.parseInt(
        process.env.OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS ?? "30000",
        10,
      ),
    },
  },
} as const;
