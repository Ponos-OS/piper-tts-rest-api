// Package logging builds the service logger on top of log/slog.
package logging

import (
	"io"
	"log/slog"
	"strings"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// Attribute keys the formatters give special treatment.
const (
	keyService = "service"
	keyContext = "context"
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
// Service is added to every record.
func New(cfg config.Log, service string, out io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)

	if cfg.Mode == config.LogModeJSON {
		handler := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level, ReplaceAttr: renameBuiltins})

		return slog.New(handler).With(slog.String(keyService, service))
	}

	// Ensures newPlainHandler/plainHandler implements slog.Handler at compile time
	return slog.New(newPlainHandler(out, level, service))
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
