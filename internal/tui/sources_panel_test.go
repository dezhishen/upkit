package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
)

// 来源面板应当列出清单里声明的插件来源。
func TestSourcesPanelRendersPluginRows(t *testing.T) {
	m := newTestModel(t)
	m.afs.Sources = []apps.SourceSpec{{
		ID:     "corp-index",
		Name:   "企业源",
		Kind:   "plugin",
		Config: map[string]string{"endpoint": "https://x"},
	}}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))

	view := content(m)
	if !strings.Contains(view, "企业源") {
		t.Fatalf("来源面板未显示插件名称:\n%s", view)
	}
	if !strings.Contains(view, "订阅") {
		t.Fatalf("来源面板应包含订阅分组:\n%s", view)
	}
}

// 空来源时给出引导而不是空白。
func TestSourcesPanelEmptyState(t *testing.T) {
	m := newTestModel(t)
	out := m.viewSources(80, 20)
	if !strings.Contains(out, "还没有任何插件来源") {
		t.Fatalf("空状态提示缺失:\n%s", out)
	}
}

// 插件配置的入口就在插件条目上：按 c 进入该插件的配置视图。
func TestSourcesPanelOpensPluginConfig(t *testing.T) {
	m := newTestModel(t)
	m.afs.Sources = []apps.SourceSpec{{ID: "corp-index", Name: "企业源", Kind: "plugin"}}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('c'))

	if m.cfgFor != "corp-index" {
		t.Fatalf("应进入 corp-index 的配置视图，实际 cfgFor=%q", m.cfgFor)
	}
	// 没有宿主（插件未运行）时应说明「没有声明可配置项」，而不是 panic 或空白。
	out := m.viewSources(80, 20)
	if !strings.Contains(out, "没有声明可配置项") {
		t.Fatalf("配置视图提示缺失:\n%s", out)
	}
	// esc 返回列表。
	m = update(t, m, key(tea.KeyEsc))
	if m.cfgFor != "" {
		t.Fatalf("esc 应返回列表，实际 cfgFor=%q", m.cfgFor)
	}
}

// 首次添加订阅必须先确认启用订阅功能，再输入地址（默认不订阅）。
func TestSubscriptionAuthorizationFlow(t *testing.T) {
	m := newTestModel(t)
	store, err := pluginfeed.LoadStore(filepath.Join(t.TempDir(), pluginfeed.FileName))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	m.feed = store

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('a'))

	if store.FeatureAuthorized() {
		t.Fatal("订阅功能默认必须是关闭的")
	}
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "启用插件订阅") {
		t.Fatalf("首次添加订阅应先要求启用订阅功能，实际 confirm=%+v", m.confirm)
	}

	// 确认启用 → 应弹出地址输入框。
	m = update(t, m, key('y'))
	if !store.FeatureAuthorized() {
		t.Fatal("确认后订阅功能应已授权")
	}
	if m.prompt == nil {
		t.Fatal("授权后应弹出订阅地址输入框")
	}
}

// 内置官方源：入口存在，且同样要走授权流程（内置不绕过任何确认）。
func TestBuiltinFeedEntry(t *testing.T) {
	m := newTestModel(t)
	store, err := pluginfeed.LoadStore(filepath.Join(t.TempDir(), pluginfeed.FileName))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	m.feed = store

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('o'))

	// 订阅功能默认关闭：先确认启用。
	if m.confirm == nil {
		t.Fatal("首次使用官方源应先要求启用订阅功能")
	}
	if store.FeatureAuthorized() {
		t.Fatal("确认之前不应已授权")
	}

	m = update(t, m, key('y'))
	if !store.FeatureAuthorized() {
		t.Fatal("确认后应已授权")
	}
	if m.prompt == nil {
		t.Fatal("授权后应弹出订阅地址输入框")
	}
	if m.prompt.Input.Value() != pluginfeed.BuiltinFeedURL {
		t.Fatalf("应预填官方源地址，实际 %q", m.prompt.Input.Value())
	}

	// 已添加过时不再重复弹窗。
	if _, err := store.AddSubscription(pluginfeed.BuiltinFeedURL); err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}
	m.prompt = nil
	m = update(t, m, key('o'))
	if m.prompt != nil {
		t.Fatal("官方源已在列表中时不应再次弹窗")
	}
	if !strings.Contains(m.status, "已在订阅列表中") {
		t.Fatalf("应提示官方源已存在，实际 status=%q", m.status)
	}
}
func TestRemoveSubscriptionOnlyAppliesToSubscriptionRows(t *testing.T) {
	m := newTestModel(t)
	m.afs.Sources = []apps.SourceSpec{{ID: "corp-index", Name: "企业源", Kind: "plugin"}}
	store, err := pluginfeed.LoadStore(filepath.Join(t.TempDir(), pluginfeed.FileName))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	m.feed = store

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('d'))

	if m.confirm != nil {
		t.Fatalf("选中插件行时不应进入删除确认: %+v", m.confirm)
	}
	if !strings.Contains(m.status, "只能删除订阅") {
		t.Fatalf("应提示只能删除订阅，实际 status=%q", m.status)
	}
}

