// Package apps 读写软件清单（<设置目录>/apps.yaml）并构建运行时 AppRef。
//
// 清单只描述「要管哪些软件、怎么装」，upkit 自身的设置见 settings 包。
// 同时提供旧版单软件 config.yaml 的迁移。
package apps

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/util"
)

// 默认值。
var (
	defaultPreserve       = []string{"User Data", "*.log"}
	defaultDetectPortable = []string{"state-file", "pe-resource", "dir-name"}
)

// 来源标识与限定 ID 的写法属于清单层的事实，宿主与适配器都引用这里，
// 避免同一个契约在多处各写一遍。
const (
	// KindPlugin 是插件来源的 kind 值（内置来源不需要声明）。
	KindPlugin = "plugin"
	// SourceKindPluginPrefix 是插件来源的类型前缀：清单里写成 "plugin:<来源ID>"。
	SourceKindPluginPrefix = KindPlugin + ":"
	// QualifiedIDSeparator 分隔限定 ID 里的来源与软件：<来源ID>/<软件ID>。
	QualifiedIDSeparator = "/"
	// KeyKind 是四条轴段落的通用键：source.kind / unpack.kind / method.kind，
	// 决定该段使用哪个适配器；KeyKindAlias 是它的历史别名。
	KeyKind      = "kind"
	KeyKindAlias = "type"
	// SourceKeyApp 是 source 段的保留键：软件在插件内的原始 ID
	// （与限定 ID 里的软件部分可能不同）。
	SourceKeyApp = "app"
	// FallbackAppID 是推不出合法 id 时使用的兜底值。
	FallbackAppID = "app"
)

// File 是设置目录下的来源与状态文件。//
// 只有 Sources、Conflicts、Equivalents、NotEquivalent 会落盘；
// Apps 是运行时字段（yaml:"-"），启动时由各插件来源填充，用户不手写。
type File struct {
	Version       int           `yaml:"version"`
	Sources       []SourceSpec  `yaml:"sources"`
	Conflicts     Conflicts     `yaml:"conflicts"`
	Equivalents   []Equivalence `yaml:"equivalents"`
	NotEquivalent [][]string    `yaml:"not_equivalent"`

	// Apps 由插件提供，不序列化：软件的来源只能是订阅，不能在文件里声明。
	Apps []AppSpec `yaml:"-"`
	Path string    `yaml:"-"`
}

// SourceSpec 描述一个「软件来源」。
//
// 内置源无需声明（始终存在）；这里主要声明插件源：插件可执行文件位置、信任哈希、
// 以及该来源内部每个软件的启用开关。
type SourceSpec struct {
	ID string `yaml:"id"`
	// Name 是展示名；留空时用插件自己声明的名字。
	Name string `yaml:"name,omitempty"`
	// Kind 目前只有 plugin（内置源不需要写进来）。
	Kind string `yaml:"kind"`
	// Exec 是插件可执行文件；相对路径按插件目录解析，留空则按 <id>[.exe] 查找。
	Exec string `yaml:"exec"`
	// Mode 为 catalog | full，留空表示 catalog。
	Mode string `yaml:"mode"`
	// Enabled 缺省为 true。
	Enabled *bool `yaml:"enabled"`
	// Trust 是插件可执行文件的 sha256。为空或与实际不符时一律不启动该插件。
	Trust string `yaml:"trust"`
	// TimeoutSeconds 是启动与单次调用的超时，缺省 20 秒。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// Config 是插件级配置（对应插件的 ConfigSchema）。
	Config map[string]string `yaml:"config"`
	// Apps 是源内软件级开关；未列出的软件沿用插件给出的默认（启用）。
	Apps []SourceAppSpec `yaml:"apps"`
	// Path 记录实际使用的清单文件（不在文件里出现）。
	Path string `yaml:"-"`
}

// SourceAppSpec 是源内单个软件的开关。
type SourceAppSpec struct {
	ID      string `yaml:"id"`
	Enabled *bool  `yaml:"enabled"`
}

// EnabledValue 报告来源是否启用（缺省启用）。
func (s SourceSpec) EnabledValue() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// AppEnabled 报告源内某个软件是否启用（未声明或缺省值视为启用）。
func (s SourceSpec) AppEnabled(appID string) bool {
	for _, a := range s.Apps {
		if a.ID != appID {
			continue
		}
		if a.Enabled == nil {
			return true
		}
		return *a.Enabled
	}
	return true
}

