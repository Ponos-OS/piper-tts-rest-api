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
- Git hooks: `lefthook` (Go binary, `lefthook.yml`), pinned as a Go `tool` in `tools/go.mod` like golangci-lint. It replaced husky in step 13C. lefthook v2.2 needs Go 1.27, so v2.1.17 is pinned (the last one for Go 1.26). Hooks: pre-commit runs `make lint_check`; pre-push runs `make test` and `make integration_test` (needs Docker; the e2e test builds the image, so a push takes about 10 minutes).
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

## RabbitMQ retry and delay: measurements

Question: with `x-opt-delivery-time` (RabbitMQ 4.4), does a message with a short delay wait behind an earlier-published message with a long delay (head-of-line blocking)? Answer: no. Measured on 2026-11-09 with random delays (200 ms to 8 s) and 10, 1000 and 10000 messages: every message arrived within 34 ms of its due time and none was held behind a longer-delay one, while the same delays through a retry queue with per-message TTL (the control) were blocked. The test program and its full logs live outside this repository, in `/tmp/rabbit-experiments` (see its `README.md`). The measurements below, taken earlier against `rabbitmq:4.3-management-alpine` (4.3.7) and `rabbitmq:4.4-management-alpine` (4.4.0) with 100 messages per scenario, agree with that result.

### Delaying a message

Two ways to make a message wait, with the same 100 delays through both:

| Scenario | Mechanism | Broker | Mean lateness | Late by more than 250 ms | Pairs out of due-time order |
|---|---|---|---|---|---|
| Random delays 0.2-6 s | Retry queue with per-message TTL and a dead-letter exchange | 4.3 | 2697 ms | 91 of 100 | 2474 of 4950 |
| same | same | 4.4 | 2698 ms | 91 of 100 | 2474 of 4950 |
| same | `x-opt-delivery-time` header | 4.4 | 4 ms | 0 | 0 |
| One 8 s message first, then 99 of 1 s | Retry queue with TTL | 4.3 and 4.4 | 6944 ms | 99 of 100 | |
| same | `x-opt-delivery-time` header | 4.4 | 7 ms | 0 | 0 |

- A retry queue with per-message TTL blocks at the head of the queue (queues are FIFO and an expired message is released only when it reaches the head). With one 8 s message first, all 100 messages arrived at 8.0 s; the 99 that were due at 1.0 s were about 7 s late. With random delays they arrived in clumps instead of evenly. 4.3 and 4.4 behave the same.
- `x-opt-delivery-time` (a header holding a `long`, milliseconds since the epoch) is new in RabbitMQ 4.4. An AMQP 0.9.1 publisher can set it, no plugin and no queue argument are needed. The message is stored in the queue at once and becomes visible to consumers at that time. Measured: messages arrived within 2-7 ms of their due time, in due-time order, and did not block each other.
- On 4.3 the header is silently ignored: all 100 messages arrived within 11 ms. Using it requires 4.4 or newer.
- Consequence for `Retry-After`: a retry queue can honor it per message, but one long `Retry-After` delays every retry behind it. With `x-opt-delivery-time` the service can honor it per message without that problem.

### Returning a message to the queue (`x-delivery-limit=3`, 100 messages)

| Consumer action | Deliveries per message | Dead-lettered | `x-delivery-count` header |
|---|---|---|---|
| `basic.nack(requeue=true)` | 25 each (stopped by the test), 2500 in total | 0 | never present; `x-acquired-count` present on 2400 redeliveries |
| `basic.reject(requeue=true)` | exactly 4 each (1 + 3 redeliveries) | 100 of 100 | 1, 2, 3 |

Both brokers gave the same numbers. `nack` does not count toward the delivery limit (documented since 4.3), `reject` does. A requeue loop with `nack` never ends by itself.

Delayed retry (queue arguments `x-delayed-retry-type=failed`, `x-delayed-retry-min=300`, `x-delayed-retry-max=2000`, consumer uses `reject(requeue=true)`): the broker held each message back for about `min * x-delivery-count`.

