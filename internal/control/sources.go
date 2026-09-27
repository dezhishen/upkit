package control

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginhost"
	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"
)

// reloadTimeout 是重建插件来源（停旧进程、启新进程）的时间上限。
const reloadTimeout = 20 * time.Second

// SourceInfo 是一个插件来源的汇总状态：清单里的声明与宿主报出的运行期状态合并后
// 的结果。前端拿它渲染一行，不需要自己拼这两边。
type SourceInfo struct {
	ID string
	// Name 是清单里的名字，没有则用插件自报的名字。
	Name string
	// Declared 表示清单里有没有这条来源。
	//
	// 宿主会扫描插件目录，把「手工放进去、清单里没写」的插件也报出来 —— 这类来源
	// 在被信任之前一直缺一个可落盘的承载（信任写在清单里），所以界面上要能区分。
	Declared bool

	State   pluginhost.State
	Detail  string
	Version string
	Apps    int
	// Enabled 是清单里的启停开关（声明缺省为启用）。
	//
	// 不能拿 State 反推：来源在“启用但宿主没加载”时同样会显示 disabled。
	Enabled bool
	// Exec / SHA256 是宿主解析出的可执行文件路径与它实际算出的哈希。未信任状态
	// 靠这两个值变成可信。
	Exec   string
	SHA256 string
}

// PluginField 是插件声明的一个配置项及其当前值。
type PluginField struct {
	Key      string
	Label    string
	Value    string
	Default  string
	Help     string
	Required bool
	Secret   bool
}

// Sources 汇总插件来源：清单里声明的前，仅被宿主发现的后。
func (c *Controller) Sources() []SourceInfo {
	live := map[string]pluginhost.SourceStatus{}
	var discovered []pluginhost.SourceStatus
	if c.host != nil {
		for _, st := range c.host.Sources() {
			live[st.ID] = st
			discovered = append(discovered, st)
		}
	}

	out := make([]SourceInfo, 0, len(discovered)+len(c.manifestSources()))
	declared := map[string]bool{}
	for _, spec := range c.manifestSources() {
		declared[spec.ID] = true
		info := SourceInfo{ID: spec.ID, Name: spec.Name, Declared: true, Enabled: spec.EnabledValue()}
		switch {
		case !spec.EnabledValue():
			info.State, info.Detail = pluginhost.StateDisabled, "已在清单中停用"
		default:
			if st, ok := live[spec.ID]; ok {
				info.State, info.Detail = st.State, st.Detail
				info.Version, info.Apps = st.Version, st.Apps
				info.Exec, info.SHA256 = st.Exec, st.SHA256
				info.Name = firstNonEmpty(spec.Name, st.Name)
			} else {
				info.State, info.Detail = pluginhost.StateDisabled, "宿主未加载"
			}
		}
		out = append(out, info)
	}

	// 宿主发现到、但清单里没声明的来源。宿主的顺序按 id 排好，直接沿用。
	for _, st := range discovered {
		if declared[st.ID] {
			continue
		}
		out = append(out, SourceInfo{
			ID:       st.ID,
			Name:     firstNonEmpty(st.Name, st.ID),
			State:    st.State,
			Detail:   st.Detail,
			Version:  st.Version,
			Apps:     st.Apps,
			Exec:     st.Exec,
			SHA256:   st.SHA256,
			Enabled:  true,
			Declared: false,
		})
	}
	return out
}

// TrustSource 记录某个来源的信任哈希，并立刻按新清单重载。
//
// 这是「插件等于任意代码执行」那条线：哈希只由调用方给出（界面上是用户核对过
// 路径与摘要之后确认的），这里只负责写下来并让宿主认得。
func (c *Controller) TrustSource(ctx context.Context, id, sha string) error {
	if c.afs == nil {
		return fmt.Errorf("清单未加载，无法记录信任")
	}
	c.afs.SetSourceTrust(id, sha)
	if err := c.afs.Save(); err != nil {
		// 内存里已经改了，但没落盘：下次启动会退回未信任，所以这里必须报错。
		return fmt.Errorf("保存清单: %w", err)
	}
	return c.ReloadPlugins(ctx)
}

// SetSourceEnabled 改写某个来源的启用状态并立刻生效。
func (c *Controller) SetSourceEnabled(ctx context.Context, id string, enabled bool) error {
	sources := c.manifestSources()
	for i := range sources {
		if sources[i].ID != id {
			continue
		}
		v := enabled
		c.afs.Sources[i].Enabled = &v
		if err := c.afs.Save(); err != nil {
			return fmt.Errorf("保存清单: %w", err)
		}
		return c.ReloadPlugins(ctx)
	}
	return fmt.Errorf("来源 %s 不在清单里（还未登记）", id)
}

