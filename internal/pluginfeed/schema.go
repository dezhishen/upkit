package pluginfeed

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SchemaVersion 是订阅文件当前的 schema 版本。
//
// 宿主只接受自己认识的小于等于该值的订阅；更高版本会被拒绝（而不是猜着解析），
// 因为新版本可能有宿主不认识的语义。
const SchemaVersion = 1

// Feed 是订阅文件的顶层结构。
type Feed struct {
	Schema    int       `yaml:"schema"`
	Name      string    `yaml:"name"`
	UpdatedAt time.Time `yaml:"updated_at"`
	Plugins   []Plugin  `yaml:"plugins"`
}

// Plugin 是订阅里的一个插件条目。
type Plugin struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Homepage    string `yaml:"homepage"`
	Version     string `yaml:"version"`
	// Mode 为 catalog | full，缺省按 catalog 处理。
	Mode string `yaml:"mode"`
	// MinHostVersion 是要求的最低宿主版本；宿主更旧时该条目会被标记为不可用，
	// 而不是等启动后才报协议不兼容。
	MinHostVersion string   `yaml:"min_host_version"`
	Packages       Packages `yaml:"packages"`
}

// Package 是某个平台上的插件产物。
type Package struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
	Size   int64  `yaml:"size"`
}

// Packages 是「平台 → 包」的映射，例如 "windows/amd64"。
//
// 它内部仍是 map（JSON 对象天然如此），但**不向调用方暴露 map**：读取用 For，
// 枚举用 Platforms。这样上层代码与 UI 都只面对结构化数据。
type Packages struct {
	items map[string]Package
}

// NewPackages 用已有的映射构造（测试与程序内部使用）。
func NewPackages(items map[string]Package) Packages {
	return Packages{items: items}
}

// For 返回指定平台的包。
func (p Packages) For(platform string) (Package, bool) {
	pkg, ok := p.items[platform]
	return pkg, ok
}