// 切换来源启停会写回清单（而不是只改内存）。
func TestToggleSourceWritesBackToManifest(t *testing.T) {
	m := newTestModel(t)
	path := filepath.Join(t.TempDir(), "apps.yaml")
	m.afs.Path = path
	m.afs.Sources = []apps.SourceSpec{{ID: "corp-index", Name: "企业源", Kind: "plugin"}}

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	// 空格：真实按键是 KeyRunes，字符串为 " "（bubbletea 也可能给 KeySpace，两种都要认）。
	m = update(t, m, key(' '))

	if m.afs.Sources[0].EnabledValue() {
		t.Fatal("space 应停用该来源")
	}
	reloaded, err := apps.Load(path)
	if err != nil {
		t.Fatalf("重新加载清单: %v", err)
	}
	if len(reloaded.Sources) != 1 || reloaded.Sources[0].EnabledValue() {
		t.Fatalf("停用状态没有落盘: %+v", reloaded.Sources)
	}
}

// newTestHost 造一个只会「发现」、不会真的启动插件的宿主。
//
// 未信任的插件在宿主里是提前返回的，所以这里不需要真能跑起来的可执行文件：一个
// 立刻退出的脚本就够，宿主算完哈希就把它记为未信任。
func newTestHost(t *testing.T, dir string, ids ...string) *pluginhost.Manager {
	t.Helper()
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(dir, id+".bin"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("写插件文件: %v", err)
		}
		man := "id: " + id + "\nexec: " + id + ".bin\n"
		if err := os.WriteFile(filepath.Join(dir, id+".plugin.yaml"), []byte(man), 0o644); err != nil {
			t.Fatalf("写插件描述: %v", err)
		}
	}
	host, err := pluginhost.NewManager(pluginhost.Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	host.Load(context.Background())
	return host
}

// 手工放进 plugin/ 的插件：清单里没有，但宿主发现了，面板必须列出来。
//
// 这是最需要操作的一种状态 —— 未信任、因此没启动。如果连行都不出现，用户就只剩下
// 「去翻日志」和「去改 YAML」，而面板的空状态恰好在推荐用户手工放置插件。
func TestSourcesPanelListsDiscoveredSources(t *testing.T) {
	m := newTestModel(t)
	m.host = newTestHost(t, t.TempDir(), "corp-index")

	rows := m.sourceRows()
	if len(rows) != 1 || rows[0].spec.ID != "corp-index" {
		t.Fatalf("插件目录里发现到的来源应当出现在列表里，实际 %d 行: %+v", len(rows), rows)
	}
	if rows[0].state != pluginhost.StateUntrusted {
		t.Fatalf("没记信任的插件应为未信任，实际 %q", rows[0].state)
	}
	if rows[0].declared {
		t.Error("该来源不在清单里，declared 应为 false")
	}
	if rows[0].sha == "" {
		t.Error("未信任的来源应当带上宿主算出的 sha256，否则界面无法让人核对")
	}

	out := m.viewSources(100, 20)
	if !strings.Contains(out, "corp-index") {
		t.Fatalf("面板没有渲染这个来源:\n%s", out)
	}
	if !strings.Contains(out, "按 t 信任") {
		t.Fatalf("未信任状态应当说明按哪个键:\n%s", out)
	}
}

