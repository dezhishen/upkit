package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// consoleHandler 输出人类可读的一行日志（带级别着色）。
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
	group string
}

func newConsoleHandler(w io.Writer, level slog.Level) slog.Handler {
	return &consoleHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteString(" ")
	b.WriteString(colorize(r.Level, strings.ToUpper(levelTag(r.Level))))
	b.WriteString(" ")
	if h.group != "" {
		b.WriteString(h.group)
		b.WriteString(".")
	}
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	b.WriteString("\n")
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &next
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.group = name
	return &next
}

func levelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

func colorize(l slog.Level, s string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	switch {
	case l >= slog.LevelError:
		return "\x1b[31m" + s + "\x1b[0m"
	case l >= slog.LevelWarn:
		return "\x1b[33m" + s + "\x1b[0m"
	default:
		return "\x1b[36m" + s + "\x1b[0m"
	}
}

// Elapsed 便于日志里统一打印耗时。
func Elapsed(start time.Time) time.Duration { return time.Since(start).Truncate(time.Millisecond) }
