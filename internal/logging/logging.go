// Package logging 提供结构化日志（JSONL）、内存环形缓冲与审计日志。
//
// 格式化与输出用 go.uber.org/zap，文件轮转用 lumberjack；
// 敏感字段脱敏由本包实现（zap 不自带，见 zap.go 的 redactCore）。
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

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
	ring *Ring
	core.Logger

	// closer 负责关闭轮转器（lumberjack 实现了 io.Closer）。
	closer io.Closer

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

	if strings.TrimSpace(opts.Dir) != "" {
		_ = os.MkdirAll(opts.Dir, 0o755)
	}

	logger, closer := newZapLogger(opts, m.ring, m.opts.RunID)
	m.closer = closer
	m.Logger = &adapter{log: logger}

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
	if m.closer != nil {
		return m.closer.Close()
	}
	return nil
}

// ── core.Logger 适配 ──────────────────────────────────────────

type adapter struct {
	log *zap.Logger
}

func (a *adapter) Debug(msg string, kv ...any) { a.log.Debug(msg, toFields(kv)...) }
func (a *adapter) Info(msg string, kv ...any)  { a.log.Info(msg, toFields(kv)...) }
func (a *adapter) Warn(msg string, kv ...any)  { a.log.Warn(msg, toFields(kv)...) }
func (a *adapter) Error(msg string, kv ...any) { a.log.Error(msg, toFields(kv)...) }

func (a *adapter) With(kv ...any) core.Logger {
	return &adapter{log: a.log.With(toFields(kv)...)}
}

var sensitiveKeys = []string{"token", "password", "authorization", "secret", "apikey", "api_key", "cookie"}

// redacted 是替换后的占位符。
const redacted = "***"

// isSensitiveKey 判断字段名本身是否携带凭据。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// credentialKeys 是文本里可能跟随凭据的键名（小写；匹配时按 ASCII 大小写不敏感）。
//
// 只写键名不写「键=」是因为值可能用 = 或 : 两种写法：
//
//	?token=abc          （query）
//	{"token":"abc"}     （JSON）
var credentialKeys = []string{
	"token", "password", "passwd", "secret", "apikey", "api_key",
	"authorization", "cookie",
}

// redactText 抹掉字符串里可能出现的凭据。
//
// 分两条路径：
//   - 整串就是一个绝对 URL 时做结构化替换（按参数名替换值，其余原样保留）；
//   - 其余情况按键名做就地替换。
//
// 两条路径都在有限步内结束，不依赖调用方超时。
func redactText(s string) string {
	if s == "" {
		return s
	}
	if u, ok := parseAbsoluteURL(s); ok {
		return redactURL(u)
	}
	return redactInline(s)
}

