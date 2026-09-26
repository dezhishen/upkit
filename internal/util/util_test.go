package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("空串原样返回", func(t *testing.T) {
		if got := ExpandPath(""); got != "" {
			t.Fatalf("得到 %q，期望空串", got)
		}
		if got := ExpandPath("   "); got != "" {
			t.Fatalf("纯空白应视为空串，得到 %q", got)
		}
	})

	t.Run("常见变量都能展开", func(t *testing.T) {
		cases := []struct {
			name   string
			in     string
			expect func(got string) bool
		}{
			// %TEMP% 在非 Windows 上走兜底，必须落到 os.TempDir 而不是原样保留。
			{"%TEMP%", `%TEMP%\upkit`, func(got string) bool {
				return filepath.IsAbs(got) && strings.HasSuffix(got, "upkit") &&
					!strings.Contains(got, "%")
			}},
			{"大小写混合的 %temp%", `%temp%\x`, func(got string) bool {
				return !strings.Contains(got, "%")
			}},
			// 未定义的变量保持原样，避免静默变成空路径。
			{"未定义的 %NOPE_X%", `%NOPE_X%\a`, func(got string) bool {
				return strings.Contains(got, "%NOPE_X%")
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := ExpandPath(tc.in); !tc.expect(got) {
					t.Fatalf("ExpandPath(%q) = %q，不满足预期", tc.in, got)
				}
			})
		}
	})

	t.Run("波浪号展开到用户目录", func(t *testing.T) {
		if home == "" {
			t.Skip("无法获取用户目录")
		}
		if got := ExpandPath("~"); got != filepath.Clean(home) {
			t.Fatalf("ExpandPath(~) = %q，期望 %q", got, filepath.Clean(home))
		}
		got := ExpandPath("~/x")
		if !strings.HasPrefix(got, filepath.Clean(home)) {
			t.Fatalf("ExpandPath(~/x) = %q，未落在 %q 下", got, home)
		}
	})

	t.Run("结果总是绝对路径且已清理", func(t *testing.T) {
		got := ExpandPath("./a/../b")
		if !filepath.IsAbs(got) {
			t.Fatalf("期望绝对路径，得到 %q", got)
		}
		if strings.Contains(got, "..") {
			t.Fatalf("路径未清理：%q", got)
		}
	})
}

func TestExistsFamily(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// 悬空符号链接：Exists 用 Lstat，应报存在；FileExists/DirExists 用 Stat，应报不存在。
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nope"), link); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	cases := []struct {
		path                  string
		exists, isFile, isDir bool
	}{
		{file, true, true, false},
		{sub, true, false, true},
		{filepath.Join(dir, "missing"), false, false, false},
		{link, true, false, false},
	}
	for _, tc := range cases {
		if got := Exists(tc.path); got != tc.exists {
			t.Errorf("Exists(%q) = %v，期望 %v", tc.path, got, tc.exists)
		}
		if got := FileExists(tc.path); got != tc.isFile {
			t.Errorf("FileExists(%q) = %v，期望 %v", tc.path, got, tc.isFile)
		}
		if got := DirExists(tc.path); got != tc.isDir {
			t.Errorf("DirExists(%q) = %v，期望 %v", tc.path, got, tc.isDir)
		}
	}
}

func TestEnsureDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")

	if err := EnsureDir(nested); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if !DirExists(nested) {
		t.Fatal("目录未创建")
	}
	// 重复调用必须幂等：调用方常在每次启动时无脑建目录。
	if err := EnsureDir(nested); err != nil {
		t.Fatalf("重复创建应无错，得到 %v", err)
	}
	// 目标是已存在的文件时应报错，不能静默成功。
	file := filepath.Join(base, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(file); err == nil {
		t.Fatal("目标已是文件时应报错")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "未知"},
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TiB"},
	}
	for _, tc := range cases {
		if got := HumanBytes(tc.in); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanSpeed(t *testing.T) {
	// 非正数与非法浮点必须有占位输出，不能渲染成 "0 B/s" 或 "NaN MiB/s"。
	for _, in := range []float64{0, -1, -0.5} {
		if got := HumanSpeed(in); got != "-- /s" {
			t.Errorf("HumanSpeed(%v) = %q，期望 %q", in, got, "-- /s")
		}
	}
	if got := HumanSpeed(1024); got != "1.00 KiB/s" {
		t.Errorf("HumanSpeed(1024) = %q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "--:--"},
		{0, "00:00"},
		{59 * time.Second, "00:59"},
		{time.Minute, "01:00"},
		{time.Hour, "1:00:00"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
		// 四舍五入到秒：59.6s 应进位成 1 分钟而不是显示 00:59。
		{59*time.Second + 600*time.Millisecond, "01:00"},
	}
	for _, tc := range cases {
		if got := HumanDuration(tc.in); got != tc.want {
			t.Errorf("HumanDuration(%v) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestTimestamp(t *testing.T) {
	got := Timestamp(time.Date(2026, 9, 26, 14, 30, 5, 0, time.UTC))
	if got != "20260926-143005" {
		t.Fatalf("得到 %q", got)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"普通名不变", "upkit.exe", "upkit.exe"},
		{"空格替换", "my app.exe", "my_app.exe"},
		{"Windows 非法字符", `a\b/c:d*e?f"g<h>i|j`, "a_b_c_d_e_f_g_h_i_j"},
		{"首尾点与下划线裁掉", "._name_.", "name"},
		{"首尾空白裁掉", "  name  ", "name"},
		{"中间的点保留", "a.b.txt", "a.b.txt"},
		{"空串", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeFileName(tc.in); got != tc.want {
				t.Errorf("SanitizeFileName(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatchFold(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "anything", true},
		{"*", "anything", true},
		{"**", "anything", true},
		{"*.exe", "SETUP.EXE", true},
		{"*.exe", "setup.msi", false},
		{"Setup.Exe", "setup.exe", true},
		{"demo*", "DemoApp", true},
		// 非法 pattern 必须返回 false，而不是 panic 或当成匹配。
		{"[", "x", false},
	}
	for _, tc := range cases {
		if got := MatchFold(tc.pattern, tc.name); got != tc.want {
			t.Errorf("MatchFold(%q, %q) = %v，期望 %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"max 为 0", "abcdef", 0, ""},
		{"max 为负", "abcdef", -1, ""},
		{"无需截断", "abc", 5, "abc"},
		{"恰好等长", "abcde", 5, "abcde"},
		// max<=3 时不加省略号，否则结果会比 max 还长。
		{"max 为 1", "abcdef", 1, "a"},
		{"max 为 3", "abcdef", 3, "abc"},
		{"max 为 4", "abcdef", 4, "a..."},
		{"max 为 5", "abcdef", 5, "ab..."},
		// 必须按 rune 截断，不能把中文切成半个字。
		{"中文不切坏", "张三李四王五", 3, "张三李"},
		{"中文带省略号", "张三李四王五", 4, "张..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q，期望 %q", tc.in, tc.max, got, tc.want)
			}
			if !isValidUTF8(got) {
				t.Errorf("Truncate(%q, %d) 产生了非法 UTF-8: %q", tc.in, tc.max, got)
			}
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"", "  ", "a"}, "a"},
		// 纯空白算空，不能被当成有效值返回。
		{[]string{"   ", "b"}, "b"},
		{[]string{"a", "b"}, "a"},
	}
	for _, tc := range cases {
		if got := FirstNonEmpty(tc.in...); got != tc.want {
			t.Errorf("FirstNonEmpty(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestIsHex(t *testing.T) {
	valid := []string{"0", "0123456789abcdef", "ABCDEF", "aBcD1234"}
	for _, s := range valid {
		if !IsHex(s) {
			t.Errorf("IsHex(%q) = false，期望 true", s)
		}
	}
	invalid := []string{"g", "xyz", "12 34", "0x12", "１２"}
	for _, s := range invalid {
		if IsHex(s) {
			t.Errorf("IsHex(%q) = true，期望 false", s)
		}
	}
	// 空串在实现上返回 true（循环不执行）；这里固定住该行为，
	// 提醒调用方必须自行做长度校验 —— ParseSHA256 正是这么做的。
	if !IsHex("") {
		t.Fatal("IsHex(\"\") 的既有语义是 true，调用方需自行校验长度")
	}
}

func TestParseSHA256(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const upper = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

	t.Run("三种写法都能解析", func(t *testing.T) {
		cases := []string{
			sum + "  upkit.exe",
			sum,
			"sha256:" + sum,
			"  " + sum + "  ",
		}
		for _, in := range cases {
			if got := ParseSHA256(in); got != sum {
				t.Errorf("ParseSHA256(%q) = %q，期望 %q", in, got, sum)
			}
		}
	})

	t.Run("统一转小写", func(t *testing.T) {
		if got := ParseSHA256(upper); got != sum {
			t.Errorf("得到 %q，期望小写形式", got)
		}
	})

	t.Run("只取第一行", func(t *testing.T) {
		// 多行清单只认第一行：第一行是摘要时取它，不会误取后面几条。
		content := sum + "  a.txt\n" + strings.Repeat("f", 64) + "  b.txt\n"
		if got := ParseSHA256(content); got != sum {
			t.Errorf("得到 %q，期望第一行的 %q", got, sum)
		}
		// 第一行不是摘要时返回空串 —— 这是有意行为：整份清单里的任意一行
		// 都可能碰巧是 64 位 hex，跳过首行去后面找会引入不确定性。
		content = "not-a-hash  a.txt\n" + sum + "  b.txt\n"
		if got := ParseSHA256(content); got != "" {
			t.Errorf("首行不是摘要时应返回空串，得到 %q", got)
		}
	})

	t.Run("解析不出时返回空串", func(t *testing.T) {
		bad := []string{
			"",
			"   ",
			"abc",
			// 长度不足 64：不能只看 IsHex。
			"0123456789abcdef",
			// 含非 hex 字符。
			strings.Repeat("z", 64),
			"hello world",
		}
		for _, in := range bad {
			if got := ParseSHA256(in); got != "" {
				t.Errorf("ParseSHA256(%q) = %q，期望空串", in, got)
			}
		}
	})
}

func TestDefaultHelpers(t *testing.T) {
	if got := DefaultString("", "def"); got != "def" {
		t.Errorf("DefaultString 空值应返回默认值，得到 %q", got)
	}
	if got := DefaultString("   ", "def"); got != "def" {
		t.Errorf("DefaultString 纯空白应返回默认值，得到 %q", got)
	}
	if got := DefaultString("v", "def"); got != "v" {
		t.Errorf("DefaultString 非空应原样返回，得到 %q", got)
	}

	if got := DefaultInt(0, 7); got != 7 {
		t.Errorf("DefaultInt(0, 7) = %d，期望 7", got)
	}
	if got := DefaultInt(3, 7); got != 3 {
		t.Errorf("DefaultInt(3, 7) = %d，期望 3", got)
	}
	// 负数不是零值，应原样保留 —— 范围校验是 Normalize 的职责，不是这里的。
	if got := DefaultInt(-1, 7); got != -1 {
		t.Errorf("DefaultInt(-1, 7) = %d，期望 -1", got)
	}
}
