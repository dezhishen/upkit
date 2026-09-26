package plugin

import (
	"regexp"
	"strings"
)

// Registration 是一条注册记录：一个软件 + 它的构造器 + 元信息。
type Registration struct {
	Software
	// New 是该软件的构造器，必填。
	New AppFactory
	// Schema 可选：静态声明该软件的配置项。声明是纯数据，宿主不必构造实例
	// 就能生成配置表单，因此即使构造器暂时失败（例如缺少必填配置）也能配置。
	Schema *ConfigSchema
}

// Option 用于定制 Registration。
type Option func(*Registration)

// WithName 设置展示名。
func WithName(name string) Option {
	return func(r *Registration) { r.Name = name }
}

// WithDescription 设置一句话说明。
func WithDescription(desc string) Option {
	return func(r *Registration) { r.Description = desc }
}

// WithHomepage 设置主页。
func WithHomepage(url string) Option {
	return func(r *Registration) { r.Homepage = url }
}

// WithTags 设置分组标签。
func WithTags(tags ...string) Option {
	return func(r *Registration) { r.Tags = append(r.Tags, tags...) }
}

// WithProvides 设置软身份（别名、上游 owner/repo 等），用于跨来源去重。
func WithProvides(provides ...string) Option {
	return func(r *Registration) { r.Provides = append(r.Provides, provides...) }
}

// WithTarget 设置安装目标提示。
func WithTarget(t TargetHint) Option {
	return func(r *Registration) { r.Target = &t }
}

// WithDefaults 设置建议的默认值（结构化字段，不需要构造 map）。
func WithDefaults(defaults Defaults) Option {
	return func(r *Registration) { r.Defaults = defaults }
}

// WithConfigSchema 静态声明该软件的配置项，宿主据此生成配置表单。
func WithConfigSchema(schema ConfigSchema) Option {
	return func(r *Registration) { r.Schema = &schema }
}

// Register 注册一个软件。
//
//	id    该软件在插件内的唯一标识（宿主对外呈现为 <插件ID>/<id>）
//	ctor  该软件的构造器，宿主首次需要它时调用
//	opts  可选的元信息（展示名、别名、安装目标建议、默认配置…）
//
//	plugin.Register("corp-vpn", NewCorpVPN, plugin.WithName("公司 VPN"))
func Register(id string, ctor AppFactory, opts ...Option) Registration {
	r := Registration{New: ctor}
	r.ID = strings.TrimSpace(id)
	r.Name = r.ID
	for _, opt := range opts {
		if opt != nil {
			opt(&r)
		}
	}
	if r.Name == "" {
		r.Name = r.ID
	}
	return r
}

// idPattern 是插件 ID 与软件 ID 的合法形式：小写字母数字开头，允许 . _ -
// 禁止路径分隔符与 ..，这是「插件私有目录由 ID 派生」的前提。
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// reservedNames 是 Windows 保留设备名，用作目录名会在 Windows 上失败。
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidID 报告一个 ID 是否合法（插件 ID 与软件 ID 共用同一规则）。
func ValidID(id string) bool {
	if !idPattern.MatchString(id) {
		return false
	}
	if strings.Contains(id, "..") {
		return false
	}
	base := id
	if i := strings.IndexAny(base, "."); i >= 0 {
		base = base[:i]
	}
	return !reservedNames[base]
}
