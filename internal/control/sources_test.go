package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/pluginhost"
)

// addDiscoveredPlugin 往插件目录里再放一个「手工放进去、清单里没声明」的插件。
//
// 这是宿主会扫到、但清单里没有承载的那种来源：信任决定必须能落到清单里，否则它
// 永远起不来。返回它的 id 与摘要。
func addDiscoveredPlugin(t *testing.T, f *pluginFixture, id string) (string, string) {
	t.Helper()
	execPath := filepath.Join(f.pluginDir, id+".exe")
	if _, err := fsutil.CopyFile(f.execPath, execPath); err != nil {
		t.Fatalf("复制插件: %v", err)
	}
	sha, err := pluginhost.HashFile(execPath)
	if err != nil {
		t.Fatalf("计算摘要: %v", err)
	}
	if _, err := pluginhost.SaveManifest(f.pluginDir, pluginhost.Manifest{
		ID: id, Name: "手工插件", Exec: id + ".exe", Mode: "catalog",
		Version: "0.1.0", SHA256: sha,
	}); err != nil {
		t.Fatalf("写插件描述: %v", err)
	}
	return execPath, sha
}

// 来源汇总要把「清单声明」与「宿主发现」合成一行，并分清两者：
// 声明过但没加载、声明过但被停用、宿主发现但清单里没有 —— 三种状态界面都要能区分。
func TestSourcesMergesDeclaredAndDiscovered(t *testing.T) {
	f := newPluginControl(t, nil)

	// 宿主发现、但清单里没声明的插件。宿主只在重扫时才会看见它 —— 这一步正是
	// 界面上「刷新」做的事。
	addDiscoveredPlugin(t, f, "side-loaded")
	if err := f.host.Reconfigure(context.Background(), f.afs.Sources); err != nil {
		t.Fatalf("重扫插件目录: %v", err)
	}

	// 清单里声明、但宿主这一轮没拿到（记完清单再塞进去，宿主仍是旧的一份）。
	f.afs.Sources = append(f.afs.Sources, apps.SourceSpec{
		ID: "stale-one", Name: "没加载的来源", Kind: apps.KindPlugin,
	})

	infos := f.ctrl.Sources()
	byID := map[string]SourceInfo{}
	for _, s := range infos {
		byID[s.ID] = s
	}

	live, ok := byID["example-static"]
	if !ok {
		t.Fatalf("应有 example-static: %+v", infos)
	}
	if !live.Declared || !live.Enabled {
		t.Fatalf("声明并启用的来源应如实上报: %+v", live)
	}
	if live.State != pluginhost.StateOK {
		t.Fatalf("插件已在跑，状态应为 ok: %+v", live)
	}
	if live.Version == "" || live.Apps == 0 {
		t.Fatalf("应带上插件自报的版本与软件数: %+v", live)
	}
	if live.Exec != f.execPath || live.SHA256 != f.sha {
		t.Fatalf("应带上可执行文件与摘要（未信任来源靠它变可信）: %+v", live)
	}

	if got := byID["side-loaded"]; got.Declared {
		t.Fatalf("宿主发现的来源不该算「声明过」: %+v", got)
	}
	if got := byID["side-loaded"]; !got.Enabled {
		t.Fatalf("自动发现的来源默认是启用的: %+v", got)
	}
	if got := byID["side-loaded"]; got.Name != "手工插件" {
		t.Fatalf("名字应回退到描述文件里的名字: %+v", got)
	}
	if got := byID["side-loaded"]; got.SHA256 == "" || got.Exec == "" {
		t.Fatalf("发现的来源也要给路径与摘要，否则用户没法核对: %+v", got)
	}

	if got := byID["stale-one"]; got.State != pluginhost.StateDisabled || !strings.Contains(got.Detail, "宿主未加载") {
		t.Fatalf("声明了但没加载的来源要说清楚: %+v", got)
	}

	// 停用之后状态由清单决定，而不是宿主说了算（来源可能仍在跑）。
	f.afs.Sources[0].Enabled = boolPtr(false)
	infos = f.ctrl.Sources()
	for _, s := range infos {
		if s.ID == "example-static" {
			if s.State != pluginhost.StateDisabled || s.Enabled {
				t.Fatalf("停用状态不对: %+v", s)
			}
			if !strings.Contains(s.Detail, "停用") || s.State != pluginhost.StateDisabled {
				t.Fatalf("停用原因要说清楚: %+v", s)
			}
		}
	}
	// 名字回退：清单里没写名字时用宿主/描述文件里的。
	f.afs.Sources[0].Enabled = boolPtr(true)
	f.afs.Sources[0].Name = ""
	infos = f.ctrl.Sources()
	if infos[0].Name != "示例静态源" {
		t.Fatalf("名字应回退到插件自报的名字: %+v", infos[0])
	}
}

