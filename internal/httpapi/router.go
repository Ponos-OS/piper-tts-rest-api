package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// Options holds what the router needs: its collaborators and the limits applied to the endpoints.
type Options struct {
	Logger           *slog.Logger
	Synthesizer      *piper.Synthesizer
	Jobs             JobPublisher
	MaxConcurrency   int
	MaxBodySizeBytes int
	Synthesize       SynthesizeOptions
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
	r.Use(requestLogging(opts.Logger))

	r.Get("/health", handleHealth)

	speak := &speaker{
		synth:   opts.Synthesizer,
		slots:   newSemaphore(opts.MaxConcurrency),
		maxBody: opts.MaxBodySizeBytes,
		log:     logging.Named(opts.Logger, "Speak"),
	}
	r.Post("/speak", speak.handleSpeak)

	synthesize := &synthesizer{
		jobs:     opts.Jobs,
		maxBody:  opts.Synthesize.MaxBodySizeBytes,
		callback: opts.Synthesize.Callback,
		log:      logging.Named(opts.Logger, "Synthesize"),
	}
	r.Post("/synthesize", synthesize.handleSynthesize)

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusNotFound, "Not Found")
	}
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)

	return r
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "OK")
}
