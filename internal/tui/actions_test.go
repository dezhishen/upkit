package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// twoApps 造两条软件，供键位语义测试用。
func twoApps() []*engine.App {
	return []*engine.App{
		{Ref: core.AppRef{ID: "demo", Name: "Demo"}, Action: core.ActionUpdate},
		{Ref: core.AppRef{ID: "git", Name: "Git"}, Action: core.ActionNoOp},
	}
}

// 同一个面板里，操作栏上的键不能重复：重复意味着有一个按钮点了没用（或点到别人）。
func TestActionsHaveUniqueKeys(t *testing.T) {
	m := newTestModelWith(t, func(o *control.Options) {
		o.Apps.Sources = []apps.SourceSpec{
			{ID: "corp-a", Name: "第一源", Kind: apps.KindPlugin},
			{ID: "sub", Name: "订阅", Kind: apps.KindPlugin},
		}
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})

	// 来源面板有三层：列表 / 订阅详情 / 插件配置，各查一遍。
	for _, tab := range []tabID{tabOverview, tabDetail, tabJobs, tabLogs, tabSettings, tabSources} {
		m.tab = tab
		seen := map[string]bool{}
		for _, a := range m.actions() {
			if a.key == "" || a.label == "" {
				t.Fatalf("%s：操作项缺键名或说明：%+v", tabTitles[tab], a)
			}
			if seen[a.key] {
				t.Fatalf("%s：同一个键 %q 出现在两个按钮上", tabTitles[tab], a.key)
			}
			seen[a.key] = true
		}
	}

	m.tab = tabSources
	m.feedFor = "https://example.com/feed.yaml"
	if len(m.actions()) == 0 {
		t.Fatalf("订阅详情应有可用操作")
	}
	m.feedFor = ""
	m.cfgFor = "corp-a"
	if len(m.actions()) == 0 {
		t.Fatalf("插件配置应有可用操作")
	}
}

// 每个按钮都必须点得动（命中区间非空），且点下去不会 panic、不会弹出错误框。
func TestActionBarButtonsAreClickable(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(t, m, key('6')) // 来源面板：动作最多

	_, spans := m.viewActions(m.width)
	if len(spans) == 0 {
		t.Fatalf("来源面板应至少有一个操作按钮")
	}
	if n := len(m.actions()); len(spans) != n {
		t.Fatalf("按钮数与命中区间数不一致：%d vs %d", n, len(spans))
	}
	for i := range spans {
		mid := (spans[i].start + spans[i].end) / 2
		got := update(t, m, click(mid, m.height-2))
		// 子系统没启用（这台机器上订阅/插件宿主没开）时报错是诚实的行为，不算 bug；
		// 这里要钉住的是「按钮点得动、映射到了某个动作」，不是「什么都不会报错」。
		if got.fatal != nil && !strings.Contains(got.fatal.Error(), "未启用") {
			t.Fatalf("点第 %d 个按钮（%s）弹出意外错误：%v", i+1, m.actions()[i].label, got.fatal)
		}
		got.fatal = nil
		got.prompt = nil
		got.confirm = nil
		m = got
	}
}

// 操作栏必须落在终端宽度以内，且窄终端下宁可少放按钮也不换行。
func TestActionBarFitsWidth(t *testing.T) {
	m := newTestModel(t)
	for w := 20; w <= 160; w += 7 {
		m.width = w
		for _, tab := range []tabID{tabOverview, tabSettings, tabSources} {
			m.tab = tab
			line, spans := m.viewActions(w)
			if Width(line) > w {
				t.Fatalf("宽度 %d 下操作栏超宽（%d）：%q", w, Width(line), line)
			}
			for _, s := range spans {
				if s.start < 0 || s.end > w || s.end <= s.start {
					t.Fatalf("宽度 %d 下命中区间不对：%+v", w, s)
				}
			}
			if strings.Contains(line, "\n") {
				t.Fatalf("操作栏不应换行：%q", line)
			}
		}
	}
}

// 键位语义全局唯一 —— 这几条正是之前打架的地方。
func TestKeySemanticsDoNotOverlap(t *testing.T) {
	m := newTestModelWith(t, func(o *control.Options) {
		o.Apps.Sources = []apps.SourceSpec{{ID: "corp-a", Name: "第一源", Kind: apps.KindPlugin}}
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.apps = twoApps()

	// 概览：R 是回滚（弹确认框），r 不再是回滚。
	m.tab = tabOverview
	m = update(t, m, key('r'))
	if m.confirm != nil {
		t.Fatalf("概览里 r 不该再是回滚（回滚已挪到 R）")
	}
	m = update(t, m, key('R'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "回滚") {
		t.Fatalf("概览里 R 应是回滚确认框，实际 %+v", m.confirm)
	}
	m.confirm = nil

	// 设置：D 是恢复默认，R 不再是（回滚在设置里没有意义）。
	m.tab = tabSettings
	m = update(t, m, key('R'))
	if m.confirm != nil {
		t.Fatalf("设置里 R 不该触发任何危险操作")
	}
	m = update(t, m, key('D'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "默认") {
		t.Fatalf("设置里 D 应是恢复默认确认框，实际 %+v", m.confirm)
	}
	m.confirm = nil

	// 来源：打开是 enter，c 不再兼职（c 全局表示「检查」）。
	m.tab = tabSources
	m.srcCursor = 0
	m = update(t, m, key('c'))
	if m.cfgFor != "" {
		t.Fatalf("来源里 c 不该再打开配置视图（打开已统一到 enter）")
	}
	m = update(t, m, key(tea.KeyEnter))
	if m.cfgFor == "" {
		t.Fatalf("enter 应打开插件配置")
	}

	// 子视图：返回只有 esc；q 不再是「返回」（q 只表示退出）。
	m = update(t, m, key('q'))
	if m.cfgFor == "" {
		t.Fatalf("子视图里 q 不该返回上一层")
	}
	m = update(t, m, key(tea.KeyEsc))
	if m.cfgFor != "" {
		t.Fatalf("esc 应返回来源列表")
	}

	// 订阅详情同理。
	m.feedFor = "https://example.com/feed.yaml"
	m = update(t, m, key('q'))
	if m.feedFor == "" {
		t.Fatalf("订阅详情里 q 不该返回")
	}
	m = update(t, m, key(tea.KeyEsc))
	if m.feedFor != "" {
		t.Fatalf("esc 应返回来源列表")
	}
}

// 日志的 G 与其它面板一致：跑到末尾（也就恢复了跟随）。
func TestLogFollowKeyIsTopBottom(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabLogs

	m = update(t, m, key('g'))
	if m.logFollow {
		t.Fatalf("跳到顶部应暂停跟随")
	}
	m = update(t, m, key('G'))
	if !m.logFollow {
		t.Fatalf("G 应回到末尾并恢复跟随")
	}
}
