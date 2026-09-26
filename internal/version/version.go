// Package version 负责版本号解析、比较与本地版本探测。
//
// 本地版本来源按优先级排列：
//  1. upkit 自己写入的状态文件（.upkit-state.json）；
//  2. chrome.exe 的 PE 版本资源（VS_VERSION_INFO，纯 Go 解析，不依赖 PowerShell）；
//  3. 安装目录名中出现的版本号（部分用户手工解压时目录名带版本）。
package version

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/dezhishen/upkit/internal/util"
)

// RecordFile 是 upkit 在安装目录写入的状态文件名。
const RecordFile = ".upkit-state.json"

// numbersRe 用于从任意字符串中提取版本号。
var numbersRe = regexp.MustCompile(`\d+(?:\.\d+){1,3}`)

// Version 是解析后的版本号。
//
// Raw 保留原始字符串，Numbers 为点分数字部分，Suffix 为 "-1.1" 这类构建后缀。
type Version struct {
	Raw     string
	Numbers []int
	Suffix  string
}

// String 返回规范化后的版本字符串。
func (v Version) String() string {
	if v.Suffix == "" {
		return v.NumbersString()
	}
	return v.NumbersString() + "-" + v.Suffix
}

// NumbersString 返回点分数字部分，例如 "131.0.6778.86"。
func (v Version) NumbersString() string {
	parts := make([]string, len(v.Numbers))
	for i, n := range v.Numbers {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// Major 返回主版本号（解析失败时为 0）。
func (v Version) Major() int {
	if len(v.Numbers) == 0 {
		return 0
	}
	return v.Numbers[0]
}

// IsZero 报告版本号是否为空。
func (v Version) IsZero() bool { return len(v.Numbers) == 0 }

// Parse 解析版本字符串。
//
// 支持的写法：v131.0.6778.86、131.0.6778.86-1.1、
// ungoogled-chromium_131.0.6778.86-1.1_windows_x64。
func Parse(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Version{}, errors.New("版本字符串为空")
	}
	match := numbersRe.FindString(raw)
	if match == "" {
		return Version{}, fmt.Errorf("无法从 %q 中解析版本号", s)
	}
	v := Version{Raw: raw}
	for _, part := range strings.Split(match, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, fmt.Errorf("解析版本段 %q: %w", part, err)
		}
		v.Numbers = append(v.Numbers, n)
	}
	// 数字部分之后紧跟的 "-x.y" 视为构建后缀。
	rest := raw[strings.Index(raw, match)+len(match):]
	if strings.HasPrefix(rest, "-") {
		rest = strings.TrimLeft(rest, "-")
		end := strings.IndexFunc(rest, func(r rune) bool {
			return !(r >= '0' && r <= '9') && r != '.'
		})
		if end >= 0 {
			rest = rest[:end]
		}
		v.Suffix = strings.Trim(rest, ".")
	}
	return v, nil
}

// MustParse 与 Parse 相同，但解析失败时返回零值 Version。
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		return Version{}
	}
	return v
}

// Compare 比较两个版本字符串，返回 -1 / 0 / 1。
//
// 按照「忽略构建后缀」的语义比较，即 131.0.6778.86 与
// 131.0.6778.86-1.1 被视为同一版本。任一方无法解析时，
// 返回 0（视为相同），避免误判触发无意义的更新。
func Compare(a, b string) int {
	va, errA := Parse(a)
	vb, errB := Parse(b)
	if errA != nil || errB != nil {
		return 0
	}
	return va.CompareNumbers(vb)
}

