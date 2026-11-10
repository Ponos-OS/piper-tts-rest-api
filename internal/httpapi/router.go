package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

const (
	apiTitle    = "Piper TTS REST API"
	docsPath    = "/docs"
	typeString  = "string"
	openAPIPath = "/openapi"
)

// Options holds what the router needs: its collaborators and the limits applied to the endpoints.
type Options struct {
	Logger           *slog.Logger
	Synthesizer      *piper.Synthesizer
	Jobs             JobPublisher
	MaxConcurrency   int
	MaxBodySizeBytes int
	Synthesize       SynthesizeOptions
	// Version is reported in the OpenAPI document.
	Version string
	// DocsEnabled serves the Swagger UI at /docs and the OpenAPI document at /openapi.json and /openapi.yaml.
	DocsEnabled bool
}

// SynthesizeOptions holds the limits applied to /synthesize.
type SynthesizeOptions struct {
	MaxBodySizeBytes int
	Callback         config.Callback
}

// NewRouter returns the service's HTTP handler.
// Unknown paths and wrong methods return 404.
func NewRouter(opts Options) http.Handler {
	r := chi.NewRouter()
	// Tracing wraps the logging, so the request log lines carry the trace id.
	r.Use(
		otelhttp.NewMiddleware("http",
			otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != healthPath }),
			otelhttp.WithSpanNameFormatter(spanName),
		),
		requestLogging(opts.Logger),
	)

	registerOperations(newAPI(r, opts), opts)

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusNotFound, "Not Found")
	}
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)

	return r
}

// OpenAPI returns the OpenAPI document of the service as indented JSON.
func OpenAPI(version string) ([]byte, error) {
	opts := Options{Logger: slog.New(slog.DiscardHandler), Version: version}
	api := newAPI(chi.NewRouter(), opts)
	registerOperations(api, opts)

	doc, err := json.MarshalIndent(api.OpenAPI(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAPI document: %w", err)
	}

	return append(doc, '\n'), nil
}

func newAPI(r chi.Router, opts Options) huma.API {
	cfg := huma.DefaultConfig(apiTitle, opts.Version)
	cfg.Info.Description = "Text to speech with Piper. /v1/speak streams an MP3; /v1/synthesize queues a job that uploads it."
	cfg.CreateHooks = nil // no "$schema" field in responses.
	cfg.DocsPath, cfg.OpenAPIPath = "", ""
	if opts.DocsEnabled {
		cfg.DocsPath, cfg.OpenAPIPath = docsPath, openAPIPath
		cfg.DocsRenderer = huma.DocsRendererSwaggerUI
	}

	return humachi.New(r, cfg)
}

func registerOperations(api huma.API, opts Options) {
	registerHealth(api)
	registerSpeak(api, &speaker{
		synth:   opts.Synthesizer,
		slots:   newSemaphore(opts.MaxConcurrency),
		maxBody: opts.MaxBodySizeBytes,
		log:     logging.Named(opts.Logger, "Speak"),
	})
	registerSynthesize(api, &synthesizer{
		jobs:     opts.Jobs,
		maxBody:  opts.Synthesize.MaxBodySizeBytes,
		callback: opts.Synthesize.Callback,
		log:      logging.Named(opts.Logger, "Synthesize"),
	})
}

type healthOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

func registerHealth(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        healthPath,
		Summary:     "Health check",
		Tags:        []string{"Health"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "The service is up.",
				Content:     map[string]*huma.MediaType{"text/plain": {Schema: &huma.Schema{Type: typeString, Examples: []any{"OK"}}}},
			},
		},
	}, func(_ context.Context, _ *struct{}) (*healthOutput, error) {
		return &healthOutput{ContentType: "text/plain; charset=utf-8", Body: []byte("OK")}, nil
	})
}

// spanName is "POST /speak": the method and the matched route, so spans are not named per URL.
// chi sets r.Pattern once the route is matched, and otelhttp renames the span with this function afterwards.
func spanName(_ string, r *http.Request) string {
	if r.Pattern == "" {
		return r.Method
	}

	return r.Method + " " + r.Pattern
}
