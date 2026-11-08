package logging_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
)

const (
	service   = "piper-tts-rest-api"
	keyMethod = "method"
	keyURL    = "url"
	keyJobID  = "jobId"
)

var fixedTime = time.Date(2026, time.March, 8, 0, 17, 21, 123_000_000, time.UTC)

func newLogger(mode, level string) (*slog.Logger, *bytes.Buffer) {
	out := &bytes.Buffer{}

	return logging.New(config.Log{Mode: mode, Level: level}, service, out), out
}

func emit(t *testing.T, l *slog.Logger, level slog.Level, msg string, attrs ...slog.Attr) {
	t.Helper()

	r := slog.NewRecord(fixedTime, level, msg, 0)
	r.AddAttrs(attrs...)
	require.NoError(t, l.Handler().Handle(t.Context(), r))
}

func TestNew_JSON(t *testing.T) {
	l, out := newLogger(config.LogModeJSON, "info")

	emit(t, logging.Named(l, "HttpMiddleware"), slog.LevelInfo, "Incoming POST /speak",
		slog.String(keyMethod, "POST"), slog.String(keyURL, "/speak"))

	assert.JSONEq(t, `{
		"timestamp": "2026-03-08T00:17:21.123Z",
		"level": "info",
		"service": "piper-tts-rest-api",
		"context": "HttpMiddleware",
		"message": "Incoming POST /speak",
		"method": "POST",
		"url": "/speak"
	}`, out.String())
}

func TestNew_JSONLevelNames(t *testing.T) {
	tests := []struct {
		level slog.Level
		name  string
	}{
		{slog.LevelError, "error"},
		{slog.LevelWarn, "warn"},
		{slog.LevelInfo, "info"},
		{logging.LevelVerbose, "verbose"},
		{slog.LevelDebug, "debug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, out := newLogger(config.LogModeJSON, "debug")

			emit(t, l, tt.level, "msg")

			assert.Contains(t, out.String(), `"level":"`+tt.name+`"`)
		})
	}
}

func TestNew_PlainText(t *testing.T) {
	l, out := newLogger(config.LogModePlainText, "info")

	emit(t, logging.Named(l, "HttpMiddleware"), slog.LevelInfo, "Incoming POST /speak",
		slog.String(keyMethod, "POST"), slog.String(keyURL, "/speak"))

	assert.Equal(t,
		`[2026-03-08T00:17:21.123Z] info    HttpMiddleware       [piper-tts-rest-api] Incoming POST /speak | {"method":"POST","url":"/speak"}`+"\n",
		out.String())
}

func TestNew_PlainTextWithoutExtrasOrContext(t *testing.T) {
	l, out := newLogger(config.LogModePlainText, "info")

	emit(t, l, slog.LevelInfo, "Server started")

	assert.Equal(t,
		`[2026-03-08T00:17:21.123Z] info                         [piper-tts-rest-api] Server started`+"\n",
		out.String())
}

func TestNew_PlainTextKeepsAttributesFromWith(t *testing.T) {
	l, out := newLogger(config.LogModePlainText, "info")

	emit(t, logging.Named(l.With(slog.String(keyJobID, "j1")), "Worker"), slog.LevelInfo, "Picked up")

	assert.Equal(t,
		`[2026-03-08T00:17:21.123Z] info    Worker               [piper-tts-rest-api] Picked up | {"jobId":"j1"}`+"\n",
		out.String())
}

func TestNew_PlainTextLevelNames(t *testing.T) {
	tests := []struct {
		level slog.Level
		name  string
	}{
		{slog.LevelError, "error  "},
		{slog.LevelWarn, "warn   "},
		{logging.LevelVerbose, "verbose"},
		{slog.LevelDebug, "debug  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, out := newLogger(config.LogModePlainText, "debug")

			emit(t, l, tt.level, "msg")

			assert.Contains(t, out.String(), "Z] "+tt.name+" ")
		})
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	levels := []slog.Level{slog.LevelError, slog.LevelWarn, slog.LevelInfo, logging.LevelVerbose, slog.LevelDebug}
	tests := []struct {
		configured string
		enabled    []bool
	}{
		{config.LogLevelError, []bool{true, false, false, false, false}},
		{config.LogLevelWarn, []bool{true, true, false, false, false}},
		{config.LogLevelInfo, []bool{true, true, true, false, false}},
		{config.LogLevelVerbose, []bool{true, true, true, true, false}},
		{config.LogLevelDebug, []bool{true, true, true, true, true}},
	}

	for _, tt := range tests {
		t.Run(tt.configured, func(t *testing.T) {
			for _, mode := range []string{config.LogModeJSON, config.LogModePlainText} {
				l, _ := newLogger(mode, tt.configured)

				got := make([]bool, 0, len(levels))
				for _, level := range levels {
					got = append(got, l.Enabled(t.Context(), level))
				}

				assert.Equal(t, tt.enabled, got, mode)
			}
		})
	}
}