// parseAbsoluteURL 判断 s 是否整体就是一个绝对 URL。
//
// 要求不含空白字符，避免把「拉取 https://… 失败」这类整句日志当成 URL 解析而丢掉原文。
func parseAbsoluteURL(s string) (*url.URL, bool) {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return nil, false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// redactURL 结构化抹掉 URL 中的凭据：query 里的敏感参数与 userinfo 里的密码。
//
// 与就地替换相比，它按参数边界取值，不会误伤 value 之外的内容，
// 也不存在「写回去的 *** 又被下一轮匹配到」的隐患。
func redactURL(u *url.URL) string {
	out := *u
	changed := false
	if out.User != nil {
		if _, ok := out.User.Password(); ok {
			out.User = url.UserPassword(out.User.Username(), redacted)
			changed = true
		}
	}
	if out.RawQuery != "" {
		q := out.Query()
		for k := range q {
			if isSensitiveKey(k) {
				q.Set(k, redacted)
				changed = true
			}
		}
		if changed {
			out.RawQuery = q.Encode()
		}
	}
	if !changed {
		return out.String()
	}
	// Encode 把占位符里的 * 转义成 %2A，写进日志后看不出是脱敏标记；
	// 而 * 与 %2A 在 URL 里语义相同，替换回去无损（字面量 %2A 会被编码成 %252A，不受影响）。
	return strings.ReplaceAll(out.String(), "%2A", "*")
}

// redactInline 就地把凭据值替换成 redacted。
//
// 两处必须成立的不变量：
//  1. 找到键名后要先确认它确实是一个键（后接 = 或 : 或引号），否则 tokenizer 这类
//     普通单词会被误伤；
//  2. 每一轮 pos 都必须前进，否则写到串里的 redacted 会让下一轮又匹配到自己。
//     旧实现正是缺了第 2 条：它每次都从新串的头部重新查找，而 "token=***" 里仍然
//     含有 "token="，于是 s 在 "token=***" 上原地自转，永不退出。
func redactInline(s string) string {
	for _, key := range credentialKeys {
		pos := 0
		for pos < len(s) {
			i := indexFoldASCII(s[pos:], key)
			if i < 0 {
				break
			}
			keyStart := pos + i
			keyEnd := keyStart + len(key)

			// 不是键（例如 tokenizer、tokenizeCount）就跳过这个词继续找。
			if keyEnd >= len(s) || !isKeyTerminator(s[keyEnd]) {
				pos = keyEnd
				continue
			}
			// 越过 = / : / 引号，定位到值的起点。
			valStart := keyEnd
			for valStart < len(s) && isKeySeparator(s[valStart]) {
				valStart++
			}
			valEnd := valStart
			spansSpace := keySpansSpace(key)
			for valEnd < len(s) && !credentialValueEndsAt(s, valEnd, spansSpace) {
				valEnd++
			}
			s = s[:valStart] + redacted + s[valEnd:]
			pos = valStart + len(redacted) // 跳过刚写入的占位符，保证前进
		}
	}
	return s
}

// credentialValueEndsAt 判断位置 i 是否是凭据值的结束处。
//
// 空白要单独判断：它通常终止一个值（token=abc 后面的空格），
// 但 authorization 的值本身含空格（Bearer <token>），此时不能停。
func credentialValueEndsAt(s string, i int, spansSpace bool) bool {
	c := s[i]
	if c == ' ' || c == '\t' {
		return !spansSpace
	}
	return isCredentialDelimiter(c)
}

// keySpansSpace 报告该键的值是否会包含空格。
//
// authorization 的值形如 "Bearer <token>"，只抹掉 Bearer 会把后面的 token 留在日志里。
func keySpansSpace(key string) bool {
	return key == "authorization"
}

// isKeyTerminator 判断键名之后的字节是否说明它真的是个键。
func isKeyTerminator(c byte) bool {
	return c == '=' || c == ':' || c == '"'
}

// isKeySeparator 是键与值之间的连接符。
func isKeySeparator(c byte) bool {
	return c == '=' || c == ':' || c == '"'
}

// isCredentialDelimiter 判断字节是否终止一个凭据值。
//
// 分隔符取宽一些：多抹（把值截短）只是可读性损失，漏抹则是凭据泄漏。
func isCredentialDelimiter(c byte) bool {
	switch c {
	case '&', ' ', '\t', '\r', '\n', '"', '\'', '`', ',', ';', ')', ']', '}':
		return true
	}
	return false
}

// indexFoldASCII 在 s 中查找 needle，仅对 ASCII 字母做大小写折叠。
//
// 与 strings.ToLower 不同，它不改变字节长度，所以返回的偏移可直接用于切分 s。
// 旧实现拿 ToLower 的结果算偏移、再去切原串，遇到 K（U+212A）这类折叠后字节数
// 变化的字符时会错位。needle 必须是小写 ASCII。
func indexFoldASCII(s, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		j := 0
		for ; j < len(needle); j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != needle[j] {
				break
			}
		}
		if j == len(needle) {
			return i
		}
	}
	return -1
}

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
