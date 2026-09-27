package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// 启动后的第一帧必须说清「在忙什么」。
//
// 启动要做两件事：把订阅里的插件拉起来（本地，几毫秒到几秒）和挨个问上游版本
// （网络，可能十几秒）。以前这两步合并、且界面不设任何加载态 —— 于是用户盯着
// 「还没有任何软件」等半天，看起来就是卡死了 / 没有加载中。
func TestStartupShowsLoadingState(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	out := content(m)
	if !m.loading {
		t.Fatal("还没拿到列表就该处于加载态")
	}
	if !strings.Contains(out, "正在加载软件列表") {
		t.Fatalf("第一帧应说明正在加载列表：\n%s", out)
	}
	if !strings.Contains(out, m.spinnerText()) {
		t.Fatalf("加载态要有转圈：\n%s", out)
	}
	// 关键：不能在这一刻谎报「还没有任何软件」。
	if strings.Contains(out, "还没有任何软件") {
		t.Fatalf("列表还没到手就说「还没有任何软件」会让人以为坏了：\n%s", out)
	}
}

// 本地列表先到 → 界面立刻有内容，然后接着查上游；查完才收尾。
func TestLocalListThenCheck(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	apps := []*engine.App{demoApp(core.ActionNoOp), {Ref: core.AppRef{ID: "fzf", Name: "fzf"}}}

	m, cmd := updateCmd(t, m, appsMsg{apps: apps, local: true})
	if cmd == nil {
		t.Fatal("本地列表到了之后应立刻接着查上游")
	}
	if !m.loading || m.loadLabel != "正在检查上游版本" {
		t.Fatalf("应进入「正在检查上游版本」：%+v", m.loadLabel)
	}
	if m.checkTotal != 2 || len(m.checkLeft) != 2 {
		t.Fatalf("待检查清单应覆盖全部软件：total=%d left=%v", m.checkTotal, m.checkLeft)
	}
	out := content(m)
	if !strings.Contains(out, "检查中 0/2") {
		t.Fatalf("右上角应报检查进度：\n%s", out)
	}
	if !strings.Contains(out, "正在检查上游版本 0/2") {
		t.Fatalf("底栏应报检查进度：\n%s", out)
	}

	// 逐个回话：进度往前走，对应行的状态列显示「检查中」。
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventPhase, Phase: "检查",
		Level: core.LevelInfo, Msg: "本地 1.0.0 → 上游 1.1.0"}))
	if m.isChecking("demo") {
		t.Fatal("回话过的软件不该还算「检查中」")
	}
	if !m.isChecking("fzf") {
		t.Fatal("还没回话的软件应仍在检查中")
	}
	out = content(m)
	if !strings.Contains(out, "检查中 1/2") {
		t.Fatalf("进度应前进到 1/2：\n%s", out)
	}
	if !strings.Contains(out, "检查中") || !strings.Contains(strings.Join(strings.Fields(out), ""), "fzf") {
		t.Fatalf("列表应仍在渲染：\n%s", out)
	}

	// 检查收尾：加载态清空，列表回到正常渲染。
	m, _ = updateCmd(t, m, appsMsg{apps: apps})
	if m.loading || m.checkTotal != 0 || m.checkLeft != nil {
		t.Fatalf("收尾后应清空加载态：loading=%v total=%d left=%v", m.loading, m.checkTotal, m.checkLeft)
	}
	if !strings.Contains(content(m), "共 2 个软件") {
		t.Fatalf("收尾后底栏应报统计：\n%s", content(m))
	}
}

// 检查失败时也要收尾：不能停在「检查中」把界面锁死。
func TestCheckErrorClearsLoading(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = updateCmd(t, m, appsMsg{local: true})

	m, _ = updateCmd(t, m, appsMsg{err: errors.New("插件子系统未启用")})
	if m.loading || m.checkTotal != 0 {
		t.Fatalf("出错也要收尾：loading=%v total=%d", m.loading, m.checkTotal)
	}
	if m.fatal == nil || !strings.Contains(content(m), "插件子系统未启用") {
		t.Fatalf("错误要显示出来：%v\n%s", m.fatal, content(m))
	}
}

