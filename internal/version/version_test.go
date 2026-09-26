package version

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in      string
		numbers string
		suffix  string
		wantErr bool
	}{
		{in: "131.0.6778.86", numbers: "131.0.6778.86"},
		{in: "v131.0.6778.86-1.1", numbers: "131.0.6778.86", suffix: "1.1"},
		{
			in:      "ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip",
			numbers: "131.0.6778.86",
			suffix:  "1.1",
		},
		{in: "  131.0.6778.86  ", numbers: "131.0.6778.86"},
		{in: "dev", wantErr: true},
		{in: "", wantErr: true},
		{in: "chrome", wantErr: true},
	}
	for _, c := range cases {
		v, err := Parse(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) 期望返回错误", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Parse(%q) 返回错误: %v", c.in, err)
		}
		if v.NumbersString() != c.numbers {
			t.Errorf("Parse(%q).NumbersString() = %q，期望 %q", c.in, v.NumbersString(), c.numbers)
		}
		if v.Suffix != c.suffix {
			t.Errorf("Parse(%q).Suffix = %q，期望 %q", c.in, v.Suffix, c.suffix)
		}
	}
}

func TestCompareIgnoresRevisionSuffix(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"131.0.6778.86-1.1", "131.0.6778.86", 0},
		{"131.0.6778.86-1.2", "131.0.6778.86-1.1", 0},
		{"132.0.0.0", "131.0.6778.86", 1},
		{"131.0.6778.85", "131.0.6778.86", -1},
		{"131.0.6778", "131.0.6778.0", 0},
		{"garbage", "131.0.0.0", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareFull(t *testing.T) {
	a := MustParse("131.0.6778.86-1.2")
	b := MustParse("131.0.6778.86-1.1")
	if got := a.CompareFull(b); got != 1 {
		t.Errorf("CompareFull = %d，期望 1", got)
	}
	if got := b.CompareFull(a); got != -1 {
		t.Errorf("CompareFull = %d，期望 -1", got)
	}
}

func TestVersionHelpers(t *testing.T) {
	v := MustParse("v131.0.6778.86-1.1")
	if v.Major() != 131 {
		t.Errorf("Major() = %d，期望 131", v.Major())
	}
	if v.String() != "131.0.6778.86-1.1" {
		t.Errorf("String() = %q", v.String())
	}
	if !Greater("131.0.6778.86", "130.0.0.0") {
		t.Error("Greater 判定错误")
	}
	if !Equal("131.0.6778.86-1.1", "131.0.6778.86") {
		t.Error("Equal 应忽略修订号")
	}
	if !MustParse("").IsZero() {
		t.Error("空版本应视为零值")
	}
}

func TestWriteReadRecordAndDetectLocal(t *testing.T) {
	dir := t.TempDir()

	v, err := DetectLocal(dir)
	if err != nil {
		t.Fatalf("DetectLocal: %v", err)
	}
	if v != "" {
		t.Fatalf("空目录应返回空版本，得到 %q", v)
	}

	rec := &Record{Version: "131.0.6778.86-1.1", Tag: "131.0.6778.86-1.1", Asset: "a.zip"}
	if err := WriteRecord(dir, rec); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	got, err := DetectLocal(dir)
	if err != nil {
		t.Fatalf("DetectLocal: %v", err)
	}
	if got != "131.0.6778.86-1.1" {
		t.Fatalf("DetectLocal = %q，期望 131.0.6778.86-1.1", got)
	}

	read, err := ReadRecord(dir)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if read == nil || read.Asset != "a.zip" {
		t.Fatalf("ReadRecord 结果异常: %+v", read)
	}
	if read.InstalledAt.IsZero() || time.Since(read.InstalledAt) > time.Hour {
		t.Errorf("InstalledAt 未正确填充: %v", read.InstalledAt)
	}
}

func TestDetectLocalFallsBackToDirName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ungoogled-chromium_131.0.6778.86-1.1_windows_x64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := DetectLocal(dir)
	if err != nil {
		t.Fatalf("DetectLocal: %v", err)
	}
	if got != "131.0.6778.86" {
		t.Fatalf("DetectLocal = %q，期望 131.0.6778.86", got)
	}
}

// TestLookupVersionValue 用合成的 VS_VERSION_INFO 片段验证 PE 解析逻辑。
func TestLookupVersionValue(t *testing.T) {
	const value = "131.0.6778.86"
	data := buildStringEntry([]byte{0xAA, 0xBB, 0xCC}, "ProductVersion", value)

	if got := lookupVersionValue(data, "ProductVersion"); got != value {
		t.Fatalf("lookupVersionValue = %q，期望 %q", got, value)
	}
	if got := lookupVersionValue(data, "FileVersion"); got != "" {
		t.Fatalf("不存在的键应返回空，得到 %q", got)
	}
	if got := lookupVersionValue([]byte("nothing here"), "ProductVersion"); got != "" {
		t.Fatalf("无版本信息时应返回空，得到 %q", got)
	}

	// 即使同名文本出现在值位置之后，也应继续向后找到真正的键。
	noisy := append(append([]byte{}, data...), buildStringEntry(nil, "ProductVersion", "132.0.0.0")...)
	if got := lookupVersionValue(noisy, "ProductVersion"); got != value {
		t.Fatalf("应优先返回首个合法值，得到 %q", got)
	}
}

func TestDecodeUTF16String(t *testing.T) {
	data := utf16LE([]byte("131.0.6778.86"))
	data = append(data, 0, 0)
	if got := decodeUTF16String(data, 0); got != "131.0.6778.86" {
		t.Fatalf("decodeUTF16String = %q", got)
	}
	if got := decodeUTF16String(data, len(data)); got != "" {
		t.Fatalf("越界读取应返回空，得到 %q", got)
	}
}

// buildStringEntry 构造一个符合 VS_VERSION_INFO 布局的 String 条目。
func buildStringEntry(prefix []byte, key, value string) []byte {
	var b bytes.Buffer
	b.Write(prefix)
	entryStart := b.Len()
	b.Write(make([]byte, 6)) // wLength / wValueLength / wType 占位
	b.Write(utf16LE([]byte(key)))
	b.Write([]byte{0, 0}) // 键的 NUL 终止符
	for (b.Len()-entryStart)%4 != 0 {
		b.WriteByte(0)
	}
	b.Write(utf16LE([]byte(value)))
	b.Write([]byte{0, 0})
	return b.Bytes()
}
