package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/fx"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// Guards against clients that open a connection and never finish sending headers.
// There is deliberately no read or write timeout: /speak streams for as long as synthesis takes.
const readHeaderTimeout = 10 * time.Second

const (
	keyPort       = "port"
	keyPath       = "path"
	keySampleRate = "sampleRate"
	keyError      = "error"
)

func NewHTTPRouter(
	cfg config.Config,
	logger *slog.Logger,
	synth *piper.Synthesizer,
	broker *amqp.Broker,
) http.Handler {
	return httpapi.NewRouter(httpapi.Options{
		Logger:           logger,
		Synthesizer:      synth,
		Jobs:             broker,
		MaxConcurrency:   cfg.MaxConcurrency,
		MaxBodySizeBytes: cfg.MaxBodySizeBytes,
		Version:          cfg.Service.Version,
		DocsEnabled:      cfg.Service.DocsEnabled,
		Synthesize: httpapi.SynthesizeOptions{
			MaxBodySizeBytes: cfg.Synthesize.MaxBodySizeBytes,
			Callback:         cfg.Synthesize.Callback,
		},
	})
}

// NewHTTPServer listens on start, so an unusable port aborts startup, and drains in-flight requests on stop.
func NewHTTPServer(
	lc fx.Lifecycle,
	sd fx.Shutdowner,
	cfg config.Config,
	logger *slog.Logger,
	synth *piper.Synthesizer,
	handler http.Handler,
) *http.Server {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	boot := logging.Named(logger, "Bootstrap")

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var lcfg net.ListenConfig
			ln, err := lcfg.Listen(ctx, "tcp", ":"+strconv.Itoa(cfg.Service.Port))
			if err != nil {
				return fmt.Errorf("listen: %w", err)
			}

			boot.InfoContext(ctx, "TTS API running", slog.Int(keyPort, cfg.Service.Port))
			boot.InfoContext(
				ctx,
				"Piper model",
				slog.String(keyPath, cfg.PiperModelPath),
				slog.Int(keySampleRate, synth.SampleRate()),
			)

			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					boot.ErrorContext(
						context.WithoutCancel(ctx),
						"Server error",
						slog.String(keyError, err.Error()),
					)

					if shutdownErr := sd.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
						boot.ErrorContext(
							context.WithoutCancel(ctx),
							"Shutdown failed",
							slog.String(keyError, shutdownErr.Error()),
						)
					}
				}
			}()

			return nil
		},
		OnStop: srv.Shutdown,
	})

	return srv
}