// 检查不是任务：它没有「完成」事件，不该在任务面板里留下永远「进行中」的行。
func TestCheckEventsDoNotCreateJobs(t *testing.T) {
	m := ready(t, demoApp(core.ActionNoOp))
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventPhase, Phase: "检查",
		Level: core.LevelInfo, Msg: "本地 1.0.0 → 上游 1.1.0"}))

	if len(m.jobs) != 0 {
		t.Fatalf("检查事件不该建任务行：%+v", m.jobs)
	}
	// 但要进日志 —— 用户在日志里能看到「本地 → 上游」。
	found := false
	for _, l := range m.logs {
		if strings.Contains(l.Msg, "上游") {
			found = true
		}
	}
	if !found {
		t.Fatalf("检查结果应进日志：%+v", m.logs)
	}

	// 真正的更新事件照旧建行。
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventStarted, Phase: "下载"}))
	if len(m.jobs) != 1 || m.jobs[0].State != "进行中" {
		t.Fatalf("更新应建任务行：%+v", m.jobs)
	}
}

// 空清单：加载结束后才说「还没有任何软件」，并给出下一步该去哪。
func TestEmptyStateOnlyAfterLoading(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = updateCmd(t, m, appsMsg{local: true})
	m, _ = updateCmd(t, m, appsMsg{})

	if m.loading {
		t.Fatal("收尾后不该还在加载")
	}
	out := content(m)
	if !strings.Contains(out, "还没有任何软件") || !strings.Contains(out, "来源") {
		t.Fatalf("空态要说清下一步：\n%s", out)
	}
}

// 冲突说明必须出现在概览里：只写「冲突」而不说跟谁冲突，用户没法处置。
func TestOverviewShowsConflictReason(t *testing.T) {
	conflict := demoApp(core.ActionNoOp)
	conflict.Shadowed = true
	conflict.Conflict = &engine.Conflict{
		Kind: "same-target", With: []string{"vscodium"},
		Message: "与 vscodium 安装到同一个位置",
	}
	m := ready(t, conflict)

	if out := content(m); !strings.Contains(out, "与 vscodium 安装到同一个位置") {
		t.Fatalf("概览应显示冲突原因：\n%s", out)
	}
	// 详情里同理。
	m.tab = tabDetail
	if out := content(m); !strings.Contains(out, "与 vscodium 安装到同一个位置") {
		t.Fatalf("详情应显示冲突原因：\n%s", out)
	}
}

// 详情里的动作名要跟概览一致（中文化），别一个「可更新」一个「update」。
func TestDetailActionLabelIsLocalised(t *testing.T) {
	m := ready(t, demoApp(core.ActionUpdate))
	m.tab = tabDetail

	out := content(m)
	if !strings.Contains(out, "可更新") {
		t.Fatalf("详情应显示中文动作名：\n%s", out)
	}
	if strings.Contains(out, "update") {
		t.Fatalf("不该把内部动作名直接显示给用户：\n%s", out)
	}
}

// 任务面板：在跑的任务带转圈，没有速度可言时不要显示「-- /s」。
func TestJobRowShowsSpinnerAndDash(t *testing.T) {
	m := ready(t, demoApp(core.ActionUpdate))
	m.tab = tabJobs
	m.jobs = []*jobItem{
		{AppID: "demo", Name: "Demo", State: "进行中", Phase: "下载", Done: 1 << 20, Total: 8 << 20},
		{AppID: "fzf", Name: "fzf", State: "完成", Phase: "收尾"},
	}

	out := content(m)
	if !strings.Contains(out, m.spinnerText()+" 进行中") {
		t.Fatalf("进行中的任务应带转圈：\n%s", out)
	}
	if strings.Contains(out, "-- /s") {
		t.Fatalf("没有速度时不该显示「-- /s」：\n%s", out)
	}
}