// Platforms 返回全部平台标识（字典序）。
func (p Packages) Platforms() []string {
	out := make([]string, 0, len(p.items))
	for k := range p.items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Len 返回包数量。
func (p Packages) Len() int { return len(p.items) }

// MarshalYAML 实现 yaml.Marshaler（和 JSON 共用同一套结构定义）。
func (p Packages) MarshalYAML() (any, error) {
	if p.items == nil {
		return nil, nil
	}
	return p.items, nil
}

// UnmarshalYAML 实现 yaml.Unmarshaler。
func (p *Packages) UnmarshalYAML(unmarshal func(any) error) error {
	var m map[string]Package
	if err := unmarshal(&m); err != nil {
		return fmt.Errorf("packages 必须是「平台 → 包」的映射: %w", err)
	}
	p.items = m
	return nil
}

// upkit 只发行 Windows 版本，因此「宿主平台」里的操作系统部分是常量，只有架构跟着
// 构建目标走。开发机（可能是 Linux/macOS）上编译与测试同样成立：测的是「如果宿主
// 是 Windows」这条路径，而不是又多支持了一个系统。
const (
	// TargetOS 是 upkit 唯一支持的操作系统。
	TargetOS = "windows"
	// ArchAMD64 与 ArchARM64 是两个支持的架构。
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// Platform 返回宿主的目标平台标识，形如 "windows/amd64"。
//
// 注意它取的是 runtime.GOARCH 而不是 runtime.GOOS：在开发机上跑测试时得到的仍然是
// Windows 平台的包，这样订阅里的平台键就只有 Windows 一种，不会有“顺手支持一下”
// 的歧义。
func Platform() string { return PlatformOf(TargetOS, runtime.GOARCH) }

// PlatformOf 返回指定 GOOS/GOARCH 的平台标识（测试与工具使用）。
func PlatformOf(goos, goarch string) string {
	return strings.TrimSpace(goos) + "/" + strings.TrimSpace(goarch)
}

// SupportedPlatforms 返回全部受支持的平台键（amd64 在前）。
func SupportedPlatforms() []string {
	return []string{PlatformOf(TargetOS, ArchAMD64), PlatformOf(TargetOS, ArchARM64)}
}

// SupportedPlatform 报告某个平台键是否受支持：必须是非 Windows 以外的系统一律不算。
func SupportedPlatform(platform string) bool {
	osName, arch, ok := strings.Cut(strings.TrimSpace(platform), "/")
	if !ok || osName != TargetOS {
		return false
	}
	return arch == ArchAMD64 || arch == ArchARM64
}

// Validate 检查订阅的完整性与自洽性。
//
// 校验在下载任何东西之前完成：宁可在预览阶段报错，也不要装到一半失败。
// hostPlatform 为空时按当前平台校验。
func (f *Feed) Validate(hostVersion, hostPlatform string) error {
	if f == nil {
		return fmt.Errorf("订阅为空")
	}
	if f.Schema <= 0 {
		return fmt.Errorf("订阅缺少 schema 版本")
	}
	if f.Schema > SchemaVersion {
		return fmt.Errorf("订阅 schema 版本为 %d，当前宿主只支持 %d（请升级 upkit）", f.Schema, SchemaVersion)
	}
	if len(f.Plugins) == 0 {
		return fmt.Errorf("订阅里没有任何插件")
	}
	if hostPlatform == "" {
		hostPlatform = Platform()
	}
	if !SupportedPlatform(hostPlatform) {
		return fmt.Errorf("不支持的平台 %q：upkit 只支持 %s",
			hostPlatform, strings.Join(SupportedPlatforms(), "、"))
	}

	seen := map[string]int{}
	for i, p := range f.Plugins {
		if err := p.validate(hostVersion, hostPlatform); err != nil {
			return fmt.Errorf("plugins[%d]: %w", i, err)
		}
		if prev, dup := seen[p.ID]; dup {
			return fmt.Errorf("plugins[%d]: 插件 id %q 重复（与 plugins[%d] 冲突）", i, p.ID, prev)
		}
		seen[p.ID] = i
	}
	return nil
}

func (p Plugin) validate(hostVersion, hostPlatform string) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("缺少 id")
	}
	if !ValidID(p.ID) {
		return fmt.Errorf("id %q 不合法（要求 ^[a-z0-9][a-z0-9._-]{0,63}$，且不得为 Windows 保留名）", p.ID)
	}
	if p.Packages.Len() == 0 {
		return fmt.Errorf("插件 %s 没有任何平台的包", p.ID)
	}
	if _, ok := p.Packages.For(hostPlatform); !ok {
		return fmt.Errorf("插件 %s 没有 %s 平台的包（可选：%v）", p.ID, hostPlatform, p.Packages.Platforms())
	}
	// 订阅里出现其它平台的包不报错也不采纳：它可能同时服务别的工具，
	// upkit 只挑 Windows 的那一份。
	for _, platform := range p.Packages.Platforms() {
		pkg, _ := p.Packages.For(platform)
		if err := pkg.validate(); err != nil {
			return fmt.Errorf("插件 %s 的 %s 包: %w", p.ID, platform, err)
		}
	}
	if p.MinHostVersion != "" && hostVersion != "" && CompareVersions(hostVersion, p.MinHostVersion) < 0 {
		return fmt.Errorf("插件 %s 要求宿主版本 >= %s，当前为 %s", p.ID, p.MinHostVersion, hostVersion)
	}
	return nil
}

func (pkg Package) validate() error {
	if strings.TrimSpace(pkg.URL) == "" {
		return fmt.Errorf("缺少 url")
	}
	// sha256 是强制的：订阅等于远程代码执行授权，没有哈希就无法判断下载到的东西。
	if !ValidSHA256(pkg.SHA256) {
		return fmt.Errorf("缺少合法的 sha256（插件包必须提供 64 位十六进制摘要）")
	}
	if pkg.Size < 0 {
		return fmt.Errorf("size 不能为负")
	}
	return nil
}
