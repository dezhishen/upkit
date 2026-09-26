package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// mustReturn 在限定时间内跑完 fn，超时即判定失败。
//
// 脱敏曾经因为循环不前进而永不返回，直接调用会让整个测试包永久挂起（CI 表现为超时
// 而不是失败）。这里加一道护栏，让同类回归表现为一条明确的失败信息。
func mustReturn(t *testing.T, in string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- redactText(in) }()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatalf("redactText(%q) 未在 3 秒内返回，疑似死循环", in)
		return ""
	}
}

func TestRedactText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"无凭据原样返回", "普通日志消息", "普通日志消息"},

		// 旧实现在这两类输入上死循环：写入的 *** 自身仍含 token=
		{"单个查询参数", "token=abc", "token=***"},
		{"已是脱敏结果（幂等）", "token=***", "token=***"},
		{"脱敏结果后接参数", "token=***&x=1", "token=***&x=1"},

		{"空值", "token=", "token=***"},
		{"问号开头", "?token=abc&page=2", "?token=***&page=2"},
		{"重复参数都抹", "token=a&token=b", "token=***&token=***"},
		{"中间位置", "x=1&token=***&y=2", "x=1&token=***&y=2"},

		{"大小写不敏感", "TOKEN=abc", "TOKEN=***"},
		{"混合大小写", "Token=abc", "Token=***"},

		{"access_token 由子串覆盖", "access_token=abc", "access_token=***"},
		{"password", "password=hunter2", "password=***"},
		{"api_key", "api_key=k-123", "api_key=***"},
		{"authorization", "authorization=Bearer abc", "authorization=***"},

		{"冒号分隔（JSON）", `{"token":"abc"}`, `{"token":"***"}`},
		{"冒号不带引号", "token:abc", "token:***"},
		{"多个凭据字段", `{"token":"a","password":"b"}`, `{"token":"***","password":"***"}`},

		{"普通单词不误伤", "tokenizer=1", "tokenizer=1"},
		{"普通单词不误伤（大小写）", "TOKENIZE_COUNT=1", "TOKENIZE_COUNT=1"},

		{"引号包裹", `token="abc"`, `token="***"`},
		{"中文前后文", "请求失败 token=abc 请重试", "请求失败 token=*** 请重试"},
		{"制表符分隔", "token=abc\tpage=2", "token=***\tpage=2"},
		{"中文键前缀", "密钥token=abc", "密钥token=***"},
		{"中文值被抹掉", "token=秘密", "token=***"},

		// 整串是绝对 URL 时走结构化替换：非敏感参数原样保留
		{"完整 URL 保留其它参数", "https://x.test/f?token=abc&page=2", "https://x.test/f?page=2&token=***"},
		{"完整 URL 无凭据原样返回", "https://x.test/f?a=1", "https://x.test/f?a=1"},
		{"URL userinfo 密码", "https://u:p@x.test/f", "https://u:***@x.test/f"},
		{"整句里的 URL 走就地替换", "拉取 https://x.test/f?token=abc 失败", "拉取 https://x.test/f?token=*** 失败"},
		// 以 http 开头但含空格 → 不是「整串是 URL」，退回就地替换，不重新编码
		{"URL 带空格退回就地替换", "https://x.test/f?token=abc 失败", "https://x.test/f?token=*** 失败"},
		{"http 协议同样处理", "http://x.test/f?token=abc&p=1", "http://x.test/f?p=1&token=***"},
		{"缺 host 退回就地替换", "https:///f?token=abc", "https:///f?token=***"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustReturn(t, tc.in); got != tc.want {
				t.Errorf("redactText(%q)\n  得到 %q\n  期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactTextTerminates 是死循环的回归用例。
//
// 旧实现每轮都对新串从头查找 "token="，而替换结果 "token=***" 仍含该子串，
// 于是 s 在 "token=***" 上原地自转。这些输入必须能返回。
func TestRedactTextTerminates(t *testing.T) {
	inputs := []string{
		"token=abc",
		"token=***",
		"token=***&x=1",
		"password=***",
		"access_token=***",
		"token=a&token=b&token=c",
		"token=1 token=2 token=3",
		strings.Repeat("token=***&", 200),
		"https://x.test/f?token=abc",
	}
	for _, in := range inputs {
		mustReturn(t, in) // 超时会 t.Fatal
	}
}

// TestRedactTextIdempotent 验证对已脱敏结果再跑一次不会改变结果，
// 也不会无限增长（旧实现每次替换都会再插入一个 ***）。
func TestRedactTextIdempotent(t *testing.T) {
	seeds := []string{
		"token=abc",
		"访问 https://x.test/f?token=abc&p=1 失败",
		`{"token":"abc"}`,
		"password=p secret=s",
	}
	for _, seed := range seeds {
		once := mustReturn(t, seed)
		twice := mustReturn(t, once)
		if once != twice {
			t.Errorf("脱敏不幂等：%q\n  第一次 %q\n  第二次 %q", seed, once, twice)
		}
	}
}

func TestIndexFoldASCII(t *testing.T) {
	cases := []struct {
		name   string
		s      string
		needle string
		want   int
	}{
		{"精确匹配", "token=abc", "token=", 0},
		{"大写折叠", "TOKEN=abc", "token=", 0},
		{"混合大小写", "ToKeN=abc", "token=", 0},
		{"未命中", "abc", "token=", -1},
		{"末尾匹配", "x=1&token=", "token=", 4},
		{"空 needle", "abc", "", 0},
		{"needle 比 s 长", "ab", "abcdef", -1},
		// 多字节字符必须按字节原样比较，不能因折叠而错位
		{"中文前置", "密钥token=abc", "token=", 6},
		{"KELVIN 符号（ToLower 会改变其字节长度）", "\u212a token=abc", "token=", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexFoldASCII(tc.s, tc.needle); got != tc.want {
				t.Errorf("indexFoldASCII(%q, %q) = %d，期望 %d", tc.s, tc.needle, got, tc.want)
			}
		})
	}
}

// TestIndexFoldASCIIKeepsByteOffsets 验证返回值可以直接用于切分原串。
func TestIndexFoldASCIIKeepsByteOffsets(t *testing.T) {
	s := "\u212a KEY=value" // KELVIN SIGN + 空格 + KEY=value
	i := indexFoldASCII(s, "key=")
	if i < 0 {
		t.Fatal("未找到 key=")
	}
	if got := s[i:]; got != "KEY=value" {
		t.Errorf("偏移错位：s[%d:] = %q", i, got)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"token", "TOKEN", "access_token", "api_key", "apikey", "password", "authorization", "Cookie"}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = false，期望 true", k)
		}
	}
	plain := []string{"app", "url", "version", "phase", "page", "message"}
	for _, k := range plain {
		if isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = true，期望 false", k)
		}
	}
}

