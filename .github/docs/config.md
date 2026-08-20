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
      <td>NODE_ENV</td>
      <td>production</td>
      <td><code>development</code> / <code>test</code> relaxes HTTPS requirement.</td>
    </tr>
    <tr>
      <td>SERVICE_NAME</td>
      <td>piper-tts-rest-api</td>
      <td>Used in logs and OpenTelemetry <code>service.name</code>.</td>
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
      <td>OTLP HTTP base URL (e.g. <code>http://collector:4318</code>). <strong>The SDK appends <code>/v1/traces</code></strong>.</td>
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
  </tbody>
</table>

In production point `OTEL_EXPORTER_OTLP_ENDPOINT` to your own collector and set `OTEL_ENABLED=true`.

### Logging

Every request and response is logged. For end-to-end tracing across services, enable OpenTelemetry.

<details><summary><code>LOGGING_MODE=PLAIN_TEXT</code> (default)</summary>

```
[2026-03-08T00:17:21.123Z]     HttpMiddleware       [piper-tts-rest-api] Incoming POST /speak | {"method":"POST","url":"/speak","headers":{...}}
```

</details>

<details><summary><code>LOGGING_MODE=JSON</code></summary>

```json
{
  "timestamp": "2026-03-08T00:17:21.123Z",
  "level": "info",
  "service": "piper-tts-rest-api",
  "context": "HttpMiddleware",
  "message": "Incoming POST /speak",
  "method": "POST",
  "url": "/speak",
  "headers": {}
}
```

Use this for log aggregators (e.g. Loki, Elasticsearch, Datadog).

</details>

#### Track These Dimensions

`jobId`, attempt, model, instance, state/stage, error code, and trace ID.

- Queue depth, oldest-message age, unacknowledged messages, retry-queue depth, and DLQ depth.
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
      <td>Delay between retries.</td>
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
      <td>Enforce HTTPS for callback URLs.</td>
    </tr>
    <tr>
      <td>SYNTHESIZE_CALLBACK_ALLOWED_HOSTS</td>
      <td>(empty)</td>
      <td>Comma‑separated hostnames allowed for callbacks, empty means any valid HTTP(S) host.</td>
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
      <td>SYNTHESIZE_RETRY_QUEUE</td>
      <td>synthesize.retry</td>
      <td>Retry queue name.</td>
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
