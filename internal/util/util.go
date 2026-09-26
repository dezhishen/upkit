// Package util 汇集项目内跨包复用的通用小工具。
//
// 这里的函数刻意保持无状态、无副作用，方便在任意平台上做单元测试。
package util

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// winVarRe 匹配 Windows 风格的 %VAR% 环境变量占位符。
var winVarRe = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

// windowsEnvFallback 为非 Windows 平台提供 Windows 常见环境变量的兜底值。
//
// 这样在 Linux/macOS 上（本地开发、CI、单元测试）也能正确展开
// %TEMP% / %LOCALAPPDATA% 之类的默认路径，而不是把它们原样留在路径里。
var windowsEnvFallback = map[string]func() string{
	"TEMP": func() string { return os.TempDir() },
	"TMP":  func() string { return os.TempDir() },
	"LOCALAPPDATA": func() string {
		if dir, err := os.UserConfigDir(); err == nil {
			return dir
		}
		return filepath.Join(os.TempDir(), "upkit-localappdata")
	},
	"APPDATA": func() string {
		if dir, err := os.UserConfigDir(); err == nil {
			return dir
		}
		return filepath.Join(os.TempDir(), "upkit-appdata")
	},
	"USERPROFILE": func() string {
		if dir, err := os.UserHomeDir(); err == nil {
			return dir
		}
		return os.TempDir()
	},
	"PROGRAMFILES": func() string { return string(filepath.Separator) + "Program Files" },
}

// ExpandPath 展开路径中的环境变量、~ 与相对路径，返回绝对路径。
//
// 同时支持 Windows 的 %VAR% 与类 Unix 的 $VAR / ${VAR} 写法。
// 空字符串原样返回。
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.Contains(p, "%") {
		p = evictWindowsVars(p)
	}
	if strings.ContainsAny(p, "$") {
		p = os.ExpandEnv(p)
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// evictWindowsVars 替换 %VAR%（大小写不敏感）为对应环境变量的值。
func evictWindowsVars(p string) string {
	return winVarRe.ReplaceAllStringFunc(p, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := lookupEnvFold(name); ok && v != "" {
			return v
		}
		if fn, ok := windowsEnvFallback[strings.ToUpper(name)]; ok {
			return fn()
		}
		return m
	})
}

// lookupEnvFold 以大小写不敏感的方式查找环境变量。
func lookupEnvFold(name string) (string, bool) {
	if v, ok := os.LookupEnv(name); ok {
		return v, true
	}
	upper := strings.ToUpper(name)
	for _, kv := range os.Environ() {
		k, v, found := strings.Cut(kv, "=")
		if found && strings.ToUpper(k) == upper {
			return v, true
		}
	}
	return "", false
}

// Exists 报告路径（文件或目录）是否存在。
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// FileExists 报告 path 是否为已存在的普通文件。
func FileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// DirExists 报告 path 是否为已存在的目录。
func DirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// EnsureDir 创建目录（含父目录），已存在时不报错。
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// HumanBytes 把字节数格式化为人类可读的形式，例如 "27.4 MiB"。
func HumanBytes(n int64) string {
	if n < 0 {
		return "未知"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// HumanSpeed 把 字节/秒 格式化为 "3.21 MiB/s"。
func HumanSpeed(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 || math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) {
		return "-- /s"
	}
	return HumanBytes(int64(bytesPerSecond)) + "/s"
}

// HumanDuration 把时长压缩成 m:ss 或 h:mm:ss 形式。
func HumanDuration(d time.Duration) string {
	if d < 0 {
		return "--:--"
	}
	total := int(d.Round(time.Second).Seconds())
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// Timestamp 返回用于文件名的时间戳，例如 20260926-143005。
func Timestamp(t time.Time) string {
	return t.Format("20060102-150405")
}

// SanitizeFileName 移除文件名中的非法字符，保证可安全用于 Windows/Linux。
func SanitizeFileName(name string) string {
	replacer := strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_", "?", "_",
		`"`, "_", "<", "_", ">", "_", "|", "_", " ", "_",
	)
	out := replacer.Replace(strings.TrimSpace(name))
	return strings.Trim(out, "._")
}

// MatchFold 以大小写不敏感的方式做 glob 匹配；pattern 为 "**" 时总是匹配。
func MatchFold(pattern, name string) bool {
	if pattern == "" || pattern == "*" || pattern == "**" {
		return true
	}
	ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
	return err == nil && ok
}

// Truncate 按 rune 截断字符串，超出部分以省略号结尾。
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

// FirstNonEmpty 返回第一个非空字符串。
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseSHA256 从校验文件内容中提取 SHA256 摘要。
//
// 兼容 "<64位hex>  file.zip"、单行摘要，以及 GitHub 的 "sha256:<hex>" 三种写法。
// 解析不出时返回空字符串。
func ParseSHA256(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	// 只取第一行，避免整个 sha256sum 清单被误解析。
	line := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	if i := strings.Index(line, ":"); i > 0 && !strings.Contains(line[:i], " ") {
		line = line[i+1:]
	}
	for _, f := range strings.Fields(line) {
		if len(f) == 64 && IsHex(f) {
			return strings.ToLower(f)
		}
	}
	return ""
}

// IsHex 报告字符串是否全部由十六进制字符组成。
func IsHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// DefaultString 在 v 为空时返回 def。
func DefaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// DefaultInt 在 v 为 0 时返回 def，用于配置文件缺省值兜底。
func DefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
