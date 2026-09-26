package plugin

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config 是插件的配置值集合。
//
// 它有意**不暴露 map**：动态键值只存在于 SDK 内部。插件作者有两种用法：
//
//  1. 推荐——声明一个结构体，用 DecodeConfig 一次解出来（SDK 内部走 JSON 转义）：
//
//     type myConfig struct {
//     Endpoint string `json:"endpoint"`
//     Timeout  int    `json:"timeout"`
//     }
//     var c myConfig
//     if err := plugin.DecodeConfig(cfg.Config, &c); err != nil { ... }
//
//  2. 需要单个值时用带类型的读取方法：cfg.Config.String("endpoint", "")、
//     cfg.Config.Int("timeout", 30)、cfg.Config.Bool("verbose", false)。
//
// Config 可被 JSON 编解码，因此能直接跨进程传输。
type Config struct {
	values map[string]string
}

// NewConfig 用已有的键值对构造配置（宿主把清单里的配置交给插件时使用）。
func NewConfig(values map[string]string) Config {
	return Config{values: values}
}

// Set 写入一项配置。
func (c *Config) Set(key, value string) {
	if c.values == nil {
		c.values = make(map[string]string)
	}
	c.values[key] = value
}

// Get 返回原始字符串值（键不存在时为空串）。
func (c Config) Get(key string) string { return c.values[key] }

// Has 报告某个键是否存在。
func (c Config) Has(key string) bool {
	_, ok := c.values[key]
	return ok
}

// Len 返回配置项数量。
func (c Config) Len() int { return len(c.values) }

// Keys 返回全部键（字典序），用于日志与排错。
func (c Config) Keys() []string {
	out := make([]string, 0, len(c.values))
	for k := range c.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String 返回字符串值；缺失或为空白时返回 def。
func (c Config) String(key, def string) string {
	if v := strings.TrimSpace(c.values[key]); v != "" {
		return v
	}
	return def
}

// Int 返回整数值；缺失或无法解析时返回 def。
func (c Config) Int(key string, def int) int {
	v := c.String(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Bool 返回布尔值；缺失或无法解析时返回 def。
func (c Config) Bool(key string, def bool) bool {
	switch strings.ToLower(c.String(key, "")) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return def
	}
}

// Duration 返回时长值（如 "30s"、"5m"）；缺失或无法解析时返回 def。
func (c Config) Duration(key string, def time.Duration) time.Duration {
	v := c.String(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// MarshalJSON 实现 json.Marshaler（跨进程传输时使用）。
func (c Config) MarshalJSON() ([]byte, error) {
	if c.values == nil {
		return []byte("null"), nil
	}
	return json.Marshal(c.values)
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (c *Config) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		c.values = nil
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%w: 配置必须是「字符串 → 字符串」的映射: %v", ErrBadConfig, err)
	}
	c.values = m
	return nil
}

// clone 返回深拷贝。
func (c Config) clone() Config {
	out := Config{}
	for _, k := range c.Keys() {
		out.Set(k, c.values[k])
	}
	return out
}

// DecodeConfig 把配置解码到结构体（按字段的 json tag 匹配）。
//
// 配置的值在清单与界面里永远是字符串（"true"、"30"、"30s"），而结构体字段可能
// 是 bool / int / time.Duration / 列表。SDK 在这里按目标字段的类型做一次转换，
// 插件作者只需要声明结构体，不必自己 Parse 一遍：
//
//	type myConfig struct {
//		Endpoint string        `json:"endpoint"`
//		Timeout  time.Duration `json:"timeout"` // "30s" / "500ms" 都行
//		Verbose  bool          `json:"verbose"`  // "true" / "1" / "yes"
//		Mirrors  []string      `json:"mirrors"`  // 逗号或换行分隔
//	}
//
// 值本身已经是 JSON（对象、数组）时原样交给 json 包。
func DecodeConfig(cfg Config, out any) error {
	if out == nil {
		return fmt.Errorf("%w: DecodeConfig 的目标不能为空", ErrBadConfig)
	}
	if len(cfg.values) == 0 {
		return nil // 空配置不应覆盖目标结构体已有的值
	}
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("%w: DecodeConfig 的目标必须是非空指针，实际是 %T", ErrBadConfig, out)
	}

	types := fieldTypes(rv.Elem().Type())
	raw := make(map[string]any, len(cfg.values))
	for k, v := range cfg.values {
		raw[k] = coerceValue(v, typeOf(types, k))
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("编码配置: %w", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: 配置无法解码到 %T: %v", ErrBadConfig, out, err)
	}
	return nil
}

// durationType 用于把 time.Duration 与普通整数区分开（它的 Kind 也是 Int64）。
var durationType = reflect.TypeOf(time.Duration(0))

// fieldTypes 收集结构体的「json 名 → 类型」，用于指导值转换。
//
// 同时登记原名与小写名：encoding/json 匹配字段时忽略大小写。
func fieldTypes(t reflect.Type) map[string]reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			name = strings.Split(tag, ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
		}
		out[name] = f.Type
		out[strings.ToLower(name)] = f.Type
	}
	return out
}

// typeOf 按 json 名（忽略大小写）查目标类型。
func typeOf(types map[string]reflect.Type, key string) reflect.Type {
	if types == nil {
		return nil
	}
	if t, ok := types[key]; ok {
		return t
	}
	return types[strings.ToLower(key)]
}

// coerceValue 把清单里的字符串值转成目标字段能接受的 JSON 值。
//
// 目标是未知类型（结构体里没这个名字）时原样保留字符串，让 json 包去报错 ——
// 静默吞掉拼错的键会让插件作者以为配置生效了。
func coerceValue(s string, t reflect.Type) any {
	if t == nil {
		return s
	}
	trimmed := strings.TrimSpace(s)
	switch {
	case t == durationType:
		if d, err := time.ParseDuration(trimmed); err == nil {
			return int64(d)
		}
	case t.Kind() == reflect.Bool:
		if b, err := strconv.ParseBool(trimmed); err == nil {
			return b
		}
		// 兼容 yaml / 界面里常见的写法。
		switch strings.ToLower(trimmed) {
		case "yes", "on":
			return true
		case "no", "off":
			return false
		}
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n
		}
	case t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uint64:
		if n, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
			return n
		}
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f
		}
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return splitList(trimmed)
	case t.Kind() == reflect.Slice || t.Kind() == reflect.Map || t.Kind() == reflect.Struct:
		// 复杂类型（对象、数组的数组）：值本身就该是 JSON 文本 ——
		// 界面上这类配置项是文本框，插件作者填的就是 JSON。
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v
		}
	}
	return s
}

// splitList 把逗号、分号或换行分隔的文本切成列表。
func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
