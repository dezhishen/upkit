package tui

import (
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/pluginfeed"
)

// 停用的软件必须留在列表里，而且要能一眼看出它被停用了。
//
// 以前清单层直接把停用的软件过滤掉：按了空格之后软件就从界面上消失，既看不到
// 也启不回来 —— 用户只能去改 apps.yaml。
func TestDisabledAppStaysVisible(t *testing.T) {
	off := &engine.App{
		Ref:  core.AppRef{ID: "off", Name: "Off", Disabled: true},
		Note: "已停用（不参与检查与更新）",
	}
	m := ready(t, off, demoApp(core.ActionUpdate))

	out := content(m)
	if !strings.Contains(out, "Off（停用）") {
		t.Fatalf("停用的软件应带「（停用）」标记：\n%s", out)
	}
	if !strings.Contains(out, "停用 1") {
		t.Fatalf("右上角计数应说明有几个停用的：\n%s", out)
	}
	// 选中它按空格应当能直接启用（不是「找不到这个软件」）。
	m.cursor = 0
	if got := m.current(); got == nil || got.Ref.ID != "off" {
		t.Fatalf("光标应停在停用的那一行：%+v", got)
	}
}

// 按 h 收起来 / 再按 h 放出来：这就是「筛选」。
func TestHideDisabledFilter(t *testing.T) {
	off := &engine.App{Ref: core.AppRef{ID: "off", Name: "Off", Disabled: true}}
	m := ready(t, off, demoApp(core.ActionUpdate))
	m.tab = tabOverview

	if got := len(m.shown()); got != 2 {
		t.Fatalf("默认应显示全部（含停用），实际 %d", got)
	}
	m, _ = updateCmd(t, m, key('h'))
	if !m.hideDisabled || len(m.shown()) != 1 {
		t.Fatalf("按 h 后应只剩启用的：hide=%v shown=%d", m.hideDisabled, len(m.shown()))
	}
	if m.hiddenDisabled() != 1 {
		t.Fatalf("应报告隐藏了 1 个：%d", m.hiddenDisabled())
	}
	if out := content(m); strings.Contains(out, "Off") {
		t.Fatalf("隐藏后不该再出现那一行：\n%s", out)
	}
	if !strings.Contains(m.status, "已隐藏 1 个停用的软件") {
		t.Fatalf("底栏应说清隐藏了几个：%q", m.status)
	}

	m, _ = updateCmd(t, m, key('h'))
	if m.hideDisabled || len(m.shown()) != 2 {
		t.Fatalf("再按一次应放出来：hide=%v shown=%d", m.hideDisabled, len(m.shown()))
	}
}

// 筛选之后光标要跟着收：否则光标会停在看不见的行上，按回车操作的是「空气」。
func TestHideDisabledClampsCursor(t *testing.T) {
	off := &engine.App{Ref: core.AppRef{ID: "off", Name: "Off", Disabled: true}}
	m := ready(t, demoApp(core.ActionUpdate), off)
	m.cursor = 1 // 停在停用的那一行

	m, _ = updateCmd(t, m, key('h'))
	if m.cursor != 0 {
		t.Fatalf("光标应被收回可见范围，实际 %d", m.cursor)
	}
	if got := m.current(); got == nil || got.Ref.Disabled {
		t.Fatalf("光标不该停在停用的行上：%+v", got)
	}
}

// 停用的软件不进批量更新的清单：它「不参与」，不是「失败」。
func TestPendingIDsSkipDisabled(t *testing.T) {
	off := &engine.App{Ref: core.AppRef{ID: "off", Name: "Off", Disabled: true}, Action: core.ActionUpdate}
	on := demoApp(core.ActionUpdate)
	m := ready(t, off, on)

	ids := m.pendingIDs()
	if len(ids) != 1 || ids[0] != "demo" {
		t.Fatalf("批量更新只该包含启用的软件：%v", ids)
	}
	// 检查进度同理：停用的不发检查事件，算进总数会让进度永远差一个。
	if got := m.checkableIDs(); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("待检查清单只该包含启用的软件：%v", got)
	}
}

// 订阅的启用/停用状态要显示出来（按空格改了却看不出来，等于没法确认）。
func TestSubscriptionStateVisible(t *testing.T) {
	dir := t.TempDir()
	store, err := pluginfeed.LoadStore(dir + "/feed.yaml")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := store.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	url := "https://example.com/feed.yaml"
	if _, err := store.AddSubscription(url); err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}

	m := newTestModelWith(t, func(o *control.Options) { o.Feed = store })
	m = update(t, m, key('6'))
	m.tab = tabSources

	if out := content(m); !strings.Contains(out, "已启用") {
		t.Fatalf("订阅应显示已启用：\n%s", out)
	}

	// 按空格停用：状态要跟着变。
	m, _ = updateCmd(t, m, key(' '))
	if out := content(m); !strings.Contains(out, "已停用") {
		t.Fatalf("停用后应显示已停用：\n%s", out)
	}
	// 进订阅详情也能看到（那里看不到外面那行的状态）。
	m.feedFor = url
	if out := content(m); !strings.Contains(out, "已停用") {
		t.Fatalf("订阅详情也应标出已停用：\n%s", out)
	}
}

// 对停用的软件按「更新」「计划」要当场说清楚怎么办，而不是等引擎报错 ——
// 那时用户已经点过确认框，只会觉得「点了没用 / 报错看不懂」。
func TestUpdateDisabledAppGivesClearHint(t *testing.T) {
	off := &engine.App{
		Ref:    core.AppRef{ID: "off", Name: "Off", Disabled: true},
		Action: core.ActionUpdate,
	}
	m := ready(t, off)
	m.tab = tabOverview

	m, cmd := updateCmd(t, m, key('u'))
	if cmd != nil || m.confirm != nil {
		t.Fatal("停用的软件不该进入确认流程")
	}
	if !strings.Contains(m.status, "已停用") || !strings.Contains(m.status, "空格") {
		t.Fatalf("应提示先启用：%q", m.status)
	}

	m, cmd = updateCmd(t, m, key('p'))
	if cmd != nil {
		t.Fatal("停用的软件不该去生成计划")
	}
	if !strings.Contains(m.status, "已停用") {
		t.Fatalf("应提示先启用：%q", m.status)
	}
}
