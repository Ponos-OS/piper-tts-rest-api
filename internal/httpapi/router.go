package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// Options holds the limits applied to /speak.
type Options struct {
	MaxConcurrency   int
	MaxBodySizeBytes int
}

// NewRouter returns the service's HTTP handler.
// Unknown paths and wrong methods return 404.
func NewRouter(logger *slog.Logger, synth *piper.Synthesizer, opts Options) http.Handler {
	r := chi.NewRouter()
	r.Use(requestLogging(logger))

	r.Get("/health", handleHealth)

	speak := &speaker{
		synth:   synth,
		slots:   newSemaphore(opts.MaxConcurrency),
		maxBody: opts.MaxBodySizeBytes,
		log:     logging.Named(logger, "Speak"),
	}
	r.Post("/speak", speak.handleSpeak)

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
