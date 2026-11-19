package main

import (
	"context"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/telemetry"
)

// TelemetryProvider starts OpenTelemetry and flushes it on shutdown.
// The logger depends on it, so fx stops everything that logs first and their shutdown logs are still flushed.
func TelemetryProvider(
	lc fx.Lifecycle,
	cfg config.Config,
) (*telemetry.Telemetry, error) {
	t, err := telemetry.Setup(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{OnStop: t.Shutdown})

	return t, nil
}

// LoggerProvider builds the logger, which ships its records when OTEL_LOG_SHIPPING asks for it.
func LoggerProvider(cfg config.Config, t *telemetry.Telemetry) *slog.Logger {
	return logging.New(cfg.Log, cfg.Service.Name, os.Stdout, t.LogHandler())
}