// 启停来源要立刻生效并落盘：只改内存的话下次启动就退回去了。
func TestSetSourceEnabled(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	if err := f.ctrl.SetSourceEnabled(ctx, "example-static", false); err != nil {
		t.Fatalf("SetSourceEnabled: %v", err)
	}
	reloaded, err := apps.Load(f.afs.Path)
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	if reloaded.Sources[0].EnabledValue() {
		t.Fatal("停用状态没落盘")
	}
	// 宿主那边也要跟着停：状态由清单驱动。
	for _, s := range f.ctrl.Sources() {
		if s.ID == "example-static" && s.State != pluginhost.StateDisabled {
			t.Fatalf("停用后宿主状态应改为 disabled: %+v", s)
		}
	}

	if err := f.ctrl.SetSourceEnabled(ctx, "example-static", true); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	for _, s := range f.ctrl.Sources() {
		if s.ID == "example-static" && s.State != pluginhost.StateOK {
			t.Fatalf("重新启用后插件应又跑起来: %+v", s)
		}
	}

	if err := f.ctrl.SetSourceEnabled(ctx, "nope", true); err == nil ||
		!strings.Contains(err.Error(), "不在清单里") {
		t.Fatalf("未登记的来源应报错: %v", err)
	}
}

// 记录信任：把「手工放进插件目录的插件」变成可加载的来源。
//
// 信任只由调用方给出（界面上是用户核对过路径与摘要之后确认的），这里负责写下来
// 并让宿主认得 —— 写盘失败必须报错，否则用户会以为信任已经生效。
func TestTrustSourceRecordsAndReloads(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()
	_, sha := addDiscoveredPlugin(t, f, "side-loaded")
	if err := f.host.Reconfigure(ctx, f.afs.Sources); err != nil {
		t.Fatalf("重扫插件目录: %v", err)
	}

	// 还没信任时，这个来源在宿主里是拿不到可执行权限的。
	var before SourceInfo
	for _, s := range f.ctrl.Sources() {
		if s.ID == "side-loaded" {
			before = s
		}
	}
	if before.SHA256 != sha {
		t.Fatalf("发现的来源应报出摘要供核对: %+v", before)
	}

	if err := f.ctrl.TrustSource(ctx, "side-loaded", sha); err != nil {
		t.Fatalf("TrustSource: %v", err)
	}
	reloaded, err := apps.Load(f.afs.Path)
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	found := false
	for _, s := range reloaded.Sources {
		if s.ID == "side-loaded" {
			found = true
			if s.Trust != sha {
				t.Fatalf("信任没落盘: %+v", s)
			}
			if s.Kind != apps.KindPlugin {
				t.Fatalf("补出来的声明要带 kind: %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("信任应补出一条来源声明: %+v", reloaded.Sources)
	}
	// 宿主重载之后，这个来源应当已经能起来。
	for _, s := range f.ctrl.Sources() {
		if s.ID == "side-loaded" && s.State != pluginhost.StateOK {
			t.Fatalf("信任后应能加载: %+v", s)
		}
	}

	// 清单写不进去时必须报错（内存改了、磁盘没改，下次启动就退回未信任）。
	bad := newPluginControl(t, nil)
	_ = os.Remove(bad.afs.Path)
	if err := os.MkdirAll(bad.afs.Path, 0o755); err != nil {
		t.Fatalf("把清单路径变成目录: %v", err)
	}
	if err := bad.ctrl.TrustSource(context.Background(), "example-static", bad.sha); err == nil ||
		!strings.Contains(err.Error(), "保存清单") {
		t.Fatalf("写盘失败应报错: %v", err)
	}
}

// 手改 apps.yaml 之后「刷新」要能读进来 —— 文档让用户手写 trust，只重载内存里的
// 快照等于没读。
func TestReloadSourcesPicksUpHandEdits(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	// 手改：把来源停掉并改掉信任摘要。
	f.afs.Sources[0].Enabled = boolPtr(false)
	if err := f.afs.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 内存里再改回去，模拟「界面里的旧快照」。
	f.afs.Sources[0].Enabled = boolPtr(true)
	if err := f.ctrl.ReloadSources(ctx); err != nil {
		t.Fatalf("ReloadSources: %v", err)
	}
	if f.afs.Sources[0].EnabledValue() {
		t.Fatal("刷新应采纳磁盘上的启停状态")
	}

	// 文件不存在时保持内存里的内容（用户可能把它删了准备重建）。
	if err := os.Remove(f.afs.Path); err != nil {
		t.Fatalf("删清单: %v", err)
	}
	f.afs.Sources[0].Enabled = boolPtr(true)
	if err := f.ctrl.ReloadSources(ctx); err != nil {
		t.Fatalf("文件不存在时不该报错: %v", err)
	}
	if !f.afs.Sources[0].EnabledValue() {
		t.Fatal("文件不存在时应保持内存里的内容")
	}

	// 文件写坏了必须报错，不能静默用旧内容继续跑。
	bad := newPluginControl(t, nil)
	writeText(t, bad.afs.Path, "sources: [oops\n")
	if err := bad.ctrl.ReloadSources(ctx); err == nil ||
		!strings.Contains(err.Error(), "重读清单") {
		t.Fatalf("清单坏了应报错: %v", err)
	}
}

// 插件配置：读出来的是「当前值（没有则用默认）」，写下去要既能落盘又能热应用。
func TestPluginConfigRoundTrip(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	fields, err := f.ctrl.PluginConfig(ctx, "example-static")
	if err != nil {
		t.Fatalf("PluginConfig: %v", err)
	}
	if len(fields) == 0 {
		t.Fatal("示例插件声明了配置项")
	}
	var target PluginField
	for _, fld := range fields {
		if fld.Value == "" {
			t.Fatalf("没有显式值时也要给出默认值: %+v", fld)
		}
		if fld.Key == "download_base" {
			target = fld
		}
	}
	if target.Key == "" || !target.Required || target.Label == "" {
		t.Fatalf("字段元信息不完整: %+v", target)
	}

	// 写一项配置：清单里要落盘，插件要当场接受。
	if err := f.ctrl.SetPluginConfig(ctx, "example-static", "channel", "beta"); err != nil {
		t.Fatalf("SetPluginConfig: %v", err)
	}
	reloaded, err := apps.Load(f.afs.Path)
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	if got := reloaded.Sources[0].Config["channel"]; got != "beta" {
		t.Fatalf("配置没落盘: %+v", reloaded.Sources[0].Config)
	}
	fields, err = f.ctrl.PluginConfig(ctx, "example-static")
	if err != nil {
		t.Fatalf("PluginConfig: %v", err)
	}
	for _, fld := range fields {
		if fld.Key == "channel" && fld.Value != "beta" {
			t.Fatalf("读回来的值不对: %+v", fld)
		}
	}

	// 空值表示「清掉显式值、回到默认」。
	if err := f.ctrl.SetPluginConfig(ctx, "example-static", "channel", ""); err != nil {
		t.Fatalf("清空配置: %v", err)
	}
	reloaded, err = apps.Load(f.afs.Path)
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	if _, ok := reloaded.Sources[0].Config["channel"]; ok {
		t.Fatalf("空值应删掉这一项: %+v", reloaded.Sources[0].Config)
	}

	// 来源不存在：报错并点明 id。
	if err := f.ctrl.SetPluginConfig(ctx, "nope", "k", "v"); err == nil ||
		!strings.Contains(err.Error(), "nope") {
		t.Fatalf("未知来源应报错: %v", err)
	}
	if _, err := f.ctrl.PluginConfig(ctx, "nope"); err == nil {
		t.Fatal("未知来源读配置应报错")
	}

	// 清单写不进去时必须报错（配置在内存里已经改了，磁盘没改）。
	broken := newPluginControl(t, nil)
	_ = os.Remove(broken.afs.Path)
	if err := os.MkdirAll(broken.afs.Path, 0o755); err != nil {
		t.Fatalf("把清单路径变成目录: %v", err)
	}
	if err := broken.ctrl.SetPluginConfig(context.Background(), "example-static", "channel", "beta"); err == nil ||
		!strings.Contains(err.Error(), "保存清单") {
		t.Fatalf("写盘失败应报错: %v", err)
	}
}

// 宿主没启用时「重建来源」要明确报错，而「重读清单」不算失败：
// 清单已经读进来了，只是没有来源要重建 —— 这个区别曾经让没装插件的机器上
// 「改完清单按刷新」直接弹错。
func TestReloadWithoutHost(t *testing.T) {
	f := newPluginControl(t, func(f *pluginFixture) { f.host = nil })
	ctx := context.Background()

	// 控制层仍然要能装配（宿主字段置空后走的就是「没装插件」的路径）。
	ctrl, err := New(Options{Settings: f.set, Apps: f.afs, Feed: f.store, Version: "1.5.0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := ctrl.ReloadPlugins(ctx); err == nil || !strings.Contains(err.Error(), "插件宿主未启用") {
		t.Fatalf("宿主未启用时重建来源应报错: %v", err)
	}
	if err := ctrl.ReloadSources(ctx); err != nil {
		t.Fatalf("宿主未启用不该让「重读清单」失败: %v", err)
	}
	// 没有宿主时插件配置也读不回来，但不该报错。
	if fields, err := ctrl.PluginConfig(ctx, "example-static"); err != nil || fields != nil {
		t.Fatalf("没有宿主时应返回空: %+v %v", fields, err)
	}
}
