package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginfeed"
)

func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

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

	view := m.View()
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
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
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
