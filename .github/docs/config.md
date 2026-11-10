# Quick start

```bash
docker run --rm -p 3000:3000 9109679196/piper-tts-rest-api:latest
```

Or you can look at our [compose.yml file](../../compose.yml) to know what other services you would need to start this app.

# Configuration

You can configure and setup this image using env variables.

## General

<table>
  <thead>
    <tr>
      <th>Variable</th>
      <th>Default</th>
      <th>Description</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td>PORT</td>
      <td>3000</td>
      <td>HTTP port.</td>
    </tr>
    <tr>
      <td>DOCS_ENABLED</td>
      <td>false</td>
      <td>Serves Swagger UI at <code>/docs</code> and the OpenAPI document at <code>/openapi.json</code>. Keep it off in production.</td>
    </tr>
    <tr>
      <td>APP_ENV</td>
      <td>production</td>
      <td><code>development</code> / <code>test</code> relaxes HTTPS requirement.</td>
    </tr>
    <tr>
      <td>SERVICE_NAME</td>
      <td>piper-tts-rest-api</td>
      <td>Used in logs and OpenTelemetry <code>service.name</code>.</td>
    </tr>
    <tr>
      <td>SERVICE_VERSION</td>
      <td>unknown</td>
      <td>Release version, OpenTelemetry <code>service.version</code>. The image sets it at build time.</td>
    </tr>
    <tr>
      <td>LOGGING_MODE</td>
      <td>PLAIN_TEXT</td>
      <td>PLAIN_TEXT or JSON.</td>
    </tr>
    <tr>
      <td>LOGGING_LEVEL</td>
      <td>info</td>
      <td>error, warn, info, debug, verbose.</td>
    </tr>
    <tr>
      <td>PIPER_MODEL</td>
      <td>/app/models/en_US-lessac-medium.onnx</td>
      <td>Path to the Piper voice model. The matching <code>&lt;model&gt;.onnx.json</code> sidecar must exist next to it (the server reads <code>audio.sample_rate</code> from it at startup).</td>
    </tr>
    <tr>
      <td>MAX_CONCURRENCY</td>
      <td>10</td>
      <td>Maximum number of simultaneous <code>/speak</code> will handle simultaneously.</td>
    </tr>
  </tbody>
</table>

### Bundled English voice models

The Docker image ships these Piper voices under `/app/models/`:

- `en_US-lessac-medium.onnx` (default).
- `en_US-ryan-medium.onnx`.
- `en_US-libritts_r-medium.onnx`.
- `en_US-ljspeech-medium.onnx`.
- `en_US-amy-medium.onnx`.
- `en_US-joe-medium.onnx`.
- `en_US-john-medium.onnx`.
- `en_US-kristin-medium.onnx`.

Switch voices via `PIPER_MODEL=/app/models/<voice>.onnx`.

## OpenTelemetry