// CompareNumbers 只比较数字部分。
func (v Version) CompareNumbers(other Version) int {
	n := len(v.Numbers)
	if len(other.Numbers) > n {
		n = len(other.Numbers)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(v.Numbers) {
			x = v.Numbers[i]
		}
		if i < len(other.Numbers) {
			y = other.Numbers[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// CompareFull 比较数字部分与构建后缀（用于展示「同内核、不同修订」）。
func (v Version) CompareFull(other Version) int {
	if c := v.CompareNumbers(other); c != 0 {
		return c
	}
	return strings.Compare(v.Suffix, other.Suffix)
}

// Greater 报告 a 是否比 b 新。
func Greater(a, b string) bool { return Compare(a, b) > 0 }

// Equal 报告两个版本是否相同（忽略构建后缀）。
func Equal(a, b string) bool { return Compare(a, b) == 0 }

// Record 是 upkit 写入安装目录的状态记录。
type Record struct {
	Version     string    `json:"version"`
	Tag         string    `json:"tag"`
	Asset       string    `json:"asset"`
	SHA256      string    `json:"sha256"`
	Source      string    `json:"source"`
	InstalledAt time.Time `json:"installed_at"`
	ToolVersion string    `json:"tool_version"`
}

// RecordPath 返回安装目录中状态文件的路径。
func RecordPath(installDir string) string {
	return filepath.Join(installDir, RecordFile)
}

// ReadRecord 读取状态文件；文件不存在时返回 (nil, nil)。
func ReadRecord(installDir string) (*Record, error) {
	path := RecordPath(installDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取状态文件: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("解析状态文件 %s: %w", path, err)
	}
	return &rec, nil
}

// WriteRecord 原子地写入状态文件。
func WriteRecord(installDir string, rec *Record) error {
	if rec == nil {
		return errors.New("状态记录为空")
	}
	if err := util.EnsureDir(installDir); err != nil {
		return err
	}
	if rec.InstalledAt.IsZero() {
		rec.InstalledAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态记录: %w", err)
	}
	data = append(data, '\n')

	target := RecordPath(installDir)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入状态文件: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("更新状态文件: %w", err)
	}
	return nil
}

// DetectLocal 探测安装目录中当前版本。
//
// 未安装或无法识别时返回空字符串（不视为错误）。
func DetectLocal(installDir string) (string, error) {
	if util.Exists(RecordPath(installDir)) {
		rec, err := ReadRecord(installDir)
		if err != nil {
			return "", err
		}
		if rec != nil && rec.Version != "" {
			return rec.Version, nil
		}
	}

	exe := filepath.Join(installDir, "chrome.exe")
	if util.FileExists(exe) {
		if v, err := ProductVersion(exe); err == nil && v != "" {
			return v, nil
		}
	}

	// 兜底：从安装目录名里找版本号（例如 .../ungoogled-chromium_131.0.6778.86-1.1_windows_x64）。
	if name := filepath.Base(filepath.Clean(installDir)); name != "" && name != "." && name != string(filepath.Separator) {
		if v := numbersRe.FindString(name); v != "" && strings.Count(v, ".") >= 2 {
			return v, nil
		}
	}
	return "", nil
}

// Installed 报告安装目录中是否存在 chrome.exe。
func Installed(installDir string) bool {
	return util.FileExists(filepath.Join(installDir, "chrome.exe")) ||
		util.FileExists(filepath.Join(installDir, "chrome"))
}

// ProductVersion 读取可执行文件的 ProductVersion（失败时回退 FileVersion）。
//
// 实现方式：直接解析 PE 的 VS_VERSION_INFO 资源，纯 Go、零外部命令，
// 因此在 Linux/macOS 上也能对 chrome.exe 做同样的解析（便于测试）。
func ProductVersion(exePath string) (string, error) {
	data, err := os.ReadFile(exePath)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w", exePath, err)
	}
	if v := lookupVersionValue(data, "ProductVersion"); v != "" {
		return v, nil
	}
	if v := lookupVersionValue(data, "FileVersion"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s 中未找到版本信息", filepath.Base(exePath))
}

// versionValuePattern 用于判定解析出的字符串「像不像版本号」。
var versionValuePattern = regexp.MustCompile(`^\d+(\.\d+){1,3}$`)

// lookupVersionValue 在 PE 版本资源中查找指定键的值。
//
// VS_VERSION_INFO 的 String 条目布局为：
//
//	WORD wLength; WORD wValueLength; WORD wType; WCHAR szKey[]; 对齐填充; WCHAR Value[]
//
// 键与值均为 UTF-16LE，且值按 32 位边界对齐（相对于条目起始位置）。
// 这里先定位键字符串，再尝试若干候选偏移，返回第一个符合版本号格式的值，
// 以规避恰好在其它数据中出现同名文本的情况。
func lookupVersionValue(data []byte, key string) string {
	keyBytes := utf16LE([]byte(key))
	for start := 0; start < len(data); {
		idx := indexBytes(data[start:], keyBytes)
		if idx < 0 {
			return ""
		}
		keyPos := start + idx
		keyEnd := keyPos + len(keyBytes)

		if v := pickVersion(data, candidates(data, keyPos, keyEnd)); v != "" {
			return v
		}
		start = keyEnd
	}
	return ""
}

// candidates 返回键之后可能存放值的候选偏移列表。
func candidates(data []byte, keyPos, keyEnd int) []int {
	const entryHeaderSize = 6 // wLength + wValueLength + wType
	entryStart := keyPos - entryHeaderSize

	out := make([]int, 0, 4)
	// 首选：跳过键的 NUL 终止符后按 4 字节对齐。
	aligned := keyEnd + 2
	if entryStart >= 0 {
		if pad := (4 - (aligned-entryStart)%4) % 4; pad > 0 {
			aligned += pad
		}
	}
	out = append(out, aligned, keyEnd, keyEnd+2, keyEnd+4, keyEnd+6)
	return out
}

// pickVersion 返回候选偏移中第一个符合版本号格式的值。
func pickVersion(data []byte, offsets []int) string {
	for _, pos := range offsets {
		if pos < 0 || pos >= len(data) {
			continue
		}
		if v := decodeUTF16String(data, pos); versionValuePattern.MatchString(v) {
			return v
		}
	}
	return ""
}

// utf16LE 把 ASCII 字节串转为 UTF-16LE 字节序列（用于在文件中定位）。
func utf16LE(s []byte) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, b := range s {
		out = append(out, b, 0x00)
	}
	return out
}

// decodeUTF16String 从 pos 开始读取一个以 NUL 结尾的 UTF-16LE 字符串。
func decodeUTF16String(data []byte, pos int) string {
	if pos < 0 || pos+2 > len(data) {
		return ""
	}
	var units []uint16
	for i := pos; i+1 < len(data); i += 2 {
		u := uint16(data[i]) | uint16(data[i+1])<<8
		if u == 0 {
			break
		}
		units = append(units, u)
		if len(units) > 64 {
			break
		}
	}
	if len(units) == 0 {
		return ""
	}
	return string(utf16.Decode(units))
}

// indexBytes 返回 sub 在 b 中首次出现的偏移，未找到返回 -1。
func indexBytes(b, sub []byte) int {
	return bytes.Index(b, sub)
}

// SortDesc 对版本字符串列表降序排序（无法解析的排在最后）。
func SortDesc(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool {
		return Compare(versions[i], versions[j]) > 0
	})
}
