package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter returns the service's HTTP handler.
// Unknown paths and wrong methods return 404.
func NewRouter(logger *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(requestLogging(logger))

	r.Get("/health", handleHealth)

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
