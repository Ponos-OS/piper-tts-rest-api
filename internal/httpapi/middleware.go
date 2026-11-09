package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
)

const (
	keyMethod     = "method"
	keyURL        = "url"
	keyHeaders    = "headers"
	keyStatusCode = "statusCode"
	keyDuration   = "duration"
	keyAborted    = "aborted"
)

// healthPath is not logged: it is polled by health checks.
const healthPath = "/health"

// requestLogging logs every request when it arrives and again when it completes.
func requestLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	log := logging.Named(logger, "HttpMiddleware")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == healthPath {
				next.ServeHTTP(w, r)

				return
			}

			start := time.Now()
			url := redactedRequestURI(r.URL)
			ctx := r.Context()

			//nolint:sloglint // "Incoming POST /speak" is the documented message format (config.md).
			log.InfoContext(ctx, "Incoming "+r.Method+" "+url,
				slog.String(keyMethod, r.Method),
				slog.String(keyURL, url),
				slog.Any(keyHeaders, redactHeaders(r.Header)),
			)

			// The wrapper records the status and still supports http.Flusher,
			// which the streaming endpoints need.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				// aborted: the handler cut the connection with panic(http.ErrAbortHandler),
				// or the client left (net/http cancels the request context).
				rec := recover()
				aborted := rec != nil || ctx.Err() != nil

				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}

				//nolint:sloglint // "Completed POST /speak" is the documented message format (config.md).
				log.InfoContext(ctx, "Completed "+r.Method+" "+url,
					slog.String(keyMethod, r.Method),
					slog.String(keyURL, url),
					slog.Int(keyStatusCode, status),
					slog.Int64(keyDuration, time.Since(start).Milliseconds()),
					slog.Bool(keyAborted, aborted),
				)

				if rec != nil {
					panic(rec) // net/http must still see it to cut the connection.
				}
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