// ReloadPlugins 按当前清单重建插件来源。
//
// 必须用 Reconfigure 而不是 Load：Load 按构造时捕获的条目工作，看不见刚写进清单的
// 信任哈希与启停开关 —— 改了清单再「重载」等于没重载。
func (c *Controller) ReloadPlugins(ctx context.Context) error {
	if c.host == nil {
		return fmt.Errorf("插件宿主未启用")
	}
	return c.host.Reconfigure(ctx, c.manifestSources())
}

// ReloadSources 重读磁盘上的清单，再重建插件来源。
//
// 文档让用户手改 apps.yaml 写 trust，只重建内存里的那份快照的话，手改的内容永远
// 进不来。只采纳 Sources：清单里的 Apps 归「概览」页管，整份替换会让那边已加载的
// 列表与界面对不上。文件不存在时保持内存里的内容。
func (c *Controller) ReloadSources(ctx context.Context) error {
	if c.afs == nil {
		return fmt.Errorf("清单未加载")
	}
	if c.afs.Path != "" {
		if _, err := os.Stat(c.afs.Path); err == nil {
			fresh, err := apps.Load(c.afs.Path, apps.WithInstallRoot(c.afs.InstallRoot))
			if err != nil {
				return fmt.Errorf("重读清单: %w", err)
			}
			c.afs.Sources = fresh.Sources
		}
	}
	if c.host == nil {
		// 插件宿主没启用不算失败：清单已经重读了，只是没有来源要重建。
		// 以前这里把「插件宿主未启用」抛成错误，于是没装插件的机器上「改完清单按
		// 刷新」直接弹错，而用户想做的事其实已经做完了。
		return nil
	}
	return c.host.Reconfigure(ctx, c.manifestSources())
}

// PluginConfig 返回某个插件声明的配置项及其当前值。
func (c *Controller) PluginConfig(ctx context.Context, id string) ([]PluginField, error) {
	if c.host == nil {
		return nil, nil
	}
	schema, err := c.host.ConfigSchema(ctx, id)
	if err != nil {
		return nil, err
	}
	spec, ok := c.sourceSpec(id)
	if !ok {
		return nil, fmt.Errorf("来源 %s 不在清单里", id)
	}
	out := make([]PluginField, 0, len(schema.Fields))
	for _, f := range schema.Fields {
		row := PluginField{
			Key:      f.Key,
			Label:    firstNonEmpty(f.Label, f.Key),
			Default:  f.Default,
			Help:     f.Help,
			Required: f.Required,
			Secret:   f.Secret,
		}
		if v, ok := spec.Config[f.Key]; ok {
			row.Value = v
		} else {
			row.Value = f.Default
		}
		out = append(out, row)
	}
	return out, nil
}

// SetPluginConfig 改写插件的一项配置并热应用给它。空值表示清掉显式值、回到默认。
//
// 写盘失败必须报错（配置没存下来）；插件当场拒绝也报错，但清单已经改了，重载后
// 仍会生效。
func (c *Controller) SetPluginConfig(ctx context.Context, id, key, value string) error {
	if c.afs == nil {
		return fmt.Errorf("清单未加载")
	}
	for i := range c.afs.Sources {
		if c.afs.Sources[i].ID != id {
			continue
		}
		if c.afs.Sources[i].Config == nil {
			c.afs.Sources[i].Config = map[string]string{}
		}
		if value == "" {
			delete(c.afs.Sources[i].Config, key)
		} else {
			c.afs.Sources[i].Config[key] = value
		}
		if err := c.afs.Save(); err != nil {
			return fmt.Errorf("保存清单: %w", err)
		}
		if c.host == nil {
			return nil
		}
		if err := c.host.Configure(ctx, id, upkitplugin.NewConfig(c.afs.Sources[i].Config)); err != nil {
			return fmt.Errorf("插件未接受该配置: %w", err)
		}
		return nil
	}
	return fmt.Errorf("来源 %s 已不存在", id)
}

// manifestSources 返回清单里的来源声明（清单未加载时为空）。
func (c *Controller) manifestSources() []apps.SourceSpec {
	if c.afs == nil {
		return nil
	}
	return c.afs.Sources
}

// sourceSpec 取清单里某条来源的声明。
func (c *Controller) sourceSpec(id string) (apps.SourceSpec, bool) {
	for _, s := range c.manifestSources() {
		if s.ID == id {
			return s, true
		}
	}
	return apps.SourceSpec{}, false
}

// firstNonEmpty 返回第一个非空（去空白后）的字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