// 按 t 信任：弹出确认框（含路径与哈希）→ 写进清单 → 立刻生效。
func TestTrustSourceWritesManifest(t *testing.T) {
	m := newTestModel(t)
	m.host = newTestHost(t, t.TempDir(), "corp-index")
	path := filepath.Join(t.TempDir(), "apps.yaml")
	m.afs.Path = path
	wantSHA := m.sourceRows()[0].sha

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('t'))

	if m.confirm == nil {
		t.Fatal("t 应当先弹出确认框")
	}
	// 确认框必须摆出可执行文件与哈希：信任比的是「这个文件是不是我要的那个」，
	// 只给一个 id 没法判断。
	if !strings.Contains(m.confirm.Message, wantSHA) {
		t.Fatalf("确认框应显示 sha256 以便核对:\n%s", m.confirm.Message)
	}
	if !strings.Contains(m.confirm.Message, "corp-index.bin") {
		t.Fatalf("确认框应显示实际可执行文件路径:\n%s", m.confirm.Message)
	}

	m = update(t, m, key('y'))

	if len(m.afs.Sources) != 1 {
		t.Fatalf("信任后清单应有 1 条来源，实际 %+v", m.afs.Sources)
	}
	if m.afs.Sources[0].Trust != wantSHA {
		t.Fatalf("信任哈希没写进内存清单: %+v", m.afs.Sources[0])
	}
	if m.afs.Sources[0].Kind != apps.KindPlugin {
		t.Errorf("补出来的条目 kind 应为 plugin，实际 %q", m.afs.Sources[0].Kind)
	}

	// 落盘：重开进程后信任还在，否则用户每次启动都要重新点一遍。
	reloaded, err := apps.Load(path)
	if err != nil {
		t.Fatalf("重新加载清单: %v", err)
	}
	if len(reloaded.Sources) != 1 || reloaded.Sources[0].Trust != wantSHA {
		t.Fatalf("信任没有落盘: %+v", reloaded.Sources)
	}

	// 生效：信任之后它不再是「未信任」。
	if got := m.sourceRows()[0].state; got == pluginhost.StateUntrusted {
		t.Fatalf("信任之后不应仍是未信任")
	}
}

// 清单里没有的来源：启停要提示先信任，而不是静默无反应。
func TestToggleDiscoveredSourceExplainsTrust(t *testing.T) {
	m := newTestModel(t)
	m.host = newTestHost(t, t.TempDir(), "corp-index")

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key(' '))

	if !strings.Contains(m.status, "按 t 信任") {
		t.Fatalf("应提示先信任，实际 status=%q", m.status)
	}
}

// 提示行必须落在面板内宽以内：超宽会被 Frame 折行，把最后几行来源挤出可视区。
func TestSourcesHintFitsPanelWidth(t *testing.T) {
	const hint = "enter/c 进入   t 信任   o 官方源   a 加订阅   d 删除   space 启停   r 重载"
	m := newTestModel(t)
	m.afs.Sources = []apps.SourceSpec{{ID: "corp-index", Name: "企业源", Kind: "plugin"}}

	out := m.viewSources(80, 20)
	// 折行的断点正好落在提示里，Contains 就会失败 —— 这是最直接的不折行断言。
	if !strings.Contains(out, hint) {
		t.Fatalf("80 列下提示行被折行（或内容变了）:\n%s", out)
	}
}

// 手改 apps.yaml 之后按 r：必须真的重读磁盘。
//
// 文档让用户把 trust 写进清单，而按 r 若只重建内存里的那份快照，手改的内容永远进
// 不来，这条路就等于没通。
func TestReloadSourcesReadsManifestFromDisk(t *testing.T) {
	m := newTestModel(t)
	path := filepath.Join(t.TempDir(), "apps.yaml")
	m.afs.Path = path

	// 磁盘上有、内存里没有的一条来源。
	seed := apps.Default()
	seed.Path = path
	seed.Sources = []apps.SourceSpec{{ID: "corp-index", Kind: apps.KindPlugin, Trust: "deadbeef"}}
	if err := seed.Save(); err != nil {
		t.Fatalf("写清单: %v", err)
	}
	m.host = newTestHost(t, t.TempDir())

	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('6'))
	m = update(t, m, key('r'))

	if len(m.afs.Sources) != 1 || m.afs.Sources[0].ID != "corp-index" {
		t.Fatalf("r 应当把磁盘上的来源读进来，实际 %+v", m.afs.Sources)
	}
	if m.afs.Sources[0].Trust != "deadbeef" {
		t.Fatalf("手工写入的 trust 没有被读进来: %+v", m.afs.Sources[0])
	}
}
