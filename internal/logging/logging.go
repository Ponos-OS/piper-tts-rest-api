// Package logging builds the service logger on top of log/slog.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// Attribute keys the formatters give special treatment.
const (
	keyService = "service"
	keyContext = "context"

	keyTraceID    = "trace_id"
	keySpanID     = "span_id"
	keyTraceFlags = "trace_flags"
)

// LevelVerbose sits between info and debug
const LevelVerbose slog.Level = -2

// Field names of the JSON output, as documented in config.md.
const (
	keyTimestamp = "timestamp"
	keyMessage   = "message"
)

const timestampLayout = "2006-01-02T15:04:05.000Z"

// New creates a logger that writes to out (e.g. os.Stdout).
// cfg selects the format (JSON or PLAIN_TEXT) and the minimum level.
// Service is added to every record written to out.
// Records also go to each non-nil handler in also (the OTLP log shipper); those get the service name from their own resource.
// The records written to out carry trace_id, span_id and trace_flags when the context has a span.
func New(cfg config.Log, service string, out io.Writer, also ...slog.Handler) *slog.Logger {
	level := parseLevel(cfg.Level)

	var stdout slog.Handler
	if cfg.Mode == config.LogModeJSON {
		stdout = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level, ReplaceAttr: renameBuiltins}).
			WithAttrs([]slog.Attr{slog.String(keyService, service)})
	} else {
		stdout = newPlainHandler(out, level, service)
	}

	handlers := []slog.Handler{traceHandler{stdout}}
	for _, h := range also {
		if h != nil {
			handlers = append(handlers, h)
		}
	}
	if len(handlers) == 1 {
		return slog.New(handlers[0])
	}

	return slog.New(slog.NewMultiHandler(handlers...))
}

// traceHandler adds the trace context of the record's context.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(keyTraceID, sc.TraceID().String()),
			slog.String(keySpanID, sc.SpanID().String()),
			slog.String(keyTraceFlags, sc.TraceFlags().String()),
		)
	}

	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // it is the wrapped handler's own result.
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Named returns a logger that tags every record with a context name.
func Named(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String(keyContext, name))
}

func parseLevel(name string) slog.Level {
	switch name {
	case config.LogLevelError:
		return slog.LevelError
	case config.LogLevelWarn:
		return slog.LevelWarn
	case config.LogLevelVerbose:
		return LevelVerbose
	case config.LogLevelDebug:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

func levelName(level slog.Level) string {
	if level == LevelVerbose {
		return config.LogLevelVerbose
	}

	return strings.ToLower(level.String())
}

// Gives the JSON output the field names documented in config.md
func renameBuiltins(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}

	switch a.Key {
	case slog.TimeKey:
		return slog.String(keyTimestamp, a.Value.Time().UTC().Format(timestampLayout))
	case slog.LevelKey:
		level, ok := a.Value.Any().(slog.Level)
		if !ok {
			return a
		}

		return slog.String(slog.LevelKey, levelName(level))
	case slog.MessageKey:
		return slog.String(keyMessage, a.Value.String())
	default:
		return a
	}
}

// Removes the fields the plain-text line prints itself.
func dropBuiltins(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}

	switch a.Key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey:
		return slog.Attr{}
	default:
		return a
	}
}
