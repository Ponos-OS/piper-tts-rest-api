# Migration to Go

Port the TypeScript service (`src/`, ~5.4k lines including specs) to Go with the same external behavior: same endpoints, same env variables, same RabbitMQ topology, same callback contract, same Docker image tag line.

> [!IMPORTANT]
>
> Do all the work for a step (code, tests, lint, docs), then stop before committing. Wait for the owner's review and explicit green light before running `git commit`. Do not start step N+1 until step N is committed.

Each step is small enough to review and study on its own, and ends with something runnable.

## Ground rules

- Behavior parity is the goal. `.github/docs/APIs.md` and `.github/docs/config.md` are the contract; if the Go code and these docs disagree, the Go code is wrong.
- No new features, no new env variables, no new endpoints.
- Prefer the standard library. Every third-party dependency needs your approval before it is added (candidates are listed per step).
- The TS code is a read-only reference for behavior. It may stop building or passing at any point; nothing is spent keeping it working. It is deleted in step 13.
- Tests use testify and follow AAA, no conditionals in tests (same rules as today, by convention and `testifylint`). Integration tests use testcontainers.
- Lint is strict from step 1 (see [Lint configuration](#lint-configuration)); a step is not done with lint warnings.
- Use the latest stable Go release and idiomatic approaches as of today.

## Decisions

Decided:

- Layout: `go.mod` at the repo root, `cmd/server` and `internal/{config,logging,httpapi,piper,synthesis,amqp,worker}`. No `src/`, no `pkg/`.
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
- Open question: `verbose` has no slog level; decide the mapping.
- Done when: both output formats match the examples in `config.md`.

### 4. HTTP server, `GET /health`, request logging, graceful shutdown

- TS source: `src/server.ts`, `src/routes/health.ts`, `src/middlewares/logging.ts`.
- Goal: server on `PORT`, `/health` returns `200 text/plain`, every request and response is logged, SIGTERM drains in-flight requests.
- Learn: `net/http`, chi routing and `r.Use`, `http.Handler` and middleware as function wrapping, `httptest`, `signal.NotifyContext`, `Server.Shutdown`.
- Done when: `curl /health` works and a test covers the middleware.

### 5. Small utilities

- TS source: `semaphore.ts`, `word-counter.util.ts`, `fetch-with-timeout.util.ts`, `body.ts`, `http.ts`.
- Goal: concurrency limiter, word counter, HTTP client call with timeout, body reading with a max size (413 on overflow).
- Learn: goroutines vs event loop, buffered channels as semaphores (or `golang.org/x/sync/semaphore`), `context.WithTimeout`, `http.MaxBytesReader`, `io.Reader`.
- Done when: the behavior in each TS spec is reproduced by a Go test.

### 6. Piper and ffmpeg pipeline

- TS source: `src/services/piper.ts`, `piper.spec.ts`.
- Goal: read sample rate from `<model>.onnx.json` at startup; run `piper --output-raw` piped into `ffmpeg` producing MP3; expose it as an `io.Reader` of MP3 bytes; kill both processes and wait for exit when the context is cancelled.
- Learn: `os/exec` (`CommandContext`, `StdoutPipe`, `Wait`), process groups and signals on Linux, `io.Pipe`, `io.Copy`, goroutine leaks, `errgroup`.
- Risk: this is the hardest part to get right (cancellation and cleanup). Review it closely.
- Done when: a test with fake `piper`/`ffmpeg` scripts proves cancellation kills both and frees the slot; one real run produces a playable MP3.

### 7. `POST /speak`

- TS source: `src/routes/speak.ts`.
- Goal: JSON and `text/plain` bodies, 400/413/500 mapping, streamed `audio/mpeg` with the documented headers, semaphore held for the whole request, client disconnect cancels the pipeline.
- Learn: `http.Flusher`, `r.Context()` cancellation on disconnect, chunked responses, error handling after headers are sent.
- Done when: the curl examples in `APIs.md` work and the cancellation scenario is tested.

### 8. Synthesize domain types and request validation

- TS source: `src/shared/synthesize.ts`, `job-events.ts` and their specs.
- Goal: request struct, validation (text, `genUploadUrl`, `statusCallbackUrl`, HTTPS rule, allowed hosts, no URL credentials or fragments), job event types with the exact status names and fields.
- Learn: JSON struct tags, `encoding/json`, `net/url`, custom `error` types and `errors.Is`/`errors.As`, enums via typed constants.
- Done when: field names and status values match the TS code and the docs verbatim.

### 9. AMQP topology and publisher

- TS source: `src/services/amqp.ts`, `amqp.spec.ts`, `src/routes/synthesize.ts`.
- Goal: declare the durable exchanges and quorum queues (work, retry, DLQ, DLX) with the same arguments, publisher confirms, `POST /synthesize` returning 202 with `jobId` or 503 on publish failure.
- Learn: AMQP concepts (exchange, queue, binding, ack, confirm), `amqp091-go` channels, reconnect handling, why channels are not goroutine-safe.
- Done when: an integration test against a real RabbitMQ (testcontainers, `integration` build tag) shows the topology matches what the TS service declares.

### 10. Worker: consume, generate, upload

- TS source: `src/services/synthesize-worker.ts` (largest file), `synthesis.ts`.
- Goal: consume with `prefetch=1`, call `genUploadUrl`, run the pipeline, stream the MP3 to the presigned URL with idle and absolute timeouts, send status callbacks with `Idempotency-Key: <jobId>:<attempt>`, ack after terminal outcome.
- Learn: consumer loops, `context` propagation, `io.Pipe` for streaming upload, `http.NewRequestWithContext`, progress estimation from `SYNTHESIZE_WORDS_PER_MINUTE`.
- Note: split this into two reviews (generate+upload first, callbacks second) because of its size.
- Done when: the happy path e2e test passes.

### 11. Worker: retries, DLQ, failure semantics

- TS source: same worker file, state machine in `.github/docs/APIs.md`.
- Goal: retryable vs terminal failures, delayed retry via retry queue with `x-attempt` preserved, DLQ on exhaustion, `failed` callback with `failedAt` and `error`, at-least-once behavior on crash.
- Learn: error classification with wrapped errors, idempotency, redelivery.
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