| Wait | 4.3 | 4.4 |
|---|---|---|
| before delivery 2 | 310 ms | 304 ms |
| before delivery 3 | 608 ms | 607 ms |
| before delivery 4 | 906 ms | 910 ms |

The delay is queue-wide and linear. The documentation describes a way to request an explicit time for a returned message only for AMQP 1.0 (`x-opt-delivery-time` in the `modified` outcome); nothing was found for an AMQP 0.9.1 consumer returning a message.

### What this means for the design

- Decided (Option A, implemented in step 11; the retry queue is gone): publish the retry back to the work exchange with `x-opt-delivery-time = now + delay`, then ack the original. Keeps `Retry-After`, our own attempt count and attempt limit; removes the retry queue and the blocking. Needs 4.4.
- Rejected (Option B), broker-native: `reject(requeue=true)` with `x-delivery-limit` and delayed retry. Least code, attempts counted by the broker (crash redeliveries included), exhausted jobs dead-lettered by the broker. Loses `Retry-After`, and attempts and delay become queue arguments fixed at declaration.
- RabbitMQ 4.4 is now the baseline of the project (`compose.yml` and the integration tests use `rabbitmq:4.4-management-alpine`).

### Not measured

- An AMQP 0.9.1 consumer requesting a delay when it rejects or nacks a message (undocumented).
- Delayed messages at scale, and any limit on their number or delay (the documentation states only that they count toward the queue length limit).
- Absolute timings on production hardware; these runs used local containers on one machine.

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
- Request bodies: `decodeJSON[T]` reads one JSON object with `http.MaxBytesReader`, rejects unknown fields and trailing data, and validates the result with `go-playground/validator` struct tags (the class-validator equivalent). It returns `errBodyTooLarge`, `errInvalidJSON` or a `*validationError` listing `{field, rule}` pairs (a wrong JSON type is rule `type`). An empty body counts as `{}`, so it fails `required`. `notblank` is a custom tag registered next to the validator. Step 7 maps these to 413, 400 and 400; step 8 reuses `decodeJSON` for `/synthesize`. (Changed in 13C: `readBody` reads the whole body first and `decodeJSON[T]` takes the bytes; anything but one JSON object is `errNotObject`; only an unknown field is `errInvalidJSON`; trailing data is no longer a separate case.)
- Differences: `acquire` takes a `context.Context`, so a request that disconnects while queued stops waiting (the TS semaphore waited forever). `/speak` took only JSON until 13C, which restored plain text like the TS. Unknown fields are rejected. `sendBinary` is not ported: `/speak` streams.
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
- Errors are JSON `{"error": "..."}`: 413 `Request body too large`, 400 `Invalid JSON body`, 400 `Invalid request body` with `fields: [{field, rule}]` for validation, 500 `Synthesis failed` (the cause is logged, not returned). (Changed in 13C: errors also carry `code`, and a non-object body is `Body must be a JSON object`; see "13C result".)
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
- Differences: only the job failure codes are ported (`invalid-job-message`, `generation-failed`, `upload-url-failed`, `upload-failed`); `invalid-request`, `invalid-callback-url` and `request-body-too-large` were HTTP error codes of TS and are replaced by the `{error, fields}` response. `SynthesizeJobHeader`/`SynthesizeJobMessage` belong to the AMQP message and come in step 9. (Changed in 13C: the three HTTP codes are back as the `code` key of error responses.)
- Deferred: `SynthesizeJobEvent`. Nothing consumes it before step 10, and in Go the natural form is slog attributes at the log call, so it is decided there.

### 9. AMQP topology and publisher

