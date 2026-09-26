// Package logging 提供结构化日志（JSONL）、内存环形缓冲与审计日志。
//
// 只依赖标准库的 log/slog；文件按大小/数量/天数轮转，敏感字段自动脱敏。
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dezhishen/upkit/internal/core"
)

// Options 是日志管理器配置。
type Options struct {
	Level      string
	Dir        string
	Audit      bool
	MaxSizeMB  int
	MaxFiles   int
	MaxAgeDays int
	MaxTotalMB int
	Compress   bool
	Redact     bool
	RunID      string
	Console    bool // 是否同时输出到终端（TUI 模式建议关闭）
}

// Record 是环形缓冲里的一条日志。
type Record struct {
	At    time.Time
	Level string
	App   string
	Phase string
	Msg   string
	KV    map[string]any
}

// Manager 统一管理日志与审计。
type Manager struct {
	opts Options
	file *rotatingFile
	ring *Ring
	core.Logger

	mu        sync.Mutex
	auditFile *os.File
	auditPath string
}

// New 创建日志管理器；目录不可写时退化为「只有内存与终端」。
func New(opts Options) (*Manager, error) {
	if opts.Level == "" {
		opts.Level = "info"
	}
	m := &Manager{opts: opts, ring: NewRing(5000)}
	if opts.RunID == "" {
		opts.RunID = newRunID()
		m.opts.RunID = opts.RunID
	}

	var handlers []slog.Handler
	if strings.TrimSpace(opts.Dir) != "" {
		if err := os.MkdirAll(opts.Dir, 0o755); err == nil {
			rf, err := newRotatingFile(opts)
			if err == nil {
				m.file = rf
				handlers = append(handlers, slog.NewJSONHandler(rf, &slog.HandlerOptions{Level: levelOf(opts.Level)}))
			}
		}
	}
	if opts.Console {
		handlers = append(handlers, newConsoleHandler(os.Stderr, levelOf(opts.Level)))
	}

	var base slog.Handler
	switch len(handlers) {
	case 0:
		base = slog.NewJSONHandler(io_Discard{}, &slog.HandlerOptions{Level: levelOf(opts.Level)})
	case 1:
		base = handlers[0]
	default:
		base = fanout{handlers}
	}
	if opts.Redact {
		base = &redactHandler{inner: base}
	}

	base = &ringHandler{inner: base, ring: m.ring, runID: m.opts.RunID}
	m.Logger = &adapter{log: slog.New(base), runID: m.opts.RunID}

	if opts.Audit && strings.TrimSpace(opts.Dir) != "" {
		m.auditPath = filepath.Join(opts.Dir, "audit.jsonl")
		f, err := os.OpenFile(m.auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			m.auditFile = f
		}
	}
	return m, nil
}

// RunID 返回本次运行的 id。
func (m *Manager) RunID() string { return m.opts.RunID }

// Ring 返回内存环形缓冲。
func (m *Manager) Ring() *Ring { return m.ring }

// AuditPath 返回审计日志路径（未启用时为空）。
func (m *Manager) AuditPath() string { return m.auditPath }

// Audit 追加一条审计记录（只记「变更类」操作）。
func (m *Manager) Audit(entry map[string]any) {
	if m.auditFile == nil {
		return
	}
	if entry == nil {
		entry = map[string]any{}
	}
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["run"] = m.opts.RunID
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, _ = m.auditFile.Write(append(data, '\n'))
	_ = m.auditFile.Sync()
}

// Close 关闭文件句柄。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.auditFile != nil {
		_ = m.auditFile.Close()
		m.auditFile = nil
	}
	if m.file != nil {
		return m.file.Close()
	}
	return nil
}

// ── core.Logger 适配 ──────────────────────────────────────────

type adapter struct {
	log   *slog.Logger
	runID string
}

func (a *adapter) with(kv []any) *slog.Logger {
	return a.log.With(attrs(kv)...)
}

func (a *adapter) Debug(msg string, kv ...any) { a.with(kv).Debug(msg) }
func (a *adapter) Info(msg string, kv ...any)  { a.with(kv).Info(msg) }
func (a *adapter) Warn(msg string, kv ...any)  { a.with(kv).Warn(msg) }
func (a *adapter) Error(msg string, kv ...any) { a.with(kv).Error(msg) }

func (a *adapter) With(kv ...any) core.Logger {
	return &adapter{log: a.with(kv), runID: a.runID}
}

// attrs 把 kv 展开成 slog.Attr；奇数个时补 "!BADKEY"。
func attrs(kv []any) []any {
	out := make([]any, 0, len(kv))
	for i, v := range kv {
		if i%2 == 1 {
			continue
		}
		key, ok := v.(string)
		if !ok {
			key = fmt.Sprintf("key%d", i)
		}
		if i+1 < len(kv) {
			out = append(out, slog.Any(key, kv[i+1]))
		} else {
			out = append(out, slog.String(key, "<missing>"))
		}
	}
	return out
}