func TestRedactField(t *testing.T) {
	// 键名本身敏感：整个值被替换，不保留任何片段
	got := redactField(zap.String("token", "raw-secret"))
	if got.String != redacted {
		t.Errorf("敏感键的值未被替换：%q", got.String)
	}

	// 键名不敏感但值里含凭据：只替换值里的凭据部分
	got = redactField(zap.String("url", "https://x.test/f?token=abc&p=1"))
	if want := "https://x.test/f?p=1&token=" + redacted; got.String != want {
		t.Errorf("值里的凭据未被替换：得到 %q，期望 %q", got.String, want)
	}

	// 非字符串类型原样保留
	got = redactField(zap.Int("count", 3))
	if got.Integer != 3 {
		t.Errorf("非字符串字段被改动：%v", got)
	}
}

// newTestLogger 造一条只写内存的完整链路（JSON encoder + 脱敏 core）。
func newTestLogger(buf *bytes.Buffer) *zap.Logger {
	encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		MessageKey: "msg", LevelKey: "level", TimeKey: "ts",
		EncodeLevel: zapcore.LowercaseLevelEncoder,
		EncodeTime:  zapcore.RFC3339NanoTimeEncoder,
	})
	inner := zapcore.NewCore(encoder, zapcore.AddSync(buf), zapcore.DebugLevel)
	return zap.New(redactCore{inner})
}

// TestRedactCoreEndToEnd 走一遍真实的 zap 链路。
func TestRedactCoreEndToEnd(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("拉取订阅失败",
		zap.String("url", "https://x.test/feed.yaml?token=abc&page=2"),
		zap.String("token", "raw-secret"),
		zap.String("app", "demo"),
	)

	out := buf.String()
	for _, leak := range []string{"abc", "raw-secret"} {
		if strings.Contains(out, leak) {
			t.Errorf("日志中残留未脱敏的凭据 %q：%s", leak, out)
		}
	}
	if !strings.Contains(out, redacted) {
		t.Errorf("日志中没有出现脱敏占位符：%s", out)
	}
	if !strings.Contains(out, "demo") {
		t.Errorf("非敏感字段应保留：%s", out)
	}
}

// TestRedactCoreWithFields 验证 With 携带的字段同样被脱敏。
func TestRedactCoreWithFields(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	// 注意：zap 的 With 会先经 Core.With 固化字段，
	// 因此脱敏必须发生在 Write 阶段（redactCore 正是这么做的）。
	log.With(zap.String("token", "raw-secret")).Info("hello")

	if out := buf.String(); strings.Contains(out, "raw-secret") {
		t.Errorf("With 携带的敏感字段未脱敏：%s", out)
	}
}

// TestRedactCoreLeavesMessagesIntact 确认脱敏不会误伤普通消息。
func TestRedactCoreLeavesMessagesIntact(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("已安装 3 个软件，2 个可更新", zap.String("phase", "检查"))

	out := buf.String()
	for _, want := range []string{"已安装 3 个软件", "检查"} {
		if !strings.Contains(out, want) {
			t.Errorf("普通内容被误伤，缺少 %q：%s", want, out)
		}
	}
}

// TestManagerRedactsThroughRealChain 用 Manager 建一条完整链路（JSONL 文件 + 环形缓冲）。
func TestManagerRedactsThroughRealChain(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Options{
		Level: "info",
		Dir:   dir,
		Audit: true,
		RunID: "test-run",
		// Redact 默认开启，这里显式写出来强调意图
		Redact: true,
	})
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	defer func() { _ = m.Close() }()

	m.Info("拉取订阅失败", "url", "https://x.test/feed.yaml?token=abc")
	m.Audit(map[string]any{"action": "install", "app": "demo", "err": "token=abc"})

	// 环形缓冲里不应残留凭据
	for _, rec := range m.Ring().Snapshot() {
		if strings.Contains(rec.Msg, "abc") {
			t.Errorf("环形缓冲的消息残留凭据：%q", rec.Msg)
		}
		for k, v := range rec.KV {
			if s, ok := v.(string); ok && strings.Contains(s, "abc") {
				t.Errorf("环形缓冲的属性 %s 残留凭据：%q", k, s)
			}
		}
	}
}
