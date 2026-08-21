# API Endpoints

How the audio pipeline works?

1. `piper --output-raw` writes signed 16-bit little-endian mono PCM samples to its stdout.
2. ffmpeg reads that raw PCM (`-f s16le -ar <rate> -ac 1 -i pipe:0`) and encodes MP3 on the fly.
3. Here we have two branches:
   - If client calls `/speak` API: the API streams ffmpeg's MP3 output to the HTTP response with `Transfer-Encoding: chunked`.
   - If client calls `/synthesize` API: we upload it directly to the object storage.

The PCM sample rate is read once at process startup from the model's `.onnx.json` sidecar (`audio.sample_rate`) and passed to ffmpeg.

## `GET /health`

- Synchronous health check.
- Response `200 OK`, `text/plain` body.

## `POST /speak`

- Synchronous text-to-speech.
- Generates and streams an MP3 file directly in the HTTP response.
- Request (default max body size: 0.5 MB (~75,000 words)):
  - `Content-Type: application/json` with `{ "text": "..." }`.
  - `Content-Type: text/plain` with raw text body.
- Success response (response is streamed MP3, in other words no `Content-Length`):
  - `200 OK`.
  - `Content-Type: audio/mpeg`.
  - `Content-Disposition: inline; filename="<generated>.mp3"`.
  - `Cache-Control: no-store`.
  - `Transfer-Encoding: chunked`.
  - `Body`: MP3 audio stream, read until the stream ends.
- Errors:
  - `400`: invalid JSON or empty/missing text.
  - `413`: payload too large.
  - `500`: synthesis or transcoding failure.
- Example:

  ```shell
  # JSON
  curl -X POST http://localhost:3000/speak \
       -H 'content-type: application/json' \
       -d '{"text":"Hello from Piper API"}' \
       -o output.mp3

  # Plain text
  curl -X POST http://localhost:3000/speak \
       -H 'content-type: text/plain' \
       --data 'Hello from plain text' \
       -o output.mp3
  ```

<details>
<summary>Learn more about concurrency & cancellation for this endpoint</summary>

`POST /speak` is gated by an in-memory semaphore sized by `MAX_CONCURRENCY` (default `10`). The slot is held **for the entire lifetime of the work** — body parsing, piper synthesis, ffmpeg encoding, and streaming to the client — not just until child processes are spawned. So `MAX_CONCURRENCY` is an honest end-to-end concurrency cap.

