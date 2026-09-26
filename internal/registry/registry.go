// Package registry 是四条适配器轴的注册表。
//
// 开闭原则的落点：新增来源 / 解包器 / 落地方式 / 探测器，只需加一个实现包并在
// all.Register 里登记一行；engine、tui、cmd 都不需要改动。
package registry

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/download"
)

// Deps 是注入给适配器的共享基础设施。
type Deps struct {
	HTTP     *http.Client
	Download *download.Client
	Log      core.Logger
	Clock    func() time.Time
	// Plugins 是插件宿主的窄接口（实现见 internal/pluginhost）。为 nil 时
	// 插件来源不可用，但内置适配器不受影响。
	Plugins PluginHost
}

// PluginHost 是插件宿主需要向适配器暴露的最小能力。
//
// 定义在这里而不是 pluginhost 包，是为了让适配器只依赖这个窄接口，
// 避免 适配器 ←→ 宿主 的循环依赖。
type PluginHost interface {
	// Versions 返回插件来源里某个软件的可用版本（新 → 旧）。
	Versions(ctx context.Context, sourceID, appID string, limit int) ([]core.Release, error)
	// Latest 返回插件来源里某个软件的最新版本。
	Latest(ctx context.Context, sourceID, appID string) (core.Release, error)
	// Method 返回插件为该软件提供的安装方法（full 模式）。
	//
	// ok 为 false 表示该来源没有声明 full 能力，适配器应报错而不是静默回退：
	// 清单里写了一个软件却没有可用的安装器，静默回退只会让人以为装上了。
	Method(ref core.AppRef) (core.InstallMethod, bool)
	// Status 返回插件报告的本机安装状态（full 模式）；ok 与 Method 同义。
	Status(ctx context.Context, ref core.AppRef) (core.Status, bool, error)
}

// SourceFactory 构造来源适配器。
type SourceFactory func(app core.AppRef, deps Deps) (core.SourceResolver, error)

// UnpackerFactory 构造解包适配器。
type UnpackerFactory func(app core.AppRef, deps Deps) (core.Unpacker, error)

// MethodFactory 构造落地方式适配器。
type MethodFactory func(app core.AppRef, deps Deps) (core.InstallMethod, error)

// DetectorFactory 构造版本探测器。
type DetectorFactory func(app core.AppRef, deps Deps) (core.Detector, error)

// Registry 保存四张工厂表。
type Registry struct {
	sources   map[string]SourceFactory
	prefixes  map[string]SourceFactory
	unpackers map[string]UnpackerFactory
	methods   map[string]MethodFactory
	detectors map[string]DetectorFactory
}

// New 创建一个空注册表。
func New() *Registry {
	return &Registry{
		sources:   map[string]SourceFactory{},
		prefixes:  map[string]SourceFactory{},
		unpackers: map[string]UnpackerFactory{},
		methods:   map[string]MethodFactory{},
		detectors: map[string]DetectorFactory{},
	}
}

// RegisterSource 注册来源适配器。
func (r *Registry) RegisterSource(kind string, f SourceFactory) { r.sources[kind] = f }

// RegisterSourcePrefix 注册带前缀的系列来源，如 "plugin:" 可匹配
// "plugin:corp-index"。精确注册优先于前缀注册。
func (r *Registry) RegisterSourcePrefix(prefix string, f SourceFactory) {
	r.prefixes[prefix] = f
}

// RegisterUnpacker 注册解包适配器。
func (r *Registry) RegisterUnpacker(kind string, f UnpackerFactory) { r.unpackers[kind] = f }

// RegisterMethod 注册落地方式适配器。
func (r *Registry) RegisterMethod(kind string, f MethodFactory) { r.methods[kind] = f }

// RegisterDetector 注册版本探测器。
func (r *Registry) RegisterDetector(kind string, f DetectorFactory) { r.detectors[kind] = f }

// Source 构造来源适配器。
func (r *Registry) Source(app core.AppRef, deps Deps) (core.SourceResolver, error) {
	if f, ok := r.sources[app.Source]; ok {
		return f(app, deps)
	}
	// 前缀匹配（如 plugin:corp-index）：按前缀长度倒序，保证更长、更具体的前缀优先。
	names := make([]string, 0, len(r.prefixes))
	for prefix := range r.prefixes {
		if strings.HasPrefix(app.Source, prefix) {
			names = append(names, prefix)
		}
	}
	if len(names) > 0 {
		sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
		return r.prefixes[names[0]](app, deps)
	}
	return nil, fmt.Errorf("%w: 未知来源类型 %q（可选：%v）", core.ErrUnsupported, app.Source, r.SourceKinds())
}

// Unpacker 构造解包适配器。
func (r *Registry) Unpacker(app core.AppRef, deps Deps) (core.Unpacker, error) {
	f, ok := r.unpackers[app.Unpack]
	if !ok {
		return nil, fmt.Errorf("%w: 未知解包类型 %q（可选：%v）", core.ErrUnsupported, app.Unpack, r.UnpackerKinds())
	}
	return f(app, deps)
}

// Method 构造落地方式适配器。
func (r *Registry) Method(app core.AppRef, deps Deps) (core.InstallMethod, error) {
	f, ok := r.methods[app.Method]
	if !ok {
		return nil, fmt.Errorf("%w: 未知安装方式 %q（可选：%v）", core.ErrUnsupported, app.Method, r.MethodKinds())
	}
	return f(app, deps)
}

// Detectors 按 app.Detect 声明的顺序构造探测链。
func (r *Registry) Detectors(app core.AppRef, deps Deps) ([]core.Detector, error) {
	out := make([]core.Detector, 0, len(app.Detect))
	for _, kind := range app.Detect {
		f, ok := r.detectors[kind]
		if !ok {
			return nil, fmt.Errorf("%w: 未知探测器 %q（可选：%v）", core.ErrUnsupported, kind, r.DetectorKinds())
		}
		d, err := f(app, deps)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("app %s 未声明任何探测器（detect）", app.ID)
	}
	return out, nil
}

// SourceKinds 返回已注册的来源类型。
func (r *Registry) SourceKinds() []string {
	out := sortedKeys(r.sources)
	for prefix := range r.prefixes {
		out = append(out, prefix+"*")
	}
	sort.Strings(out)
	return out
}

// UnpackerKinds 返回已注册的解包类型。
func (r *Registry) UnpackerKinds() []string { return sortedKeys(r.unpackers) }

// MethodKinds 返回已注册的安装方式。
func (r *Registry) MethodKinds() []string { return sortedKeys(r.methods) }

// DetectorKinds 返回已注册的探测器。
func (r *Registry) DetectorKinds() []string { return sortedKeys(r.detectors) }

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
