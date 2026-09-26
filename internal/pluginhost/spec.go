package pluginhost

import (
	"strings"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
)

// QualifiedID 返回插件软件在宿主里的限定 ID：<来源ID>/<软件ID>。
//
// 裸的软件 ID 只在单个来源内有意义，跨来源比较一律用限定 ID。
func QualifiedID(sourceID, appID string) string {
	if sourceID == "" {
		return appID
	}
	return sourceID + apps.QualifiedIDSeparator + appID
}

// ToAppSpec 把插件里的一个软件翻译成清单条目。
//
// 采用「翻译成 AppSpec，再交给 apps 的构建逻辑」的做法，是为了让插件软件复用内置
// 的默认推断（unpack / method / detect / preserve），而不是在插件侧另造一套规则。
// 插件给出的 Defaults 是结构化的，直接映射到四条轴；用户在清单里写的同 ID 条目
// 以清单为准（可以覆盖这里的建议值）。
//
// full 为 true 时（插件声明了 CapabilityFull），安装方式与探测器默认指向插件自己：
// 插件接管取包、解压与落地，宿主只提供工作目录与事件通道。插件在 Defaults 里显式
// 给出 method / detect 时仍然以它为准 —— 它比宿主更清楚单个软件该怎么装。
func ToAppSpec(sw upkitplugin.Software, sourceID string, full bool) apps.AppSpec {
	spec := apps.AppSpec{
		ID:   QualifiedID(sourceID, sw.ID),
		Name: firstNonEmpty(sw.Name, sw.ID),
		Tags: append([]string(nil), sw.Tags...),
		Source: map[string]any{
			apps.KeyKind:      apps.SourceKindPluginPrefix + sourceID,
			apps.SourceKeyApp: sw.ID,
		},
	}

	if sw.Target != nil {
		spec.Install = apps.InstallSpec{
			Path:        sw.Target.PathTemplate,
			Entrypoints: append([]string(nil), sw.Target.Entrypoints...),
			Processes:   append([]string(nil), sw.Target.Processes...),
			Preserve:    append([]string(nil), sw.Target.Preserve...),
		}
	}

	applyDefaults(&spec, sw.Defaults)

	// 插件声明了 full 能力时默认走插件自己的安装方式；也可以只给个别软件声明
	// （Defaults.Method = plugin）。两条途径等价，都是「插件接管安装」。
	if full && !hasKind(spec.Method) {
		spec.Method = setKind(spec.Method, KindPlugin)
	}
	// 由插件安装的软件，探测也必须走插件 —— 只有它知道装到哪了。
	// 插件显式声明了 detect 时以它为准（比如它还写了一份状态文件）。
	if kindOf(spec.Method) == KindPlugin && len(spec.Detect) == 0 {
		spec.Detect = []string{KindPlugin}
	}

	if spec.Name == "" {
		spec.Name = sw.ID
	}
	return spec
}

// kindOf 读出一段适配器配置里声明的类型（没有就是空串）。
func kindOf(seg map[string]any) string {
	if seg == nil {
		return ""
	}
	v, ok := seg[apps.KeyKind]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// hasKind 报告一段适配器配置里是否已经写明了类型。
func hasKind(seg map[string]any) bool { return kindOf(seg) != "" }

// applyDefaults 把插件的结构化建议值翻译成清单配置。
func applyDefaults(spec *apps.AppSpec, d upkitplugin.Defaults) {
	if d.Method != "" {
		spec.Method = setKind(spec.Method, d.Method)
	}
	if d.Unpack != "" {
		spec.Unpack = setKind(spec.Unpack, d.Unpack)
	}
	if len(d.Detect) > 0 {
		spec.Detect = append([]string(nil), d.Detect...)
	}
	if d.Pin != "" {
		spec.Update.Pin = d.Pin
	}
	if d.TrackRevision {
		spec.Update.TrackRevision = true
	}

	if d.Install.Path != "" {
		spec.Install.Path = d.Install.Path
	}
	if len(d.Install.Entrypoints) > 0 {
		spec.Install.Entrypoints = append([]string(nil), d.Install.Entrypoints...)
	}
	if len(d.Install.Processes) > 0 {
		spec.Install.Processes = append([]string(nil), d.Install.Processes...)
	}
	if len(d.Install.Preserve) > 0 {
		spec.Install.Preserve = append([]string(nil), d.Install.Preserve...)
	}

	spec.Source = applyOptions(spec.Source, d.SourceOptions)
	spec.Unpack = applyOptions(spec.Unpack, d.UnpackOptions)
	spec.Method = applyOptions(spec.Method, d.MethodOptions)
}

// applyOptions 把适配器选项并进对应轴的配置段。
func applyOptions(seg map[string]any, opts upkitplugin.Options) map[string]any {
	for _, k := range opts.Keys() {
		seg = setOpt(seg, k, opts.Get(k))
	}
	return seg
}

func setKind(m map[string]any, kind string) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[apps.KeyKind] = kind
	return m
}

func setOpt(m map[string]any, key, val string) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[key] = val
	return m
}

// ToCoreRelease 把插件的版本描述翻译成宿主领域类型。
func ToCoreRelease(r upkitplugin.Release) core.Release {
	arts := make([]core.Artifact, 0, len(r.Artifacts))
	for _, a := range r.Artifacts {
		arts = append(arts, core.Artifact{
			Name:   a.Name,
			URL:    a.URL,
			Size:   a.Size,
			Digest: a.Digest,
		})
	}
	return core.Release{
		Version:     r.Version,
		Tag:         r.Tag,
		Channel:     r.Channel,
		PublishedAt: r.PublishedAt,
		Notes:       r.Notes,
		Artifacts:   arts,
	}
}