When a client closes the connection before the response is fully sent (e.g. the user navigates away, the upstream service aborts via `AbortController`, or curl is `Ctrl-C`'d), the server kills the in-flight `piper` and `ffmpeg` child processes via `SIGKILL` and waits for them to exit before releasing the semaphore slot.

The net effect: **aborting an in-flight `/speak` request immediately frees a concurrency slot**, so a queued or subsequent request can start synthesis without waiting for the cancelled one to run to completion. This is useful for callers that implement supersede-with-retry patterns — a force-regenerate, for example, can cancel an in-flight TTS HTTP call to free CPU on the TTS host before kicking off a new one.

> [!NOTE]
>
> Cancellation is per-process and per-replica. If you run multiple replicas behind a load balancer, aborting a request only frees the slot on the replica that was handling it. There is no cross-replica cancellation.

> [!TIP]
>
> Pick a client-side timeout that allows for synthesis: piper takes ~2 s before producing the first audio byte for short inputs, and proportionally longer for longer text. Aggressive timeouts will cause spurious cancellations.

</details>

<details>
<summary>Learn more about performance characteristics of this endpoint</summary>

Each `/speak` request spawns a fresh `piper` child process which loads the ONNX voice model (~61 MB for the bundled medium-quality voices) and initialises the ONNX runtime before it can begin synthesis. This adds a fixed **~1.7–2 s of cold-start cost per request**, on top of the actual synthesis time.

This service is deliberately **not** optimised for lowest-latency-per-request or highest-throughput workloads. The CLI we drive (`piper --output-raw`) has no daemon mode, so a long-lived in-memory model is not on the table without rewriting against piper's Python library. If your use case is latency-sensitive (e.g. interactive UI feedback), drive piper from Python with a single long-lived model handle instead of going through this REST API.

For batch / offline-narration workloads — which is what this service is designed for — the 2 s cold start is negligible compared to the actual synthesis time of multi-paragraph inputs, and the per-request process boundary makes cancellation and resource cleanup trivial (see [Concurrency and cancellation](#concurrency-and-cancellation) above).

</details>

## `POST /synthesize`

- Asynchronous TTS.
- Creates a long-running job, generates the MP3, and uploads it to a client-managed object store.
- The job runs in the background via RabbitMQ and reports progress via callbacks.
- Request body:
  - `text`: the text to synthesize (required).
  - `genUploadUrl`: endpoint the service calls to obtain a fresh presigned PUT URL for uploading the MP3. The service will call this callback **once** per processing attempt with:
    ```json
    {
      "jobId": "e52cbe80-17de-4204-a983-bb1c10c36341",
      "attempt": 1,
      "contentType": "audio/mpeg"
    }
    ```
    Your endpoint **must** reply with 200 and a JSON body:
    ```json
    {
      "uploadUrl": "https://storage.example/bucket/file.mp3?...&Content-Type=audio%2Fmpeg"
    }
    ```
    This URL is going to be a short living one which remains valid only for the time we most likely need to generate and then upload the audio file. You must configure this on the client side to generate upload URLs with appropriate TTL.
  - `statusCallbackUrl`: endpoint where progress and statuses are sent. Each request includes an `Idempotency-Key: <jobId>:<attempt>` header.
    | Status | Payload additions | Meaning |
    | ---------- | ---------------------------- | ----------------------------------- |
    | queued | - | Worker received the message. |
    | generating | percent (0–99) | Producing the MP3. |
    | uploading | percent (0–99) | Uploading to object storage. |
    | completed | fileSizeBytes | Upload succeeded – job done. |
    | failed | failedAt, error | Terminal failure – no more retries. |
    Percentages are stage‑local, approximate, and monotonic. Status delivery is best‑effort, timeouts or non‑2xx responses do not stop synthesis. Clients must tolerate duplicates, omissions, and out‑of‑order updates.
- Success Response (202 Accepted):
  ```json
  { "jobId": "e52cbe80-17de-4204-a983-bb1c10c36341" }
  ```
  **Note**: A 202 does **NOT** mean the MP3 is ready, the job is queued.
- Errors:
  - `400`: malformed or invalid request.
  - `413`: payload too large (configured by `SYNTHESIZE_MAX_BODY_SIZE_BYTES`).
  - `503`: RabbitMQ publish failed.
- Security:
  - Use HTTPS endpoints in production: the service enforces this by default (see configuration below).
  - Presigned URLs are redacted in logs; only the deliberate redaction‑failure path writes full URLs, so keep log retention short and restrict access.
- Example:
  ```shell
  curl -X POST http://localhost:3000/synthesize \
       -H 'Content-Type: application/json' \
       -H 'Authorization: Bearer or-basic-piper-tts-rest-api-does-not-care' \
       -d '{
         "text": "Hello world, this is a test of the asynchronous synthesis API.",
         "genUploadUrl": "https://your-server.example/generate-upload-url",
         "statusCallbackUrl": "https://your-server.example/synthesis-status"
       }'
  ```

<details>
<summary>Delivery, retries, and state flow</summary>

- RabbitMQ delivery is at least once.
- A worker crash after an object upload and before acknowledgement can repeat the upload and callbacks.
- We have an idempotency key added to each job status report callback call. But it is up to client where the presigned URL will upload the file. AKA they can be the same place or unique keys each time we request a new presigned upload URL.

Here is how it would look like:

![state machine](./state-machine.png)

```mermaid
stateDiagram-v2
  [*] --> accepted: POST /synthesize → 202
  accepted --> queued: confirmed RabbitMQ publish
  queued --> generating
  generating --> getting_upload_url
  getting_upload_url --> uploading
  uploading --> completed: PUT succeeds; acknowledge
  generating --> retrying: retryable failure
  getting_upload_url --> retrying: retryable failure
  uploading --> retrying: retryable failure
  retrying --> queued: delayed retry, attempt + 1
  retrying --> failed: attempts exhausted
  generating --> failed: terminal failure
  getting_upload_url --> failed: terminal failure
  uploading --> failed: terminal failure
  failed --> [*]: failed callback attempted; acknowledge
  completed --> [*]
```

Per-message retry TTL is not a precise scheduler: RabbitMQ expires a message only when it reaches the queue head. A short retry can wait behind an earlier longer retry. Monitor actual retry delay; use delay buckets or a delayed-message mechanism if this becomes unacceptable.

You must use HTTPS callback endpoints in production. URL credentials and fragments are rejected. Set `SYNTHESIZE_CALLBACK_ALLOWED_HOSTS` to the exact client hostnames allowed to receive callbacks; an empty value permits any valid HTTP(S) hostname.

The service declares durable exchanges and quorum queues (`synthesize.work` and `synthesize.dlq`) with `prefetch=1`. Durable declarations alone do not preserve broker data through replacement or host loss: configure persistent storage for the self-hosted RabbitMQ deployment. See RabbitMQ's [quorum queue](https://www.rabbitmq.com/docs/quorum-queues) and [persistence](https://www.rabbitmq.com/docs/persistence-conf) guidance.

`x-delivery-limit` protects against repeated crash redeliveries. It is not the application retry count: explicit retries publish a new message, resetting broker delivery count while preserving `x-attempt`.

</details>