- TS source: `src/services/amqp.ts`, `amqp.spec.ts`, `src/routes/synthesize.ts`.
- Goal: declare the durable exchanges and quorum queues (work, retry, DLQ, DLX) with the same arguments, publisher confirms, `POST /synthesize` returning 202 with `jobId` or 503 on publish failure.
- Learn: AMQP concepts (exchange, queue, binding, ack, confirm), `amqp091-go` channels, reconnect handling, how `amqp091-go` handles concurrent publishing.
- Done when: an integration test against a real RabbitMQ (testcontainers, `integration` build tag) shows the topology matches what the TS service declares.
- Implemented:
  - `internal/amqp` (`broker.go`, `topology.go`): `Connect(config.RabbitMQ)` dials (10 s timeout), declares the topology and turns on publisher confirms; `(*Broker).PublishJob(ctx, job, authorization)` publishes and returns only after the broker confirmed (`DeferredConfirmation.WaitContext`); `Close()`. Topology is the TS one: durable direct exchanges `synthesize` and `synthesize.dlx`; quorum queues `synthesize.work` (`x-delivery-limit` 5, dead-letters to the DLX), `synthesize.dlq` (bound to the DLX), all with routing key `synthesize`. Step 11 removed the `synthesize.retry` queue. Prefetch is a consumer-channel setting, so it comes with the consumer in step 10.
  - Message: persistent, `application/json`, `messageId` = job ID, `timestamp`, headers `timestamp` (ms) and `authorization` (only when the caller sent one; TS stored the string `"undefined"` when it did not). Body `{jobId, text, genUploadUrl, statusCallbackUrl}` (`synthesis.Job`, the ID is `uuid.NewString()` from `github.com/google/uuid`, a random UUID v4; the standard library has no UUID package). `synthesis.RoutingKey` is shared with the consumer.
  - Channels: `amqp091-go` already serialises concurrent publishes on a channel (`PublishWithDeferredConfirm` holds the channel's lock while it registers the confirmation and writes the message), so concurrent publishing is safe. The broker's own mutex guards `conn`, `pub` and `closed`, which a reconnect replaces; only the call that hands the message to the channel holds it, waiting for the confirm does not.
  - Reconnect (TS had none, so one broker restart meant 503 until the process restarted): if the connection or channel is closed, the next `PublishJob` re-dials and re-declares the topology; while the broker is down it fails (503) and tries again on the next request. Reconnecting consumers is part of step 11.
  - `POST /synthesize` (`httpapi/synthesize.go`): `decodeJSON[synthesis.Request]`, then `Normalize` with the configured callback policy, then publish with a 10 s timeout. 202 `{"jobId"}`; 400 `Invalid request body` (shape) or `Invalid callback URL` (both with `fields: [{field, rule}]`); 413; 503 `Unable to accept synthesize job` (the cause is logged, never returned: it can contain the broker URL). `httpapi` depends on a one-method interface, `JobPublisher`, defined next to its only caller, which `*amqp.Broker` satisfies. `main` connects at startup (fail fast, like TS) and closes the broker after the server has drained. (Changed in 13C: error bodies also carry `code`.)
- Differences: JSON error shape (`{error, fields}`, no `code` field); the TS publish waited for confirms without a timeout.
- Not done: the trace context header (step 12), `mandatory` publishing (a job published while the work queue is missing would be confirmed but unroutable; the queue is declared at connect, so only an operator deleting it at runtime causes this).
- Tests: unit tests for the message properties and every `/synthesize` branch (fake publisher). Integration (`-tags integration`, testcontainers, `rabbitmq:4.4-management-alpine`, ~20 s): the topology is read back from the management API (exchange types, durability, queue type and arguments, bindings,), a published job arrives with the right body, properties and headers, the broker reconnects after its connection is closed, and publishing after `Close` fails. Breaking the delivery limit, or disabling the reconnect each fails one of them.

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
- Part B (status callbacks) implemented in `internal/worker/{status.go,progress.go}`:
  - Order, as in TS: `queued` when the job starts, `generating` (0 at once, then progress), `uploading` 0 after the upload URL was obtained, `uploading` progress, `completed` with `fileSizeBytes`. Payloads come from the step 8 constructors; each call has `Idempotency-Key: <jobId>:<attempt>` and the caller's `Authorization`, goes through the same restricted HTTP client, and is bounded by `SYNTHESIZE_STATUS_CALLBACK_TIMEOUT_MS`.
  - Best effort: a failed or non-2xx callback is logged as a warning (URL redacted) and never changes the job. Logging non-2xx is a small addition to TS, which only logged exceptions.
  - Key statuses (`queued`, `uploading` 0, `completed`) are sent and awaited. Progress is sent in the background and dropped while an earlier progress update is still in flight, so a slow client cannot slow the job down, and a progress update never overtakes a key status (`send` waits for the one in flight; `Process` waits before returning). TS started a request per progress update with no such limit, so updates could arrive out of order.
  - Progress: generation is estimated from the word count (`piper.WordCount`), `SYNTHESIZE_WORDS_PER_MINUTE` and a 128 kbit/s MP3, capped at 99; upload progress is bytes read from the file over its size, capped at 99. Both are throttled to growing percents at most every 500 ms (`throttle`). The estimate is approximate because ffmpeg uses a variable bitrate.
  - Not in this part: the `failed` callback. Whether a failure is terminal is decided in step 11 (retry or not), so `failed` is sent there; until then a failed job is rejected to the DLQ without telling the client.
  - Tests: unit tests for the estimate, percent, throttle and the two progress wrappers; worker tests for the exact status sequence, the headers on every callback, generation progress following the estimated size, and callback failures (500, never answering) leaving the job successful. Removing the progress writer or the `completed` status each fails a test.

### 11. Worker: retries, DLQ, failure semantics

- TS source: same worker file, state machine in `.github/docs/APIs.md`.
- Goal: retryable vs terminal failures, delayed retry through `x-opt-delivery-time` with `x-attempt` preserved, DLQ on exhaustion, `failed` callback with `failedAt` and `error`, at-least-once behavior on crash.
- Learn: error classification with wrapped errors, idempotency, redelivery.
- Done when: every transition in the state diagram has a test.
- Implemented:
  - Outcomes: `Worker.Process` returns an `amqp.Outcome` instead of an error: `Ack` (uploaded), `Retry(delay)` or `Reject` (failed for good, or an invalid message). `amqp.Consumer.Run(ctx, handle)` settles the message with it. A job interrupted by shutdown keeps no outcome: the consumer ignores it and leaves the message unacknowledged.
  - Classification (`jobError.retryable`), as in TS: anything that is not an HTTP answer (network error, timeout, crash of piper or ffmpeg) is retried; of HTTP answers only 408, 429 and 5xx are. Other 4xx, and 3xx because redirects are not followed, end the job. A 429 with `Retry-After` (seconds or an HTTP date) sets the delay, any other status waits `SYNTHESIZE_RETRY_DELAY_MS` times the number of the attempt that failed (30 s, 60 s, ...). The status is kept in the error, so the upload path passes the storage's `jobError` through instead of wrapping it (a test caught that the first version lost it).
  - Retry: while `attempt < SYNTHESIZE_MAX_ATTEMPTS`, the consumer republishes the message to the work exchange (persistent, same `messageId`, caller headers copied except the broker's `x-` ones, `x-attempt` = attempt + 1, `x-opt-delivery-time` = now + delay in epoch ms), waits for the broker's confirm, and only then acknowledges the original. RabbitMQ 4.4 holds the message in the work queue until it is due; older brokers ignore the header and redeliver at once.
  - Exhausted or terminal: the `failed` status (`failedAt` UTC, `error{code,message}`) is sent. A job that used all its attempts is then rejected, so it lands in the DLQ (decided; TS acknowledged it). A job that failed for a reason a retry cannot fix (a 4xx from the client's server) is rejected too (decided in 13C; it was acknowledged before), so every failed job is in the DLQ. The DLQ also receives invalid messages (no `failed` status, because the job cannot be trusted), messages whose retry could not be published, and messages over `x-delivery-limit` (crash loops).
  - Publishing the retry is mandatory: an exchange without a bound queue would otherwise confirm the message and drop it. If the retry cannot be published (unroutable, no confirm, timeout), the job is dead-lettered. TS requeued it, but a requeue loops at full speed because `x-delivery-limit` does not count explicit requeues (observed: over a thousand redeliveries in 12 s), so a job in the DLQ is the safer outcome.
  - Consumer reconnect: when the connection drops, `Run` redials (first attempt at once, then 1 s doubling up to 30 s) and keeps consuming; it declares the topology again, so it also works after a broker that lost its queues. A job being handled during the drop cannot be settled and RabbitMQ delivers it again (at least once). `main` no longer stops the service when the consumer stops, because it only ends with the context. Deferred from step 9 and done here: the publisher's reconnect check is the `reconnect` helper in `broker.go`.
  - Consumer channels: one for consuming (prefetch 1) and one in confirm mode for retries.
- Tests: worker tests for the outcome of every status in the matrix (408, 429, 500, 502, 503 retried; 400, 401, 403, 404, 410 end the job), `Retry-After` handling, the last attempt, the `failed` payload, no `failed` for a job that will be retried, no callbacks during shutdown, and storage errors on the upload path; unit tests for the classification and `Retry-After` parsing. Consumer integration tests: retry returns the job after the delay with attempt + 1 and its headers, an unpublishable retry dead-letters the job once, reconnect after a dropped connection, plus the earlier ack, dead-letter, shutdown and prefetch tests. Breaking the classification, the attempt limit, the `Retry-After` rule, the mandatory flag and the attempt increment each fails a test.
- Not covered: crash after upload and before the ack (the redelivered job uploads again, as documented in `APIs.md`), and reconnecting while the broker stays down (only the first reconnect after a drop is tested).

### 12. OpenTelemetry

- TS source: `src/instrumentation.ts`.
- Goal: traces and logs over OTLP HTTP honoring every `OTEL_*` variable in `config.md`; trace context propagated HTTP -> AMQP -> worker -> callbacks; `trace_id` in logs.
- Work: follow [Logging and OpenTelemetry](#logging-and-opentelemetry): tracer and logger providers, `otelhttp` middleware and transport, `otelslog` handler, AMQP header carrier for producer/consumer spans, mapping of `OTEL_ENABLED` (traces), `OTEL_LOG_SHIPPING` (logs, separate), and `OTEL_BATCH_*`.
- Learn: OTel Go SDK setup, providers and shutdown, `otelhttp`, `propagation.TextMapCarrier`, the slog bridge, why Go needs no preload trick like Node's `--import`.
- Implemented:
  - `internal/telemetry`: `Setup(ctx, cfg)` builds the tracer provider (OTLP HTTP, batch options from `OTEL_BATCH_*`, W3C trace context and baggage propagators) and, when `OTEL_LOG_SHIPPING=otlp`, the logger provider and the `otelslog` handler (`OTEL_LOGS_BATCH_*`). With `OTEL_ENABLED` off it does nothing. The endpoint, the sampler (`OTEL_TRACES_SAMPLER*`) and `OTEL_RESOURCE_ATTRIBUTES` are read by the SDK itself; a test checks the endpoint and `always_off`. `main` flushes both providers last on shutdown.
  - `internal/logging.New(cfg, service, out, also...)` adds `trace_id`, `span_id` and `trace_flags` to the stdout records when the context has a span, and fans each record out to the shipping handler. The shipped records get `service.name` from the resource instead of a `service` attribute.
  - HTTP server: `otelhttp.NewMiddleware` on the chi router, `/health` filtered out like in TS. chi v5.5 sets `r.Pattern`, so the span name formatter gives `POST /speak` and no extra middleware was needed (`otelhttp` does not set the `http.route` attribute for chi; the name carries the route). It sits before the request logging, so those lines carry the trace id. `/speak` answers with `x-trace-id` when there is a valid span (TS did the same for `/speak` only).
  - AMQP: `PublishJob` runs in a producer span and injects `traceparent` into the message headers; the consumer extracts it and runs the handler in a consumer span, so the worker, its logs and its callbacks join the request's trace. A retry copies the headers (`retryHeaders` keeps everything but `x-*`), so it stays in the same trace. `headerCarrier` is the `TextMapCarrier` over the AMQP table.
  - Callbacks and upload: `otelhttp.NewTransport` on the worker's HTTP client, so every call is a client span and carries the trace context. It records `url.full`, and presigned URLs carry their signature in the query. `otelhttp` sets that attribute after the span started, so a span processor cannot change it; `internal/telemetry` wraps the trace exporter instead and rewrites `url.full` just before export: no user info or fragment, and the values of secret query parameters hidden with `redact.Query`, the same rule as the logs (a denylist, so a secret under a name not on it would pass). Error text is not a leak: `otelhttp` sits below `http.Client`, which is what adds the URL to errors. Spans printed or inspected before the exporter (for example by a test recorder) still hold the full URL.
- Tests: `telemetry` (disabled, exports traces and logs to a fake OTLP endpoint with the resource and the trace link, stdout shipping, sampler variable), `logging` (trace fields in both formats, none without a span, fan-out), `httpapi` (span name and parent from `traceparent`, request logs carry the trace id, `/health` untraced, `x-trace-id` on and off), `worker` (trace context sent with every callback and upload, one client span each), `telemetry` (the exporter sends no query or credentials of a recorded URL), `amqp` (carrier, and an integration test that the consumer continues the publisher's trace and a retry keeps it). Removing the injection, the extraction, the log fields, the route name or the URL redaction each fails a test.
- `service.version` comes from `SERVICE_VERSION` (default `unknown`); the image sets it from the release version in step 13 (`ARG`/`ENV` in the Dockerfile, fed by `.releaserc.json`).
- Not done: nothing is exported for metrics (as in TS, `OTEL_METRICS_EXPORTER` was set to `none`; the Go SDK has no metric pipeline unless one is added). SDK errors (an unreachable collector) go to the Go default error handler on stderr, not to the service logger, to avoid a log-export loop. The `OTEL_LOGS_BATCH_*` variables are now documented in `config.md`.
- Done when: a trace in the local Grafana/Tempo shows the full job path and links to Loki logs.
- Checked by hand against the Compose collector, Tempo, Loki and RabbitMQ, with the server run on the host and fake `piper`/`ffmpeg` scripts: one `POST /synthesize` gave one trace in Tempo with the server span, the publish span, the consume span and seven client spans (five status callbacks, the upload-URL call, the upload), whose `url.full` has no query (the upload one is `http://127.0.0.1:9100/file.mp3`, the signature is gone), and Loki returned the log lines of that trace by `trace_id`. The Dockerfile is still the TypeScript one until step 13, so the container itself was not run.

### 13. Tests, image, CI, cutover

Split like step 10, because each part is reviewable on its own:

- 13A: Dockerfile, release config and workflow, docs.
- 13B: port the e2e and load tests (`tests/`, `load_test.py`).
- 13C: lefthook replaces husky, compare against the TS source, delete the TS files.

Done in 13A:

- Dockerfile: Go builder stage (`golang:1.26.8-bookworm`, `CGO_ENABLED=0`, static binary), runtime stage `debian:bookworm-slim` with the same Python venv, `piper-tts`, ffmpeg, curl and the eight voices as before. The non-root user is `app` (uid 1000, the uid the compose file already uses). `ARG SERVICE_VERSION` sets `ENV SERVICE_VERSION` in the last layer. `.dockerignore` no longer lists Node paths and keeps tests, `tools` and `local-setup` out of the context.
- Checked: the image builds (2.2 GB, almost all of it the voices and onnxruntime), runs as uid 1000, reports `SERVICE_VERSION`, and with RabbitMQ 4.4 beside it `POST /speak` returned a real MP3 (ID3 tag, MPEG layer III, 22.05 kHz) from real Piper and ffmpeg. The first request took 9 s (cold model).
- Release: the workflow drops pnpm and runs semantic-release with `npx` (it is a Node tool, so `setup-node` stays). `.releaserc.json` drops `@semantic-release/npm` and `@semantic-release/git`: there is no `package.json` to bump, and the version is the git tag. The exec plugin passes `--build-arg SERVICE_VERSION=${nextRelease.version}`. Not run here: it only runs on `main` with the Docker Hub token.
- Docs: README, CONTRIBUTING (make targets, testify instead of vitest), `config.md` (`NODE_ENV` is `APP_ENV`; internal service names must be in `SYNTHESIZE_CALLBACK_ALLOWED_HOSTS`; plain HTTP needs `SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS=false`), `.env.example`.
- Left for 13B/13C: `compose.yml` needs no change for the app (it builds the Dockerfile); `test.sh` and the e2e specs still describe the TS flow.

Done in 13B:

- `e2e/synthesize_integration_test.go` (tag `integration`, so `make integration_test` runs it) ports both TS specs: the happy path (four statuses, jobId on every callback, MP3 stored with a size above zero) and three concurrent jobs. It builds the Dockerfile and starts RabbitMQ 4.4, Garage (`dxflrs/garage:v2.4.1`, testcontainers module) and WireMock 3.13.1 on one network. Garage replaces MinIO: the service only needs a presigned PUT, and it uses no versioning, object lock, server-side encryption or replication. The presigned URL is made with the AWS SDK v2 against the in-network host name, since the signature covers the host. Both are test-only dependencies; `go list -deps ./cmd/server` shows neither in the binary.
- Dropped: the Tempo, Loki and collector containers. The TS test started them but asserted nothing on them; tracing was checked by hand in step 12.
- Not ported: `load_test.py` is a manual script against `POST /speak` and works unchanged; decided in 13C.
- Checked: the run passed (663 s, most of it the image build). The compose file has no object storage, so nothing changes there.

- (Done in 13B) Port `tests/` e2e (RabbitMQ and object storage via testcontainers) and the load test expectations.
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

### 13C result

- Compared the Go service with `APIs.md` and the TS source by running the image (real Piper and ffmpeg, RabbitMQ 4.4). `GET /health`, `POST /speak` headers (`audio/mpeg`, `Content-Disposition: inline`, `Cache-Control: no-store`, chunked, no `Content-Length`), 400 for empty text and bad JSON, 404 for unknown routes, `POST /synthesize` 202 and 400 matched.
- Found and fixed: `POST /speak` rejected `text/plain` (documented, and the TS accepted any non-JSON content type as raw text). The Go handler now does the same; tests cover accepted, blank and oversized plain text, and the plain-text case that asserted the old rejection is gone.
- Decided after the comparison: keep the Go error schema `{error, fields:[{field, rule}]}` and add the TS `code` (`invalid-request`, `invalid-callback-url`, `request-body-too-large`), so the body is `{error, code, fields}`. A body that is not exactly one JSON object (plain text, truncated, trailing data, two objects, a scalar, an array) is `400 Body must be a JSON object`; an empty body counts as `{}` and fails `required`; an unknown field is `400 Invalid JSON body`. The body is read in full before it is parsed, so an oversized body of any kind is a 413. Non-retryable 4xx failures go to the DLQ.
- Deleted: `src/`, `tests/`, `package.json`, `pnpm-lock.yaml`, `tsconfig*.json`, `eslint.config.mjs`, `eslint-rules/`, `jest.config.ts`, `.jscpd.json`, `.husky/`, and the Node entries of `.gitignore` and `.dockerignore`. Kept: `test.sh` and `load_test.py`, which only use curl and HTTP and work against the Go service.
- Needs the user: the global git config sets `core.hooksPath` to `.husky/_`, so `lefthook install` refuses until it is unset (`git config --global --unset core.hooksPath`). It was not changed here.
