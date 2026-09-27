package pluginfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SchemaVersion 是订阅文件当前的 schema 版本。
//
//   - schema 1：最初版本，没有域名声明；
//   - schema 2：插件条目可以声明 download_hosts / plugin_hosts（可选，见 Plugin）。
//
// 宿主只接受自己认识的小于等于该值的订阅；更高版本会被拒绝（而不是猜着解析），
// 因为新版本可能有宿主不认识的语义。旧版本照旧接受：声明是可选的，不写就是
// 「未声明」，行为与 schema 1 一致。
const SchemaVersion = 2

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
	MinHostVersion string `yaml:"min_host_version"`
	// DownloadHosts 是这个插件可能下载东西的域名（schema 2 起，可选）。
	//
	// 它约束的是**插件给出的产物地址**：宿主在下载前核对域名，不在集合里就拒绛
	// 下载，并让用户重新确认订阅 —— 这样「插件能把下载指向哪里」在添加订阅时就
	// 已经摆明并确认过，而不是每次安装弹一次窗。
	//
	// 写主机名本身（如 github.com），不含协议与路径，也不支持通配符；留空表示
	// 未声明，宿主退回老规则（跨域下载逐次确认）。
	DownloadHosts []string `yaml:"download_hosts,omitempty"`
	// PluginHosts 是插件进程自己会访问的域名（schema 2 起，可选）。
	//
	// 仅供界面展示告知：插件是独立进程，它的网络访问宿主拦不住、也无法授权。
	// 与 DownloadHosts 分开写，是为了不让人以为这部分也被管住了。
	PluginHosts []string `yaml:"plugin_hosts,omitempty"`
	Packages    Packages `yaml:"packages"`
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

// pluginVersionRe 限制版本号的字符集。
//
// 版本号会被拼进缓存文件名（见 install.go 的 cacheName），放任任意字符就等于
// 把 filepath.Join 的越界能力交给订阅方：version: "../../x" 能写到任意路径。
var pluginVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

func (p Plugin) validate(hostVersion, hostPlatform string) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("缺少 id")
	}
	if !ValidID(p.ID) {
		return fmt.Errorf("id %q 不合法（要求 ^[a-z0-9][a-z0-9._-]{0,63}$，且不得为 Windows 保留名）", p.ID)
	}
	// version 可以为空（表示未声明），但一旦写了就必须是安全字符。
	if v := strings.TrimSpace(p.Version); v != "" && !pluginVersionRe.MatchString(v) {
		return fmt.Errorf("version %q 不合法（只允许字母、数字与 . _ + -，且不以符号开头）", p.Version)
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
	if err := validateHosts(p.ID, "download_hosts", p.DownloadHosts); err != nil {
		return err
	}
	if err := validateHosts(p.ID, "plugin_hosts", p.PluginHosts); err != nil {
		return err
	}
	return nil
}

// hostRe 限制声明里的主机名：不含协议、路径、端口、通配符，也不含下划线。
//
// 校验从严是故意的：声明是给人看、给机器比的，写错了应当在发布前就暴露，而不是
// 变成一条永远匹配不上的规则。
var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func validateHosts(id, field string, hosts []string) error {
	seen := make(map[string]bool, len(hosts))
	for _, raw := range hosts {
		h := strings.TrimSpace(raw)
		if h != strings.ToLower(h) {
			return fmt.Errorf("插件 %s 的 %s 里 %q 必须是小写主机名", id, field, raw)
		}
		if !hostRe.MatchString(h) {
			return fmt.Errorf("插件 %s 的 %s 里 %q 不是合法主机名（只写主机名，不含协议、路径、端口或通配符）", id, field, raw)
		}
		if seen[h] {
			return fmt.Errorf("插件 %s 的 %s 里 %q 重复", id, field, raw)
		}
		seen[h] = true
	}
	return nil
}

// AllowsDownload 报告某个下载地址是否落在该插件声明的域名集合内。
//
// 第二个返回值是地址的主机名，便于报错时说清是哪儿出的问题。
//
// 未声明 DownloadHosts 时一律返回 true：那是 schema 1 的老行为，由订阅域名授权 +
// 跨域逐次确认兜着。只接受**绝对地址** —— 相对地址要先相对订阅地址解析。
func (p Plugin) AllowsDownload(rawURL string) (bool, string) {
	host := HostOfURL(rawURL)
	if len(p.DownloadHosts) == 0 {
		return true, host
	}
	for _, h := range p.DownloadHosts {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true, host
		}
	}
	return false, host
}

// HostOfURL 返回地址的主机名（小写、去端口）；解析不出来时返回空串。
func HostOfURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// DeclaredFingerprint 是整份清单「声明面」的指纹：把每个插件的下载域名排序后拼
// 成稳定文本，再取 sha256。
//
// 它给「添加订阅时确认、更新时只在声明变了才重新确认」用：集合没变 → 指纹不变 →
// 用户不需要再看一遍；变了 → 宿主把新增的域名摆出来重新确认。排序是必须的，
// 否则生成器换个顺序就会让所有人的确认全部失效。
//
// 只覆盖**强制校验**的域名（download_hosts）：plugin_hosts 只是展示告知，它变了
// 不值得打扰用户。
func (f *Feed) DeclaredFingerprint() string {
	if f == nil {
		return ""
	}
	ids := make([]string, 0, len(f.Plugins))
	byID := make(map[string]Plugin, len(f.Plugins))
	for _, p := range f.Plugins {
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}
	sort.Strings(ids)

	var b strings.Builder
	for _, id := range ids {
		hosts := append([]string(nil), byID[id].DownloadHosts...)
		sort.Strings(hosts)
		b.WriteString(id)
		b.WriteByte(':')
		b.WriteString(strings.Join(hosts, ","))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
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