// SourceAppEnabled 报告某个来源内某个软件是否启用（来源不存在时视为启用）。
func (f *File) SourceAppEnabled(sourceID, appID string) bool {
	for _, s := range f.Sources {
		if s.ID == sourceID {
			return s.AppEnabled(appID)
		}
	}
	return true
}

// SetAppEnabled 记录源内某个软件的启用状态（就地修改，由调用方负责落盘）。
func (s *SourceSpec) SetAppEnabled(appID string, enabled bool) {
	v := enabled
	for i := range s.Apps {
		if s.Apps[i].ID == appID {
			s.Apps[i].Enabled = &v
			return
		}
	}
	s.Apps = append(s.Apps, SourceAppSpec{ID: appID, Enabled: &v})
}

// SetSourceTrust 记录某个插件来源的信任哈希（就地修改，由调用方负责落盘）。
//
// 来源不在清单里时补一条最小条目：宿主会扫描插件目录，把手工放进 plugin/ 的插件也
// 报出来，但这类插件只存在于运行时，不写进清单就没有地方承载信任决定
// （sidecar 按设计不承载它，见 pluginhost.Manifest）。只写 id / kind / trust，
// exec 与 mode 之类留给 sidecar —— 同一份信息在两个文件里各写一遍，迟早改一处忘一处。
func (f *File) SetSourceTrust(id, trust string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	for i := range f.Sources {
		if f.Sources[i].ID == id {
			f.Sources[i].Trust = trust
			return
		}
	}
	f.Sources = append(f.Sources, SourceSpec{ID: id, Kind: KindPlugin, Trust: trust})
}

// AppSpec 是单个软件的声明。
//
// 它由插件来源提供（不再从文件读取），字段保留 yaml tag 只为导出与调试可读。
type AppSpec struct {
	ID      string         `yaml:"id"`
	Name    string         `yaml:"name"`
	Enabled *bool          `yaml:"enabled"`
	Source  map[string]any `yaml:"source"`
	Unpack  map[string]any `yaml:"unpack"`
	Method  map[string]any `yaml:"method"`
	Detect  []string       `yaml:"detect"`
	Install InstallSpec    `yaml:"install"`
	Update  UpdateSpec     `yaml:"update"`
	Tags    []string       `yaml:"tags"`
}

// InstallSpec 描述安装位置与保护路径。
type InstallSpec struct {
	Path        string   `yaml:"path"`
	Entrypoints []string `yaml:"entrypoints"`
	Preserve    []string `yaml:"preserve"`
	Processes   []string `yaml:"processes"`
}

// UpdateSpec 描述更新策略。
type UpdateSpec struct {
	Pin           string `yaml:"pin"`
	TrackRevision bool   `yaml:"track_revision"`
}

// Conflicts 是冲突处理策略（三档一律 block，可由用户手工改成 warn 以放宽）。
type Conflicts struct {
	SameID     string `yaml:"same_id"`
	SameTarget string `yaml:"same_target"`
	Fuzzy      string `yaml:"fuzzy"`
}

// Equivalence 是用户手工声明的「这些是同一个软件」。
type Equivalence struct {
	Canonical string   `yaml:"canonical"`
	Members   []string `yaml:"members"`
	Policy    string   `yaml:"policy"`
}

// Default 返回带默认冲突策略的空清单。
func Default() *File {
	return &File{
		Version:   2,
		Conflicts: Conflicts{SameID: "block", SameTarget: "block", Fuzzy: "block"},
	}
}

// Load 读取清单；文件不存在时返回空清单（Path 指向将创建的位置）。
func Load(path string) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("apps.yaml 路径为空")
	}
	path = util.ExpandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			f := Default()
			f.Path = path
			return f, nil
		}
		return nil, fmt.Errorf("读取清单 %s: %w", path, err)
	}
	f := Default()
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(f); err != nil && err != io.EOF {
		return nil, fmt.Errorf("解析清单 %s: %w", path, err)
	}
	f.Path = path
	f.normalize()
	return f, nil
}

