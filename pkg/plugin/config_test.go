package plugin

import (
	"testing"
	"time"
)

// 配置在清单与界面里永远是字符串，DecodeConfig 必须按目标字段的类型转一次，
// 否则 bool / int / Duration / 列表字段全都解不出来。
func TestDecodeConfigCoercesScalarTypes(t *testing.T) {
	type cfg struct {
		Endpoint string        `json:"endpoint"`
		Timeout  time.Duration `json:"timeout"`
		Verbose  bool          `json:"verbose"`
		Retries  int           `json:"retries"`
		Ratio    float64       `json:"ratio"`
		Mirrors  []string      `json:"mirrors"`
	}
	var out cfg
	in := NewConfig(map[string]string{
		"endpoint": "https://example.com",
		"timeout":  "30s",
		"verbose":  "yes",
		"retries":  "3",
		"ratio":    "0.25",
		"mirrors":  "a.example.com, b.example.com\nc.example.com",
	})
	if err := DecodeConfig(in, &out); err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if out.Endpoint != "https://example.com" {
		t.Fatalf("字符串字段错误: %q", out.Endpoint)
	}
	if out.Timeout != 30*time.Second {
		t.Fatalf("Duration 字段错误: %v", out.Timeout)
	}
	if !out.Verbose {
		t.Fatal("bool 字段错误：yes 应视为 true")
	}
	if out.Retries != 3 {
		t.Fatalf("int 字段错误: %d", out.Retries)
	}
	if out.Ratio != 0.25 {
		t.Fatalf("float 字段错误: %v", out.Ratio)
	}
	if len(out.Mirrors) != 3 || out.Mirrors[2] != "c.example.com" {
		t.Fatalf("列表字段错误: %+v", out.Mirrors)
	}
}

// 字段名匹配要忽略大小写，与 encoding/json 的行为一致。
func TestDecodeConfigMatchesFieldNameCaseInsensitively(t *testing.T) {
	type cfg struct {
		Endpoint string `json:"endpoint"`
	}
	var out cfg
	if err := DecodeConfig(NewConfig(map[string]string{"Endpoint": "x"}), &out); err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if out.Endpoint != "x" {
		t.Fatalf("大小写不敏感匹配失败: %q", out.Endpoint)
	}
}

// 空配置不覆盖已有值；目标不是指针时报错而不是 panic。
func TestDecodeConfigGuardRails(t *testing.T) {
	type cfg struct {
		A string `json:"a"`
	}
	out := cfg{A: "keep"}
	if err := DecodeConfig(NewConfig(nil), &out); err != nil {
		t.Fatalf("空配置不应报错: %v", err)
	}
	if out.A != "keep" {
		t.Fatalf("空配置不应覆盖已有值: %q", out.A)
	}
	if err := DecodeConfig(NewConfig(map[string]string{"a": "b"}), cfg{}); err == nil {
		t.Fatal("非指针目标应当报错")
	}
	if err := DecodeConfig(NewConfig(map[string]string{"a": "b"}), nil); err == nil {
		t.Fatal("nil 目标应当报错")
	}
}

// 值已经是 JSON 时（对象/数组）原样交给 json 包。
func TestDecodeConfigPassesThroughJSONValues(t *testing.T) {
	type inner struct {
		URL string `json:"url"`
	}
	type cfg struct {
		Items []inner `json:"items"`
	}
	var out cfg
	in := NewConfig(map[string]string{"items": `[{"url":"https://x"}]`})
	if err := DecodeConfig(in, &out); err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].URL != "https://x" {
		t.Fatalf("JSON 值没有原样传递: %+v", out.Items)
	}
}
