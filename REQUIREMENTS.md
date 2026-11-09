# Migration to Go

Port the TypeScript service (`src/`, ~5.4k lines including specs) to Go with the same external behavior: same endpoints, same env variables, same RabbitMQ topology, same callback contract, same Docker image tag line.

> [!IMPORTANT]
>
> Do all the work for a step (code, tests, lint, docs), then stop before committing. Wait for the owner's review and explicit green light before running `git commit`. Do not start step N+1 until step N is committed.

Each step is small enough to review and study on its own, and ends with something runnable.

## Ground rules

- Behavior parity is the goal. `.github/docs/APIs.md` and `.github/docs/config.md` are the contract; if the Go code and these docs disagree, the Go code is wrong, except for the deliberate changes recorded in the step notes (step 13 updates the docs).
- No new features, no new env variables, no new endpoints.
- Prefer the standard library. Every third-party dependency needs your approval before it is added (candidates are listed per step).
- The TS code is a read-only reference for behavior. It may stop building or passing at any point; nothing is spent keeping it working. It is deleted in step 13.
- Tests use testify and follow AAA, no conditionals in tests (same rules as today, by convention and `testifylint`). Integration tests use testcontainers.
- Lint is strict from step 1 (see [Lint configuration](#lint-configuration)); a step is not done with lint warnings.
- Use the latest stable Go release and idiomatic approaches as of today.

## Decisions

Decided:

- Layout: `go.mod` at the repo root, `cmd/server` and `internal/{config,logging,redact,httpapi,piper,synthesis,amqp,worker}`. No `src/`, no `pkg/`.
- Router: `github.com/go-chi/chi`. Its middleware is plain `func(http.Handler) http.Handler`, so it composes with `otelhttp` and stdlib tests.
- Logging: stdlib `log/slog` with a small internal package (`internal/logging`), not a third-party logger. See [Logging and OpenTelemetry](#logging-and-opentelemetry).
- Telemetry: official OpenTelemetry Go SDK with OTLP HTTP exporters.
- AMQP client: `github.com/rabbitmq/amqp091-go`, wrapped by our own small `internal/amqp` package that owns reconnection. See [AMQP client choice](#amqp-client-choice).
- Tests: `github.com/stretchr/testify` (`require` for preconditions, `assert` for checks, `testifylint` in lint). Integration tests use `testcontainers-go` and run behind the `integration` build tag, so `go test ./...` stays fast.
- Lint: `golangci-lint` v2 with the config below; `dupl` replaces `jscpd`; `depguard` is enabled with an allowlist.
- Task runner: a `Makefile` (modeled on the Beatrice one, Go targets: `help`, `init`, `run`, `build`, `test`, `integration_test`, `lint_check`, `lint`, `clean`). CI and git hooks call `make` targets instead of repeating raw commands. New steps add or adjust targets when they need them.
- Git hooks: `lefthook` (Go binary, `lefthook.yml`), pinned as a Go `tool` in `tools/go.mod` like golangci-lint. It replaces husky in step 13, when `package.json` is deleted; until then husky stays and its pre-commit runs the Go lint. `.husky/pre-push` (TS e2e through Docker) is already removed. Hooks: pre-commit runs the Go lint; add a pre-push `go test ./...` only if you ask for it.
- Log shipping: `OTEL_LOG_SHIPPING` (`otlp` default, or `stdout`), ignored when `OTEL_ENABLED=false`.

## Logging and OpenTelemetry

Go has the same pieces as the Node packages you listed, but spread differently. There is no logging framework to adopt: slog is the standard, and OTel ships a bridge for it.

| Node                                     | Go                                                                                         |
| ---------------------------------------- | ------------------------------------------------------------------------------------------ |
| `@opentelemetry/api`                     | `go.opentelemetry.io/otel` (+ `/trace`, `/propagation`)                                    |
| `sdk-node`, `sdk-trace-base`, `sdk-logs` | `go.opentelemetry.io/otel/sdk`, `sdk/trace`, `sdk/log`                                     |
| `exporter-trace-otlp-http`               | `.../exporters/otlp/otlptrace/otlptracehttp`                                               |
| `exporter-logs-otlp-http`                | `.../exporters/otlp/otlplog/otlploghttp`                                                   |
| `instrumentation-http`                   | `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` (server and client)        |
| `instrumentation-winston`, `winston-transport` | `go.opentelemetry.io/contrib/bridges/otelslog` (slog handler that emits OTel log records) |
| `instrumentation-amqplib`                | None exists. Inject/extract trace context through AMQP headers by hand (step 12).          |

How it fits together:

- Log shipping is controlled by its own variable, independent of `OTEL_ENABLED` (which keeps controlling traces): `OTEL_LOG_SHIPPING` accepts `otlp` (default, pushes logs to the collector through the `otelslog` handler) or `stdout` (logs only go to stdout, for a shipper such as Alloy that tails them). It replaces `OTEL_WINSTON_DISABLE_LOG_SENDING`, which is Winston-specific and has no Go meaning.
- `internal/logging` builds one `*slog.Logger` from two handlers: a stdout handler (PLAIN_TEXT or JSON, as in `config.md`) that is always on, and the `otelslog` handler when `OTEL_LOG_SHIPPING=otlp`. Fan-out is a small custom handler, or `slog.NewMultiHandler` if your Go version has it (verify).
- Trace correlation depends on passing `context.Context` to every log call (`InfoContext`, not `Info`). The `sloglint` linter enforces it. The stdout handler also adds `trace_id` and `span_id` from the context, matching the Winston behavior documented in `config.md`.
- HTTP server: `otelhttp.NewMiddleware` mounted on the chi router creates server spans. Use the chi route pattern for span names so spans are not named per URL.
- HTTP client (callbacks, upload): `otelhttp.NewTransport` on the `http.Client` propagates trace context. Do not use an instrumented client for the OTLP exporter itself, the docs warn it recurses.
- Request/response logging is not provided by any of these. It is a small chi middleware of our own (about 40 lines, replacing `src/middlewares/logging.ts`).
- No preload trick is needed: providers are created in `main` before the server starts and shut down (flushed) on SIGTERM.
- The standard `OTEL_EXPORTER_OTLP_*` variables are read by the exporters themselves. The project-specific `OTEL_ENABLED` and `OTEL_BATCH_*` variables from `config.md` must be mapped to processor options by hand.

Caveat: `otelhttp` and `otelslog` are still `v0.x` modules (the logs signal is the least mature). Pin exact versions and upgrade deliberately.

`OTEL_LOG_SHIPPING` is ignored when `OTEL_ENABLED=false`, so its default only matters once telemetry is on. It is a rename plus a new variable relative to `config.md`, an approved exception to "no new env variables"; `config.md` is updated in step 13.

## AMQP client choice

Options considered:

| Option                         | Pros                                                                                         | Cons                                                                                                      |
| ------------------------------ | -------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `amqp091-go`                   | Maintained by the RabbitMQ team, direct AMQP 0.9.1 semantics, all official tutorials use it. | No reconnection: we write it (about 100 lines).                                                           |
| `wagslane/go-rabbitmq`         | Wraps amqp091-go; reconnection with backoff, confirm mode, `Ack`/`NackRequeue` return values. | Single maintainer; hides channels and topology, which this service depends on (ack ordering, retry queue, `x-attempt` headers). |
| `rabbitmq-amqp-go-client`      | Official, recovery and topology restore built in.                                            | AMQP 1.0, a different model than the documented prefetch/TTL/`x-delivery-limit` behavior; fewer tutorials. |

Recommendation: `amqp091-go`. The service's correctness is in the details a wrapper abstracts (when to ack, publishing to the retry queue before acking, confirms, headers for trace propagation), and understanding them is part of the goal. Reconnection is the one real saving of `go-rabbitmq`; keep it isolated in `internal/amqp` so the choice can be revisited without touching the worker. Before step 9, check `go-rabbitmq` recent release activity and whether it exposes arbitrary queue arguments and headers, in case you want to switch.

## Lint configuration

The linter list you pasted is `golangci-lint` v1 style. Several entries no longer exist in v2 (checked against the current golangci-lint docs):

| Pasted entry                           | In v2                                                                                 |
| -------------------------------------- | ------------------------------------------------------------------------------------- |
| `deadcode`, `structcheck`, `varcheck`  | Removed. `unused` covers them.                                                        |
| `gosimple`                             | Merged into `staticcheck`.                                                            |
| `typecheck`                            | Not a configurable linter anymore; type errors always fail the run.                   |
| `gofumpt`, `goimports`                 | Moved to the separate `formatters` section.                                           |
| `goerr113`, `gomnd`                    | Renamed `err113`, `mnd`.                                                              |
| `rowserrcheck`, `sqlclosecheck`        | Left out: there is no SQL in this service.                                            |
| `format = ...`, `exclude-use-default`  | v1 TOML options. v2 uses `.golangci.yml` with `version: "2"`.                         |

Everything else is kept, plus a few that target unmaintainable code directly (`gocognit`, `nestif`, `funlen`, `errorlint`, `contextcheck`, `noctx`, `gosec`, `exhaustive`, `nilerr`, `errname`, `dupl`, `containedctx`, `gocritic`, `intrange`, `prealloc`, `thelper`, `wastedassign`). Remove any you find too noisy after step 1.

The `depguard` allowlist holds only approved dependencies; adding a new one means editing this list, which is the approval checkpoint.

The full configuration lives in `.golangci.yml` and is the source of truth. Every setting of every enabled linter is written out explicitly, with the linter's default value unless a line is marked `# not default`. The non-default values are:

- `gocyclo` 10 and `gocognit` 15 (default 30).
- `nakedret` `max-func-lines: 0` (default 30): no naked returns at all.
- Stricter than default: `errcheck` `check-type-assertions` and `check-blank`; `govet` runs `shadow`; `bodyclose` `check-consumption`; `unparam` `check-exported`; `sloglint` `context: all`, `attr-only`, `static-msg`, `no-raw-keys`; `testifylint` `enable-all`.
- Extra linters: `containedctx`, `gocritic`, `intrange`, `prealloc`, `thelper`, `wastedassign`.
- Extra `revive` rules (marked `# not default` in the file), including `early-return`, `deep-exit`, `unchecked-type-assertion`, `datarace`, `defer`, `waitgroup-by-value`, `range-val-in-closure`, `identical-*` branch checks, `import-shadowing`.
- Test files are excluded from `mnd`, `err113`, `funlen`, `dupl`, `goconst`.

Other notes:

- `nestif` is set to its default of 5.
- `depguard` uses the default `list-mode: original`, which with a non-empty allow list rejects every import not on the list (checked by importing `golang.org/x/sync/errgroup`, which was rejected).
- Linters without settings: `contextcheck`, `err113`, `errname`, `nilerr`, `noctx`, `tparallel`.
- `exhaustive` and `sloglint` in this golangci-lint version reject some options the reference file lists (`ignore-enum-*`, `explicit-exhaustive-*`, `key-naming-case: ""`), so those are omitted. `golangci-lint config verify` validates the file.
- `govet` analyzers and `gosec` rule-level settings are left at their defaults and not listed individually.

Notes for review:

- `err113` forbids ad-hoc `errors.New` / `fmt.Errorf` without wrapping a sentinel or typed error. This pushes the error classification needed in steps 8 and 11 (retryable vs terminal), and is also the noisiest linter at first.
- Thresholds (10, 15, 60 lines) are starting points; the worker in step 10 will force you to split functions, which is intended.
- Wire it in: `make lint_check` in CI and in the pre-commit hook, `make lint` to apply fixes and formatting.

## Steps

### 1. Toolchain and skeleton

- Goal: `go build ./...` and `go test ./...` pass on an empty service; CI runs them.
- Work: `go mod init`, `.golangci.yml` from the section above, `.gitignore` entries, an empty `main`, the `Makefile`, and CI/hook changes so the TS project is no longer built or tested.
- golangci-lint is pinned as a Go `tool` in a separate `tools/go.mod` and run through `make lint_check` / `make lint` (raw command: `go tool -modfile=tools/go.mod golangci-lint run`). The golangci-lint docs do not recommend `go tool` (binary install is their recommendation) but say a dedicated module file is the way to do it if you do.
- Learn: modules and `go.mod`, packages vs directories, `internal/` visibility, `go vet`, `gofmt`, `golangci-lint`.
- Done when: `make build`, `make test` and `make lint_check` pass locally; the release workflow runs them instead of `pnpm lint`; the pre-commit hook runs `make lint_check`; `golangci-lint` stays a dev-only tool in `tools/go.mod`, never part of the production module or image.
- Left for step 13: replace husky with lefthook (see the Git hooks decision), and the release workflow still sets up Node and pnpm because `semantic-release` runs through `pnpm dlx`, and the Dockerfile still builds the TS app.

### 2. Configuration

- TS source: `src/shared/config.ts`, `config.spec.ts`.
- Goal: one `Config` struct loaded from env with the same defaults and the same fail-fast validation (positive integers, HTTPS callbacks required unless `APP_ENV` is development/test, comma-separated allowed hosts).
- Learn: structs, `os.LookupEnv`, `strconv`, returning `error` instead of throwing, table-driven tests.
- Done when: every row in `config.md` has a test (valid, default, invalid).
- Implemented in `internal/config`: `Load(lookup)` takes an `os.LookupEnv`-shaped function (tests pass a map), converts `*_MS` variables to `time.Duration`, and returns all invalid variables at once through `errors.Join` instead of stopping at the first.
- Deliberate differences from the TS code:
  - `PORT`, `MAX_CONCURRENCY`, `MAX_BODY_SIZE_BYTES`, `OTEL_BATCH_*` and `OTEL_LOGS_BATCH_*` are now validated as positive integers (TS silently produced `NaN`); `+5`, `1.5` and values above 2^31-1 are rejected.
  - `LOGGING_MODE` and `LOGGING_LEVEL` reject unknown values (TS cast them unchecked).
  - `OTEL_LOG_SHIPPING` (`otlp` default, `stdout`) is validated only when `OTEL_ENABLED=true`; it replaces `OTEL_WINSTON_DISABLE_LOG_SENDING`.
  - `NODE_ENV` is renamed `APP_ENV` (same values and default `production`); a breaking change for the major version.
  - `OTEL_ENABLED` stays lenient: anything but exactly `true` means disabled.
  - `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` and `OTEL_EXPORTER_OTLP_ENDPOINT` are read by the OTel Go SDK itself in step 12; the endpoint is still exposed on `Config` for logging and diagnostics.
- Not wired yet: `main` does not call `Load` until step 4 needs it.

### 3. Logging

- TS source: `src/shared/utils/logger.ts`, `app-logger.ts`.
- Goal: `LOGGING_MODE` (PLAIN_TEXT or JSON), `LOGGING_LEVEL`, `service` and `context` fields, same shapes as shown in `config.md`.
- Learn: `log/slog`, handlers, `slog.Attr`, passing a logger vs a global, `context.Context` basics.
- Work: stdout handler only in this step; `OTEL_LOG_SHIPPING`, the OTel handler and `trace_id` injection are added in step 12 behind the same `internal/logging` constructor.
- Decided: `verbose` is `slog.Level(-2)` (`logging.LevelVerbose`), between info (0) and debug (-4), which is where Winston puts it. Level order is error, warn, info, verbose, debug, so `LOGGING_LEVEL=verbose` hides debug and `debug` shows everything.
- Implemented in `internal/logging`: `New(cfg, service, w)` returns a `*slog.Logger`; `Named(l, "Bootstrap")` adds the `context` field. JSON mode is slog's JSON handler with `timestamp`/`level`/`message` renamed; PLAIN_TEXT is a small custom handler (`plain.go`) that prints the line and delegates the trailing JSON to an inner JSON handler.
- Known differences from the Winston output: JSON fields are ordered `timestamp, level, message, service, context, ...` instead of `message` last (key order is not significant); PLAIN_TEXT also prints the lower-case level (padded to 7) after the timestamp, which the TS format omitted.
- Done when: both output formats match the examples in `config.md`.

### 4. HTTP server, `GET /health`, request logging, graceful shutdown

- TS source: `src/server.ts`, `src/routes/health.ts`, `src/middlewares/logging.ts`.
- Goal: server on `PORT`, `/health` returns `200 text/plain`, every request and response is logged, SIGTERM drains in-flight requests.
- Learn: `net/http`, chi routing and `r.Use`, `http.Handler` and middleware as function wrapping, `httptest`, `signal.NotifyContext`, `Server.Shutdown`.
- Done when: `curl /health` works and a test covers the middleware.
- Implemented in `internal/httpapi` (`NewRouter`, request-logging middleware, `Serve`) and `cmd/server/main.go` (config, logger, signal handling, listener).
- Parity: `/health` is not logged; unknown paths and wrong methods both return `404 text/plain "Not Found"`; the request log keeps `Incoming <METHOD> <url>` / `Completed ...` with `method`, `url`, `headers`, `statusCode`, `duration` (ms). Field shapes follow Go, not Node: `headers` is Go's `http.Header` (canonical names such as `X-Request-Id`, each value a list), not Node's lower-cased `req.headers` object. The Go service is a new major version and does not need to match the TS log shape.
- Differences: the bootstrap messages are static (`TTS API running` with a `port` attribute, `Piper model` with a `path` attribute) because `sloglint` requires static messages; the request messages stay dynamic (documented format) behind a `//nolint:sloglint`. Go keeps the `Host` header out of `r.Header`, so it is not in the logged `headers`. Secrets are redacted (the TS service logged everything): headers and query parameters whose name contains `auth`, `token`, `secret`, `key`, `password`, `passwd`, `signature`, `credential`, `session` or `cookie` are logged as `[REDACTED]`; `Authorization` keeps its scheme (`Bearer [REDACTED]`).
- Shutdown: first SIGTERM/SIGINT stops accepting and waits for in-flight requests with no deadline (like Node's `server.close`); a second signal kills the process. `ReadHeaderTimeout` is 10s; there is no read/write timeout because `/speak` streams. Decided: no in-process drain deadline; the orchestrator force-kills a stuck container, and this is documented in `.github/docs/scaling.md` (Shutdown and stuck requests).
- Not yet: AMQP/worker startup (steps 9-10) in `main`. Piper/ffmpeg verification was added in step 6.

### 5. Small utilities

- TS source: `semaphore.ts`, `word-counter.util.ts`, `fetch-with-timeout.util.ts`, `body.ts`, `http.ts`.
- Goal: concurrency limiter, word counter, HTTP client call with timeout, body reading with a max size (413 on overflow).
- Learn: goroutines vs event loop, buffered channels as semaphores (or `golang.org/x/sync/semaphore`), `context.WithTimeout`, `http.MaxBytesReader`, `io.Reader`.
- Done when: the behavior in each TS spec is reproduced by a Go test.
- Implemented: `internal/httpapi/semaphore.go` (buffered channel; `acquire(ctx)` / `release()`), `body.go` (`decodeJSON[T]`), `json.go` (`writeJSON`), and `internal/piper/wordcount.go` (`WordCount`). All are unexported except `WordCount`, which `synthesis` and `piper` share. They are wired into `/speak` in step 7.
- Request bodies: `decodeJSON[T]` reads one JSON object with `http.MaxBytesReader`, rejects unknown fields and trailing data, and validates the result with `go-playground/validator` struct tags (the class-validator equivalent). It returns `errBodyTooLarge`, `errInvalidJSON` or a `*validationError` listing `{field, rule}` pairs (a wrong JSON type is rule `type`). An empty body counts as `{}`, so it fails `required`. `notblank` is a custom tag registered next to the validator. Step 7 maps these to 413, 400 and 400; step 8 reuses `decodeJSON` for `/synthesize`.
- Differences: `acquire` takes a `context.Context`, so a request that disconnects while queued stops waiting (the TS semaphore waited forever). `/speak` no longer accepts a plain-text body: it takes only `{"text": "..."}`. Unknown fields are rejected, and a non-string `text` is a 400 naming the field (TS treated it as empty). `sendBinary` is not ported: `/speak` streams.
- Not ported: `fetchWithTimeout`. In Go the caller passes `context.WithTimeout(parent, d)` to `http.NewRequestWithContext`, which covers both the timeout and the external abort. It is exercised with the callbacks in step 10. One difference: the Go timeout also covers reading the response body.

### 6. Piper and ffmpeg pipeline

- TS source: `src/services/piper.ts`, `piper.spec.ts`.
- Goal: read sample rate from `<model>.onnx.json` at startup; run `piper --output-raw` piped into `ffmpeg` producing MP3; expose it as an `io.Reader` of MP3 bytes; kill both processes and wait for exit when the context is cancelled.
- Learn: `os/exec` (`CommandContext`, `StdoutPipe`, `Wait`), process groups and signals on Linux, `io.Pipe`, `io.Copy`, goroutine leaks, `errgroup`.
- Risk: this is the hardest part to get right (cancellation and cleanup). Review it closely.
- Done when: a test with fake `piper`/`ffmpeg` scripts proves cancellation kills both and frees the slot; one real run produces a playable MP3.
- Implemented in `internal/piper`: `New(ctx, modelPath)` is the startup check (piper `--help`, model, sidecar, `ffmpeg -version`, sample rate from `<model>.json`) and `main` calls it before listening; `(*Synthesizer).Synthesize(ctx, text)` returns a `*Stream` (an `io.ReadCloser` of MP3 bytes).
- Design: the two pipes (piper to ffmpeg, ffmpeg to us) are `os.Pipe`s handed to the children as their stdio, so no copying goroutines run in our process. Both processes get their own process group and are killed as a group (`SIGKILL`), so nothing they spawn outlives them. One goroutine reaps both; the first failure kills the other (a crashed ffmpeg stops a CPU-bound piper). `Read` returns the failure instead of `io.EOF` when a process failed, so a truncated MP3 is never mistaken for a complete one. `Close` kills what is left and returns only after both processes are gone, which is what lets step 7 release the semaphore slot safely.
- Differences: errors are returned (the TS code threw); stderr of both processes is discarded, as in TS; the TS `synthesizeTextToMp3Buffer` (temp files, `spawnSync`) is not ported because nothing calls it. Linux/Unix only (process groups).
- Tests: fake `piper`/`ffmpeg` shell scripts on `PATH` check the arguments, the data flow, piper and ffmpeg failures, context cancellation, early `Close`, and that a grandchild process dies too (removing the group kill makes them fail). `real_integration_test.go` (`-tags integration`, needs `PIPER_MODEL`) ran the real piper and ffmpeg in a throwaway Docker image: 3.1 s of mono 22050 Hz MP3, confirmed with `ffprobe`.

### 7. `POST /speak`

- TS source: `src/routes/speak.ts`.
- Goal: JSON body only (`{"text": "..."}`, trimmed), 400/413/500 mapping, streamed `audio/mpeg` with the documented headers, semaphore held for the whole request, client disconnect cancels the pipeline.
- Learn: `http.Flusher`, `r.Context()` cancellation on disconnect, chunked responses, error handling after headers are sent.
- Done when: the curl examples in `APIs.md` work and the cancellation scenario is tested.
- Implemented in `internal/httpapi/speak.go`: `NewRouter(logger, synth, Options{MaxConcurrency, MaxBodySizeBytes})` registers `POST /speak`. The handler acquires a semaphore slot first (so the slot covers body parsing too, as in TS), decodes `{"text": "..."}` with `decodeJSON`, trims it, and calls `Synthesize(r.Context(), text)`. `defer release` is registered before `defer stream.Close`, so `Close` (which waits for both processes) runs first and the slot is freed only afterwards.
- Errors are JSON `{"error": "..."}`: 413 `Request body too large`, 400 `Invalid JSON body`, 400 `Invalid request body` with `fields: [{field, rule}]` for validation, 500 `Synthesis failed` (the cause is logged, not returned).
- Streaming: the response headers (`audio/mpeg`, `Content-Disposition: inline; filename="<unix-ms>-<random>.mp3"`, `Cache-Control: no-store`, chunked) are sent with the first audio chunk, and each chunk is flushed. A pipeline that fails before producing audio therefore answers a real 500 (TS always sent 200 first). A failure after the first chunk calls `panic(http.ErrAbortHandler)`, the documented way to cut the connection, so the client sees a truncated response (TS did `res.destroy`). The request-logging middleware now logs `Completed` in a `defer` and adds `aborted: true|false`: true when the handler cut the connection (`panic(http.ErrAbortHandler)`, recovered and re-panicked) or the client left (request context cancelled). `statusCode` stays what was actually sent, so an aborted stream logs 200; `aborted` is what tells it apart. Open: whether to rename the `Completed` message.
- Client disconnect: `r.Context()` is cancelled by `net/http`, which kills the process groups through `Synthesize`; a request still queued for a slot stops waiting.
- Differences: JSON only; the `x-trace-id` response header is added with OpenTelemetry in step 12; no cause text is returned to the client.
- Tests (`speak_test.go`, real router on `httptest.NewServer` with fake `piper`/`ffmpeg` scripts): headers and body, trimming, six invalid-body cases, 413, 500 before audio, abort after audio, and disconnect killing piper and handing the slot to the queued request. Mutation checks: passing `context.Background()`, removing the abort, not closing the stream, not trimming, and each half of the `aborted` rule each fail a test.

### 8. Synthesize domain types and request validation

- TS source: `src/shared/synthesize.ts`, `job-events.ts` and their specs.
- Goal: request struct, validation (text, `genUploadUrl`, `statusCallbackUrl`, HTTPS rule, allowed hosts, no URL credentials or fragments), job event types with the exact status names and fields.
- Learn: JSON struct tags, `encoding/json`, `net/url`, custom `error` types and `errors.Is`/`errors.As`, enums via typed constants.
- Done when: field names and status values match the TS code and the docs verbatim.
- Implemented in `internal/synthesis`:
  - `Request{Text, GenUploadURL, StatusCallbackURL}` (JSON `text`, `genUploadUrl`, `statusCallbackUrl`) carries `required,notblank` tags, so `decodeJSON[synthesis.Request]` in step 9 handles the shape. `Request.Normalize(config.Callback)` trims and checks the rules that need configuration (absolute URL, `http`/`https`, HTTPS when required, no user info, no fragment, host allow-list) and returns the normalized request. It reports every broken URL as a `*ValidationError` of `{field, rule}` violations (rules `absolute`, `scheme`, `https`, `credentials`, `fragment`, `host`), the same shape as body validation errors, so step 9 answers 400 with `fields`. The TS code stopped at the first error and used free-text messages.
  - `StatusPayload` with constructors `Queued`, `Generating`, `Uploading`, `Completed`, `Failed`: a single struct whose constructors set only the fields their status has (a Go replacement for the TS union type). Status names, field names and `failedAt`/`error{code,message}` are the documented ones. `percent` is clamped to 0-99 and is a pointer so `0` is still sent. `failedAt` is a UTC `time.Time` (RFC 3339).
  - `internal/redact` (new package, shared by `httpapi` and `synthesis`): `IsSensitive(name)` and `Query(rawQuery)`. Matching is by exact name from one list (case-insensitive, `_` equals `-`), decided over substring matching because substrings hide harmless values such as `public_key` that help when debugging. The price is that a secret under an unlisted name is logged, so extend the list in `redact.go` when one turns up. Deferred: letting operators add names at runtime (for example `LOGGING_REDACT_NAMES`). `httpapi` uses them for headers and the logged request URL; they replaced `isSensitive` and the query loop that lived in `httpapi/redact.go`.
  - `synthesis.RedactUploadURL` for logging presigned URLs: `scheme://host/path` plus the query through `redact.Query`, so signature, credential and token parameters are hidden and the rest (`X-Amz-Expires`, `X-Amz-Date`, ...) stays visible for debugging, as in TS. User info and the fragment are dropped. Differences from TS: the list also has `AWSAccessKeyId` and `signature` (S3 v2) and the shared header and token names, and `se`/`sr` (Azure expiry and resource) are not redacted. A URL that cannot be parsed is returned as is, on purpose (decided: the operator needs to see what the caller sent in production), so it can leak a signature; the `APIs.md` sentence about the redaction-failure path stays accurate.
- Differences: only the job failure codes are ported (`invalid-job-message`, `generation-failed`, `upload-url-failed`, `upload-failed`); `invalid-request`, `invalid-callback-url` and `request-body-too-large` were HTTP error codes of TS and are replaced by the `{error, fields}` response. `SynthesizeJobHeader`/`SynthesizeJobMessage` belong to the AMQP message and come in step 9.
- Deferred: `SynthesizeJobEvent`. Nothing consumes it before step 10, and in Go the natural form is slog attributes at the log call, so it is decided there.

### 9. AMQP topology and publisher

- TS source: `src/services/amqp.ts`, `amqp.spec.ts`, `src/routes/synthesize.ts`.
- Goal: declare the durable exchanges and quorum queues (work, retry, DLQ, DLX) with the same arguments, publisher confirms, `POST /synthesize` returning 202 with `jobId` or 503 on publish failure.
- Learn: AMQP concepts (exchange, queue, binding, ack, confirm), `amqp091-go` channels, reconnect handling, how `amqp091-go` handles concurrent publishing.
- Done when: an integration test against a real RabbitMQ (testcontainers, `integration` build tag) shows the topology matches what the TS service declares.
- Implemented:
  - `internal/amqp` (`broker.go`, `topology.go`): `Connect(config.RabbitMQ)` dials (10 s timeout), declares the topology and turns on publisher confirms; `(*Broker).PublishJob(ctx, job, authorization)` publishes and returns only after the broker confirmed (`DeferredConfirmation.WaitContext`); `Close()`. Topology is the TS one: durable direct exchanges `synthesize` and `synthesize.dlx`; quorum queues `synthesize.work` (`x-delivery-limit` 5, dead-letters to the DLX), `synthesize.dlq` (bound to the DLX) and `synthesize.retry` (unbound, dead-letters back into `synthesize`), all with routing key `synthesize`. Prefetch is a consumer-channel setting, so it comes with the consumer in step 10.
  - Message: persistent, `application/json`, `messageId` = job ID, `timestamp`, headers `timestamp` (ms) and `authorization` (only when the caller sent one; TS stored the string `"undefined"` when it did not). Body `{jobId, text, genUploadUrl, statusCallbackUrl}` (`synthesis.Job`, the ID is `uuid.NewString()` from `github.com/google/uuid`, a random UUID v4; the standard library has no UUID package). `synthesis.RoutingKey` is shared with the consumer.
  - Channels: `amqp091-go` already serialises concurrent publishes on a channel (`PublishWithDeferredConfirm` holds the channel's lock while it registers the confirmation and writes the message), so concurrent publishing is safe. The broker's own mutex guards `conn`, `pub` and `closed`, which a reconnect replaces; only the call that hands the message to the channel holds it, waiting for the confirm does not.
  - Reconnect (TS had none, so one broker restart meant 503 until the process restarted): if the connection or channel is closed, the next `PublishJob` re-dials and re-declares the topology; while the broker is down it fails (503) and tries again on the next request. Reconnecting consumers is part of step 11.
  - `POST /synthesize` (`httpapi/synthesize.go`): `decodeJSON[synthesis.Request]`, then `Normalize` with the configured callback policy, then publish with a 10 s timeout. 202 `{"jobId"}`; 400 `Invalid request body` (shape) or `Invalid callback URL` (both with `fields: [{field, rule}]`); 413; 503 `Unable to accept synthesize job` (the cause is logged, never returned: it can contain the broker URL). `httpapi` depends on a one-method interface, `JobPublisher`, defined next to its only caller, which `*amqp.Broker` satisfies. `main` connects at startup (fail fast, like TS) and closes the broker after the server has drained.
- Differences: JSON error shape (`{error, fields}`, no `code` field); the TS publish waited for confirms without a timeout.
- Not done: the trace context header (step 12), `mandatory` publishing (a job published while the work queue is missing would be confirmed but unroutable; the queue is declared at connect, so only an operator deleting it at runtime causes this).
- Tests: unit tests for the message properties and every `/synthesize` branch (fake publisher). Integration (`-tags integration`, testcontainers, `rabbitmq:4.3-management-alpine`, ~20 s): the topology is read back from the management API (exchange types, durability, queue type and arguments, bindings, and that the retry queue has none), a published job arrives with the right body, properties and headers, the broker reconnects after its connection is closed, and publishing after `Close` fails. Breaking the delivery limit, binding the retry queue, or disabling the reconnect each fails one of them.

### 10. Worker: consume, generate, upload

- TS source: `src/services/synthesize-worker.ts` (largest file), `synthesis.ts`.
- Goal: consume with `prefetch=1`, call `genUploadUrl`, run the pipeline, stream the MP3 to the presigned URL with idle and absolute timeouts, send status callbacks with `Idempotency-Key: <jobId>:<attempt>`, ack after terminal outcome.
- Learn: consumer loops, `context` propagation, `io.Pipe` for streaming upload, `http.NewRequestWithContext`, progress estimation from `SYNTHESIZE_WORDS_PER_MINUTE`.
- Note: split this into two reviews (generate+upload first, callbacks second) because of its size.
- Outbound destinations (SSRF), decided after an automated security review: `SYNTHESIZE_CALLBACK_ALLOWED_HOSTS` stays optional (empty still means any host, as documented). Whatever it says, the HTTP client used for `genUploadUrl`, the status callbacks and the upload refuses to connect to loopback, private, link-local (this includes the cloud metadata address 169.254.169.254), unspecified and multicast IPs. The check runs on the IP actually dialed (`net.Dialer.Control`), so a hostname that resolves to a private address or redirects to one is blocked too, and redirects are not followed. A host the operator lists in `SYNTHESIZE_CALLBACK_ALLOWED_HOSTS` is exempt, even when it is a private address (the listing is explicit consent; localhost callbacks in development or Compose need `localhost` in the list, which then also restricts clients to the listed hosts). No new environment variable and no `APP_ENV` logic. Document it in `config.md` and `APIs.md` (step 13).
- Done when: the happy path e2e test passes.
- Part A (generate and upload) implemented; part B (status callbacks) is next:
  - `internal/amqp/consumer.go`: `NewConsumer(cfg)` opens its own connection (publish and consume do not share one), sets prefetch 1 and `Run(ctx, handle)` calls `handle` for one job at a time. A nil result acknowledges, an error rejects without requeue (dead-lettered), and a job interrupted by shutdown (`ctx` cancelled) is left unacknowledged so RabbitMQ delivers it again. `Message{Body, Attempt, Authorization}` carries `x-attempt` (default 1) and the `authorization` header. Not done: reconnecting after a lost connection; the consumer returns an error and `main` stops the service so the orchestrator restarts it (step 11 replaces this).
  - `internal/worker`: `Worker.Process(ctx, amqp.Message)`: parse and check the job (`invalid-job-message`), generate the MP3 into a private temp directory (`generation-failed`), call `genUploadUrl` once per attempt with `Idempotency-Key: <jobId>:<attempt>` and the caller's `Authorization` (`upload-url-failed`), then PUT the file (`upload-failed`) with `Content-Type: audio/mpeg` and an exact `Content-Length`; the temp directory is always removed. Generation comes first and the URL second, as in TS, so a short-lived presigned URL is not wasted while audio is produced.
  - Why a temp file: the upload needs the length up front, which a stream does not have. The same as TS.
  - Upload limits: `SYNTHESIZE_UPLOAD_TIMEOUT_MS` bounds the whole PUT and `SYNTHESIZE_UPLOAD_IDLE_TIMEOUT_MS` the time without any bytes sent (a reader wrapper resets a timer on every read). Differences from TS: the idle timer stops once the whole body was sent, so waiting for the storage's response counts only against the absolute deadline; a 3xx is a failure, never followed.
  - Outbound HTTP (`worker/client.go`): the dialer refuses loopback, private, link-local (including 169.254.169.254), unspecified and multicast addresses on the address actually dialed, so a name resolving to one is blocked as well; no redirects and no proxy (a proxy would make the proxy the dialed address). A host listed in `config.Callback.AllowedHosts` is exempt: the match is on the host name in the URL, not on the address it resolves to, so another name for the same address stays blocked. Responses from the caller's server are read up to 1 MiB.
  - Errors: `jobError{code, message, cause}` with the codes of step 8. `net/http` errors carry the full request URL, which for an upload is a presigned URL with a signature, so the URL is stripped from the cause before it is returned or logged. The upload failure log shows `synthesis.RedactUploadURL`.
  - `worker.Synthesizer` is a one-method interface (`Synthesize(ctx, text) (io.ReadCloser, error)`); `main` adapts `*piper.Synthesizer` to it because that returns the concrete `*piper.Stream`.
  - `SynthesizeJobEvent` is decided as plain slog attributes (`jobId`, `attempt`, `textCharacterCount`, `fileSizeBytes`, `totalDuration`, `code`) at the log calls, no event struct.
  - Any failure currently rejects the job without requeue, so it lands in the DLQ. Retryable versus terminal and the retry queue are step 11.
  - Tests: worker unit tests use a fake synthesizer and real HTTP servers (every error code, redirects, URL not leaked, idle and absolute timeouts, a slow but steady upload surviving the idle timeout, temp files removed, every blocked address range including the v4-mapped form and `localhost`, a listed private host going through, and a second name for a listed address staying blocked); breaking the address check, the idle reset or the redirect rule each fails one. Consumer: header parsing unit test and integration tests (ack, dead-letter on error, redelivery on shutdown, one job at a time). The real piper and ffmpeg are not part of these tests; the happy path against them is the step 13 e2e.
- Part B (to do): the `queued`, `generating`, `uploading` and `completed` status callbacks with progress from `SYNTHESIZE_WORDS_PER_MINUTE`, then the `failed` callback.

### 11. Worker: retries, DLQ, failure semantics

- TS source: same worker file, state machine in `.github/docs/APIs.md`.
- Goal: retryable vs terminal failures, delayed retry via retry queue with `x-attempt` preserved, DLQ on exhaustion, `failed` callback with `failedAt` and `error`, at-least-once behavior on crash.
- Learn: error classification with wrapped errors, idempotency, redelivery.
- Deferred from step 9: extract the reconnect check in `amqp.Broker.publish` into a helper named `reconnect`, so it is easy to find when the consumer reconnect is written.
- Done when: every transition in the state diagram has a test.

### 12. OpenTelemetry

- TS source: `src/instrumentation.ts`.
- Goal: traces and logs over OTLP HTTP honoring every `OTEL_*` variable in `config.md`; trace context propagated HTTP -> AMQP -> worker -> callbacks; `trace_id` in logs.
- Work: follow [Logging and OpenTelemetry](#logging-and-opentelemetry): tracer and logger providers, `otelhttp` middleware and transport, `otelslog` handler, AMQP header carrier for producer/consumer spans, mapping of `OTEL_ENABLED` (traces), `OTEL_LOG_SHIPPING` (logs, separate), and `OTEL_BATCH_*`.
- Learn: OTel Go SDK setup, providers and shutdown, `otelhttp`, `propagation.TextMapCarrier`, the slog bridge, why Go needs no preload trick like Node's `--import`.
- Done when: a trace in the local Grafana/Tempo shows the full job path and links to Loki logs.

### 13. Tests, image, CI, cutover

- Port `tests/` e2e (RabbitMQ and object storage via testcontainers) and the load test expectations.
- Local builds go to `bin/` (`go build -o bin/<name> ./cmd/<name>`), which is gitignored; `dist/` is not used.
- Dockerfile: multi-stage build with a Go builder; the runtime stage still needs Python venv with `piper-tts`, ffmpeg, voice models, so only the Node/pnpm parts disappear. Non-root user and healthcheck stay.
- `compose.yml`: same services, only the app build changes.
- CI: replace the Node jobs; `semantic-release` and Docker Hub release stay as they are.
- Update `APIs.md` for the deliberate API changes (`/speak` accepts only JSON `{"text": "..."}`; error bodies for validation failures).
- Update `README`/`APIs.md`/`config.md`/`CONTRIBUTING.md` only where they mention Node, pnpm, or vitest/jest, and replace `OTEL_WINSTON_DISABLE_LOG_SENDING` with `OTEL_LOG_SHIPPING` in `config.md`.
- Cutover: compare the Go service's response headers, bodies, and callback payloads against `APIs.md` and the TS source, then delete `src/`, `package.json`, `pnpm-lock.yaml`, TS/ESLint/Jest configs, `.jscpd.json`, and husky hooks that call pnpm.
- Release note: this is a breaking change only if any observable behavior differs; otherwise `refactor:`/`perf:` as you decide.

## Out of scope

Anything not required for parity: new endpoints, in-memory long-lived Piper model, metrics endpoint, config changes, Kubernetes manifests.

## Suggested study order

1. A Tour of Go, then the Go standard library docs for `net/http`, `context`, `os/exec`, `io`.
2. Before step 6: goroutines, channels, `context` cancellation, and `os/exec` pipes.
3. Before step 9: RabbitMQ tutorials (work queues, acknowledgements, dead lettering) and the `amqp091-go` examples.
4. Before step 12: OpenTelemetry Go getting-started and propagation docs.