// Save 原子写回清单。
func (f *File) Save() error {
	if f.Path == "" {
		return fmt.Errorf("清单路径为空")
	}
	if f.Version == 0 {
		f.Version = 2
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("序列化清单: %w", err)
	}
	return fsutil.WriteFileAtomic(f.Path, data, 0o644)
}

// normalize 补全默认值。
func (f *File) normalize() {
	if f.Version == 0 {
		f.Version = 2
	}
	if f.Conflicts.SameID == "" {
		f.Conflicts.SameID = "block"
	}
	if f.Conflicts.SameTarget == "" {
		f.Conflicts.SameTarget = "block"
	}
	if f.Conflicts.Fuzzy == "" {
		f.Conflicts.Fuzzy = "block"
	}
}

// Enabled 报告某条清单是否启用（缺省启用）。
func (a AppSpec) EnabledValue() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// Build 把清单展开成运行时 AppRef 列表（顺序保持不变）。
func (f *File) Build() ([]core.AppRef, error) {
	out := make([]core.AppRef, 0, len(f.Apps))
	seen := map[string]int{}
	for i, spec := range f.Apps {
		if !spec.EnabledValue() {
			continue
		}
		ref, err := f.buildOne(spec)
		if err != nil {
			return nil, fmt.Errorf("apps[%d]: %w", i, err)
		}
		if prev, dup := seen[ref.ID]; dup {
			return nil, fmt.Errorf("apps[%d]: 重复的 app id %q（与 apps[%d] 冲突）", i, ref.ID, prev)
		}
		seen[ref.ID] = i
		out = append(out, ref)
	}
	return out, nil
}

// buildOne 展开单个软件声明。
func (f *File) buildOne(spec AppSpec) (core.AppRef, error) {
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return core.AppRef{}, fmt.Errorf("缺少 id")
	}
	if err := validateID(id); err != nil {
		return core.AppRef{}, err
	}
	path := util.ExpandPath(spec.Install.Path)
	if path == "" {
		return core.AppRef{}, fmt.Errorf("缺少 install.path")
	}

	method, methodOpts := splitKind(spec.Method, "portable-inplace")
	source, sourceOpts := splitKind(spec.Source, "github-release")
	unpack, unpackOpts := splitKind(spec.Unpack, "")
	if unpack == "" {
		if method == "portable-inplace" {
			unpack = "zip"
		} else {
			unpack = "raw"
		}
	}

	detect := spec.Detect
	if len(detect) == 0 {
		if method == "portable-inplace" {
			detect = append([]string(nil), defaultDetectPortable...)
		} else {
			detect = []string{"state-file", "cli-version"}
		}
	}

	preserve := spec.Install.Preserve
	if len(preserve) == 0 {
		preserve = append([]string(nil), defaultPreserve...)
	}
	entrypoints := spec.Install.Entrypoints
	processes := spec.Install.Processes
	if len(processes) == 0 && len(entrypoints) > 0 {
		processes = append([]string(nil), entrypoints...)
	}

	return core.AppRef{
		ID:            id,
		Name:          strings.TrimSpace(spec.Name),
		Source:        source,
		SourceOpts:    sourceOpts,
		Unpack:        unpack,
		UnpackOpts:    unpackOpts,
		Method:        method,
		MethodOpts:    methodOpts,
		Detect:        detect,
		InstallPath:   path,
		Entrypoints:   entrypoints,
		Processes:     processes,
		Preserve:      preserve,
		Pin:           strings.TrimSpace(spec.Update.Pin),
		TrackRevision: spec.Update.TrackRevision,
		Tags:          spec.Tags,
	}, nil
}

// PluginRef 是插件来源里一个软件的定位：来源 ID + 插件内软件 ID。
type PluginRef struct {
	SourceID string
	AppID    string
}

