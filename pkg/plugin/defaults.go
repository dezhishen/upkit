package plugin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Defaults 是插件给宿主的「建议默认值」。
//
// 它是**结构化**的：插件作者填字段，不需要构造任何 map。宿主负责把它翻译成清单里
// 四条轴的配置，用户在清单里写的同 ID 条目可以覆盖这些建议值。
type Defaults struct {
	// Method 是建议的落地方式（如 "portable-inplace"、"exe-installer"）。
	Method string
	// Unpack 是建议的解包方式（如 "zip"、"raw"）。
	Unpack string
	// Detect 是建议的版本探测链，按顺序尝试。
	Detect []string
	// Pin 固定版本；非空时宿主忽略上游最新。
	Pin string
	// TrackRevision 让宿主在上游重发同一版本号时按修订号判断是否需要更新。
	TrackRevision bool
	// Install 是建议的安装目标。
	Install InstallDefaults
	// 三条轴各自的额外选项（如 source 的 asset、unpack 的 find_root）。
	SourceOptions Options
	UnpackOptions Options
	MethodOptions Options
}

// InstallDefaults 是建议的安装目标。
type InstallDefaults struct {
	// Path 支持 ${ROOT} / ${LOCALAPPDATA} / ${ARCH} 等变量。
	Path string
	// Entrypoints 是安装后应当存在的可执行文件，宿主用它复核安装结果。
	Entrypoints []string
	// Processes 是升级前需要关闭的进程名。
	Processes []string
	// Preserve 是升级时要保留的相对路径或通配符。
	Preserve []string
}

// clone 返回深拷贝。
func (d Defaults) clone() Defaults {
	out := d
	out.Detect = append([]string(nil), d.Detect...)
	out.Install.Entrypoints = append([]string(nil), d.Install.Entrypoints...)
	out.Install.Processes = append([]string(nil), d.Install.Processes...)
	out.Install.Preserve = append([]string(nil), d.Install.Preserve...)
	out.SourceOptions = d.SourceOptions.clone()
	out.UnpackOptions = d.UnpackOptions.clone()
	out.MethodOptions = d.MethodOptions.clone()
	return out
}

// IsZero 报告是否没有给出任何建议值。
func (d Defaults) IsZero() bool {
	return d.Method == "" && d.Unpack == "" && len(d.Detect) == 0 && d.Pin == "" &&
		!d.TrackRevision && d.Install.Path == "" && len(d.Install.Entrypoints) == 0 &&
		len(d.Install.Processes) == 0 && len(d.Install.Preserve) == 0 &&
		d.SourceOptions.Len() == 0 && d.UnpackOptions.Len() == 0 && d.MethodOptions.Len() == 0
}

// Options 是某个适配器的选项集合（如 source 的 asset、unpack 的 find_root）。
//
// 它**不暴露 map**：读用 Get/Has/Keys，写用 Set 或 NewOptions。内部仍然是键值对，
// 但那是 SDK 的实现细节，会随结构体一起被 JSON 编解码。
type Options struct {
	values map[string]string
}

// NewOptions 用交替的键值对构造选项：
//
//	plugin.NewOptions("asset", "*x64*.zip", "prerelease", "false")
//
// 参数个数为奇数时最后一个键被忽略。
func NewOptions(kv ...string) Options {
	o := Options{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i], kv[i+1])
	}
	return o
}

// Set 写入一项选项。
func (o *Options) Set(key, value string) {
	if o.values == nil {
		o.values = make(map[string]string)
	}
	o.values[key] = value
}

// Get 返回选项值（键不存在时为空串）。
func (o Options) Get(key string) string { return o.values[key] }

// Has 报告某个键是否存在。
func (o Options) Has(key string) bool {
	_, ok := o.values[key]
	return ok
}

// Len 返回选项数量。
func (o Options) Len() int { return len(o.values) }

// Keys 返回全部键（字典序）。
func (o Options) Keys() []string {
	out := make([]string, 0, len(o.values))
	for k := range o.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clone 返回深拷贝。
func (o Options) clone() Options {
	out := Options{}
	for _, k := range o.Keys() {
		out.Set(k, o.values[k])
	}
	return out
}

// MarshalJSON 实现 json.Marshaler。
func (o Options) MarshalJSON() ([]byte, error) {
	if o.values == nil {
		return []byte("null"), nil
	}
	return json.Marshal(o.values)
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (o *Options) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		o.values = nil
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%w: 选项必须是「字符串 → 字符串」的映射: %v", ErrBadConfig, err)
	}
	o.values = m
	return nil
}