func levelOf(level string) slog.Level {
	switch strings.ToLower(level) {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// fanout 把日志同时写到多个 handler。
type fanout struct{ hs []slog.Handler }

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.hs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.hs {
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f fanout) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Handler, 0, len(f.hs))
	for _, h := range f.hs {
		out = append(out, h.WithAttrs(as))
	}
	return fanout{out}
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, 0, len(f.hs))
	for _, h := range f.hs {
		out = append(out, h.WithGroup(name))
	}
	return fanout{out}
}

// redactHandler 对敏感键做脱敏。
type redactHandler struct{ inner slog.Handler }

var sensitiveKeys = []string{"token", "password", "authorization", "secret", "apikey", "api_key", "cookie"}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, redactText(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		red = append(red, redactAttr(a))
	}
	return &redactHandler{inner: h.inner.WithAttrs(red)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	for _, s := range sensitiveKeys {
		if strings.Contains(key, s) {
			return slog.String(a.Key, "***")
		}
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, redactText(a.Value.String()))
	}
	return a
}

// redactText 抹掉 URL 里可能的 token 参数。
func redactText(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	for _, marker := range []string{"token=", "access_token=", "password="} {
		for {
			i := strings.Index(lower, marker)
			if i < 0 {
				break
			}
			start := i + len(marker)
			end := start
			for end < len(s) && s[end] != '&' && s[end] != ' ' && s[end] != '"' {
				end++
			}
			s = s[:start] + "***" + s[end:]
			lower = strings.ToLower(s)
		}
	}
	return s
}

// io_Discard 避免 import io 只为 Discard。
type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

// ringHandler 把日志同时写入内存环形缓冲，供 TUI 的 Logs 面板展示。
type ringHandler struct {
	inner slog.Handler
	ring  *Ring
	runID string
}

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := Record{
		At:    r.Time,
		Level: r.Level.String(),
		Msg:   r.Message,
		KV:    map[string]any{},
	}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "app":
			rec.App = a.Value.String()
		case "phase":
			rec.Phase = a.Value.String()
		}
		rec.KV[a.Key] = a.Value.Any()
		return true
	})
	h.ring.Add(rec)
	return h.inner.Handle(ctx, r)
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &ringHandler{inner: h.inner.WithAttrs(as), ring: h.ring, runID: h.runID}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{inner: h.inner.WithGroup(name), ring: h.ring, runID: h.runID}
}

// Ring 是固定容量的环形缓冲。
type Ring struct {
	mu   sync.RWMutex
	data []Record
	cap  int
	next int
	full bool
}

// NewRing 创建环形缓冲。
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1000
	}
	return &Ring{data: make([]Record, capacity), cap: capacity}
}

// Add 追加一条记录。
func (r *Ring) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.next] = rec
	r.next = (r.next + 1) % r.cap
	if r.next == 0 {
		r.full = true
	}
}

// Snapshot 按时间顺序返回全部记录。
func (r *Ring) Snapshot() []Record {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		out := make([]Record, r.next)
		copy(out, r.data[:r.next])
		return out
	}
	out := make([]Record, 0, r.cap)
	out = append(out, r.data[r.next:]...)
	out = append(out, r.data[:r.next]...)
	return out
}

// Len 返回当前记录数。
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.full {
		return r.cap
	}
	return r.next
}

// Filter 按条件过滤（app 为空表示不限；minLevel 为空表示不限）。
func (r *Ring) Filter(app, minLevel, keyword string) []Record {
	min := levelValue(minLevel)
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	out := make([]Record, 0, 64)
	for _, rec := range r.Snapshot() {
		if app != "" && rec.App != app {
			continue
		}
		if levelValue(rec.Level) < min {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(rec.Msg), keyword) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func levelValue(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn", "warning":
		return 3
	case "error":
		return 4
	default:
		return 0
	}
}

// Export 把记录导出成 JSONL 文本。
func Export(records []Record) []byte {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, rec := range records {
		_ = enc.Encode(map[string]any{
			"ts":    rec.At.Format(time.RFC3339Nano),
			"level": strings.ToLower(rec.Level),
			"app":   rec.App,
			"phase": rec.Phase,
			"msg":   rec.Msg,
			"kv":    rec.KV,
		})
	}
	return []byte(b.String())
}

func newRunID() string {
	return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
}

// SortRecords 按时间排序（导出前使用）。
func SortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool { return records[i].At.Before(records[j].At) })
}