// ParsePluginRef 从清单条目里解析出插件来源与插件内软件。
//
// 约定：Source 形如 "plugin:<来源ID>"，插件内软件 ID 由 SourceOpts 的 app 键给出；
// 缺失时退化为按限定 ID <来源ID>/<软件ID> 拆分（兼容手工编写的清单）。
//
// 来源轴、方法轴、探测器轴的插件适配器共用这一份解析，避免三处各写一遍前缀规则。
func ParsePluginRef(app core.AppRef) (PluginRef, error) {
	kind := strings.TrimSpace(app.Source)
	if !strings.HasPrefix(kind, SourceKindPluginPrefix) {
		return PluginRef{}, fmt.Errorf("%w: %q 不是插件来源（应以 %q 开头）",
			core.ErrUnsupported, kind, SourceKindPluginPrefix)
	}
	ref := PluginRef{
		SourceID: strings.TrimPrefix(kind, SourceKindPluginPrefix),
		AppID:    strings.TrimSpace(app.SourceOpts[SourceKeyApp]),
	}
	if ref.AppID == "" {
		if i := strings.LastIndex(app.ID, QualifiedIDSeparator); i >= 0 {
			ref.AppID = app.ID[i+len(QualifiedIDSeparator):]
		}
	}
	if ref.SourceID == "" || ref.AppID == "" {
		return PluginRef{}, fmt.Errorf("插件来源 %q 缺少来源 ID 或软件 ID", app.Source)
	}
	return ref, nil
}

// splitKind 从 "kind + 其余键值" 的映射里取出 kind 与扁平化选项。
func splitKind(m map[string]any, defKind string) (string, map[string]string) {
	opts := map[string]string{}
	kind := defKind
	for k, v := range m {
		if k == KeyKind || k == KeyKindAlias {
			if s := stringify(v); s != "" {
				kind = s
			}
			continue
		}
		opts[k] = stringify(v)
	}
	return kind, opts
}

// stringify 把 YAML 值转成字符串（列表用逗号连接）。
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, stringify(item))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", t)
	}
}

// validateID 校验软件 ID 的文件系统安全性。
func validateID(id string) error {
	if len(id) > 64 {
		return fmt.Errorf("id 过长（最多 64 字符）")
	}
	for i, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("id 含非法字符 %q（只允许字母、数字、-、_、.）", string(r))
		}
		if i == 0 && (r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("id 不能以 %q 开头", string(r))
		}
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("id 不能包含 ..")
	}
	return nil
}

// IDs 返回清单里全部 id（含被禁用的）。
func (f *File) IDs() []string {
	out := make([]string, 0, len(f.Apps))
	for _, a := range f.Apps {
		out = append(out, a.ID)
	}
	sort.Strings(out)
	return out
}

// Find 按 id 查找清单条目。
func (f *File) Find(id string) (int, bool) {
	for i, a := range f.Apps {
		if a.ID == id {
			return i, true
		}
	}
	return 0, false
}

// SetEnabled 修改启用状态并写回。
//
// 状态落在来源声明（sources[].apps[]）上，而不是运行时内存里的 AppSpec ——
// 后者每次启动都由插件重新生成，写在它上面重启就没了。
func (f *File) SetEnabled(id string, enabled bool) error {
	i, ok := f.Find(id)
	if !ok {
		return fmt.Errorf("未找到软件 %q", id)
	}
	sourceID, ok := SourceIDOf(f.Apps[i])
	if !ok {
		return fmt.Errorf("软件 %q 不属于任何可持久化的来源，无法保存启用状态", id)
	}
	for si := range f.Sources {
		if f.Sources[si].ID != sourceID {
			continue
		}
		v := enabled
		f.Apps[i].Enabled = &v                             // 立即在内存生效，界面马上能看到
		f.Sources[si].SetAppEnabled(f.Apps[i].ID, enabled) // 落盘，重启后仍然生效
		return f.Save()
	}
	return fmt.Errorf("软件 %q 的来源 %q 未在来源列表中找到", id, sourceID)
}

// SourceIDOf 从软件声明里解析出它所属的来源 ID。
//
// 插件来源的类型写成 plugin:<来源ID>，因此来源 ID 就是前缀之后的部分。
func SourceIDOf(spec AppSpec) (string, bool) {
	kind, _ := spec.Source[KeyKind].(string)
	if kind == "" {
		if alias, ok := spec.Source[KeyKindAlias].(string); ok {
			kind = alias
		}
	}
	id, ok := strings.CutPrefix(kind, SourceKindPluginPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// Enabled 返回某个软件是否启用（未声明时默认启用）。
func (f *File) Enabled(id string) bool {
	i, ok := f.Find(id)
	if !ok {
		return true
	}
	return f.Apps[i].EnabledValue()
}