<table>
  <thead>
    <tr>
      <th>Variable</th>
      <th>Default</th>
      <th>Description</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td>OTEL_ENABLED</td>
      <td>false</td>
      <td>Enable OTel traces and logs.</td>
    </tr>
    <tr>
      <td>OTEL_EXPORTER_OTLP_ENDPOINT</td>
      <td>(none)</td>
      <td>OTLP HTTP base URL (e.g. <code>http://collector:4318</code>). The SDK appends <code>/v1/traces</code> and <code>/v1/logs</code>.</td>
    </tr>
    <tr>
      <td>OTEL_LOG_SHIPPING</td>
      <td>otlp</td>
      <td><code>otlp</code> sends every log record to the collector through OTLP and preserves native trace-to-log links. <code>stdout</code> keeps logs on stdout only; use it when infrastructure collects stdout/stderr into the log backend and preserves the <code>trace_id</code> field. Ignored when <code>OTEL_ENABLED</code> is not <code>true</code>.</td>
    </tr>
    <tr>
      <td>OTEL_TRACES_SAMPLER</td>
      <td>parentbased_traceidratio</td>
      <td>Standard OTel sampler.</td>
    </tr>
    <tr>
      <td>OTEL_TRACES_SAMPLER_ARG</td>
      <td>1.0</td>
      <td>Ratio (0‑1) for ratio samplers.</td>
    </tr>
    <tr>
      <td>OTEL_BATCH_MAX_PENDING_SPANS</td>
      <td>2048</td>
      <td>Max queued spans before dropping.</td>
    </tr>
    <tr>
      <td>OTEL_BATCH_SPANS_PER_EXPORT</td>
      <td>512</td>
      <td>Batch size per export.</td>
    </tr>
    <tr>
      <td>OTEL_BATCH_FLUSH_INTERVAL_MS</td>
      <td>5000</td>
      <td>Flush interval.</td>
    </tr>
    <tr>
      <td>OTEL_BATCH_EXPORT_TIMEOUT_MS</td>
      <td>30000</td>
      <td>Export deadline.</td>
    </tr>
    <tr>
      <td>OTEL_LOGS_BATCH_MAX_PENDING</td>
      <td>2048</td>
      <td>Max queued log records before dropping.</td>
    </tr>
    <tr>
      <td>OTEL_LOGS_BATCH_PER_EXPORT</td>
      <td>512</td>
      <td>Log records per export.</td>
    </tr>
    <tr>
      <td>OTEL_LOGS_BATCH_FLUSH_INTERVAL_MS</td>
      <td>5000</td>
      <td>Log flush interval.</td>
    </tr>
    <tr>
      <td>OTEL_LOGS_BATCH_EXPORT_TIMEOUT_MS</td>
      <td>30000</td>
      <td>Log export deadline.</td>
    </tr>
  </tbody>
</table>

In production point `OTEL_EXPORTER_OTLP_ENDPOINT` to your own collector and set `OTEL_ENABLED=true`.

The local Compose stack sends traces to Tempo and logs to Loki. Open Grafana at [http://localhost:3001](http://localhost:3001) and sign in with `GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD` (both default to `admin`). The provisioned **Observability / Piper TTS Observability** dashboard shows recent traces and logs. Opening a trace in Tempo provides a trace-to-logs link that queries Loki by trace ID. Local Grafana, Tempo, and Loki data is transient.

Keep `OTEL_LOG_SHIPPING=otlp` unless a platform-level log collector sends the application's stdout/stderr to Loki. With the default, logs travel directly from the service through OTLP to Loki and retain native trace context. With `stdout`, the service still writes `trace_id`, `span_id` and `trace_flags` to stdout, but the infrastructure must parse and preserve `trace_id` when it sends those logs to Loki; otherwise Grafana cannot find logs for a trace.

### Logging

Every request and response is logged. For end-to-end tracing across services, enable OpenTelemetry.

<details><summary><code>LOGGING_MODE=PLAIN_TEXT</code> (default)</summary>

```
[2026-03-08T00:17:21.123Z]     HttpMiddleware       [piper-tts-rest-api] Incoming POST /v1/speak | {"method":"POST","url":"/v1/speak","headers":{...}}
```

</details>

<details><summary><code>LOGGING_MODE=JSON</code></summary>

```json
{
  "timestamp": "2026-03-08T00:17:21.123Z",
  "level": "info",
  "service": "piper-tts-rest-api",
  "context": "HttpMiddleware",
  "message": "Incoming POST /v1/speak",
  "method": "POST",
  "url": "/v1/speak",
  "headers": {}
}
```

Use this for log aggregators (e.g. Loki, Elasticsearch, Datadog).

</details>

#### Track These Dimensions

`jobId`, attempt, model, instance, state/stage, error code, and trace ID.

- Queue depth, oldest-message age, unacknowledged messages, and DLQ depth.
- Retry rate, terminal-failure rate by stage/error code, and actual retry delay.
- Queue wait, generation, upload, and total-duration percentiles.
- File-size and text-character distributions by model and instance.

Alert on sustained queue age/depth, any unexpected DLQ growth, elevated terminal failures, and upload/generation latency regression. Use the trace path from HTTP enqueue through AMQP consumption, generation, upload-URL callback, upload, retry publish, and terminal failure to investigate individual jobs.

## Asynchronous Synthesis

<table>
  <thead>
    <tr>
      <th>Variable</th>
      <th>Default</th>
      <th>Description</th>
    </tr>
  </thead>
  <tbody>
    <tr>
      <td>SYNTHESIZE_MAX_BODY_SIZE_BYTES</td>
      <td>2000000 (2 MB)</td>
      <td>Max <code>/synthesize</code> payload.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_MAX_ATTEMPTS</td>
      <td>3</td>
      <td>Total processing attempts (incl. first).</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_RETRY_DELAY_MS</td>
      <td>30000</td>
      <td>Delay after the first failed attempt; the nth retry waits n times this. A 429 with <code>Retry-After</code> overrides it.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS</td>
      <td>10000</td>
      <td>Per status‑callback call timeout.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_GEN_UPLOAD_URL_TIMEOUT_MS</td>
      <td>10000</td>
      <td>Upload‑URL generation callback call timeout.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS</td>
      <td>10000</td>
      <td>Max idle time during upload.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_UPLOAD_TIMEOUT_MS</td>
      <td>600000 (10 min)</td>
      <td>Absolute upload deadline.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_WORDS_PER_MINUTE</td>
      <td>150</td>
      <td>
        Tune <code>SYNTHESIZE_WORDS_PER_MINUTE</code> from observed generation duration and text length for each Piper voice. The default value is an estimate, not a throughput guarantee.
      </td>
    </tr>
    <tr>
      <td>SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS</td>
      <td>true (except dev/test)</td>
      <td>Enforce HTTPS for callback URLs. Set to <code>false</code> to call plain-HTTP endpoints, for example internal services.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_CALLBACK_ALLOWED_HOSTS</td>
      <td>(empty)</td>
      <td>Comma‑separated hostnames allowed for callbacks; empty means any host. The worker refuses to connect to non-public addresses (private, loopback, link-local) unless the host is listed here, so internal service names such as <code>garage</code> or <code>wiremock</code> must be listed.</td>
    </tr>
    <tr>
      <td>RABBITMQ_URL</td>
      <td>amqp://localhost:5672</td>
      <td>AMQP/AMQPS connection URL.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_EXCHANGE</td>
      <td>synthesize</td>
      <td>Direct exchange used for synthesizing.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_WORK_QUEUE</td>
      <td>synthesize.work</td>
      <td>Worker queue name.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_DEAD_LETTER_EXCHANGE</td>
      <td>synthesize.dlx</td>
      <td>Dead‑letter exchange for dead messages whom failed to be processed.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_DEAD_LETTER_QUEUE</td>
      <td>synthesize.dlq</td>
      <td>Dead‑letter queue name for failed synthesize jobs.</td>
    </tr>
  </tbody>
</table>

> [!IMPORTANT]
>
> All positive-integer values fail fast if invalid.
