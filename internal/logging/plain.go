package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

const (
	levelWidth   = 7 // len("verbose")
	contextWidth = 20
)

// plainHandler prints
//
//	[timestamp] <level padded to 7> <context padded to 20> [service] message | {"extra":"attrs"}
//
// The trailing JSON object is produced by an inner slog JSON handler, so
// groups and value encoding behave like the JSON mode.
type plainHandler struct {
	out     io.Writer
	mu      *sync.Mutex
	level   slog.Leveler
	service string
	context string
	inner   slog.Handler
	buf     *bytes.Buffer
}

func newPlainHandler(out io.Writer, level slog.Leveler, service string) *plainHandler {
	buf := &bytes.Buffer{}

	return &plainHandler{
		out:     out,
		mu:      &sync.Mutex{},
		level:   level,
		service: service,
		inner:   slog.NewJSONHandler(buf, &slog.HandlerOptions{ReplaceAttr: dropBuiltins}),
		buf:     buf,
	}
}

func (h *plainHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *plainHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	name := h.context
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == keyContext {
			name = a.Value.String()
		} else {
			rec.AddAttrs(a)
		}

		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf.Reset()
	if err := h.inner.Handle(ctx, rec); err != nil {
		return fmt.Errorf("encode extra attributes: %w", err)
	}

	line := fmt.Sprintf("[%s] %-*s %-*s [%s] %s",
		r.Time.UTC().Format(timestampLayout), levelWidth, levelName(r.Level), contextWidth, name, h.service, r.Message)
	if extras := bytes.TrimSpace(h.buf.Bytes()); string(extras) != "{}" {
		line += " | " + string(extras)
	}

	if _, err := io.WriteString(h.out, line+"\n"); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}

	return nil
}

func (h *plainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	rest := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Key == keyContext {
			clone.context = a.Value.String()
		} else {
			rest = append(rest, a)
		}
	}
	clone.inner = h.inner.WithAttrs(rest)

	return &clone
}

func (h *plainHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.inner = h.inner.WithGroup(name)

	return &clone
}
