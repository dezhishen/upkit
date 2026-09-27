package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// updateCmd 与 update 一样，但把命令也返回 —— 有些按键只产生命令（导出、检查），
// 断言「有没有产生命令」比断言界面状态更直接。
func updateCmd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update 返回了非 Model 类型: %T", next)
	}
	return got, cmd
}

// ready 造一个「有内容、尺寸已定」的模型。
func ready(t *testing.T, apps ...*engine.App) Model {
	t.Helper()
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.apps = apps
	return m
}

func demoApp(action core.Action) *engine.App {
	return &engine.App{Ref: core.AppRef{ID: "demo", Name: "Demo"}, Action: action}
}

// 概览面板的每个按键都要有反应（这是最常用的一屏）。
func TestOverviewKeyFlow(t *testing.T) {
	m := ready(t, demoApp(core.ActionUpdate))
	m.tab = tabOverview

	// 检查：选中 / 全部。
	m, cmd := updateCmd(t, m, key('c'))
	if !m.busy || cmd == nil {
		t.Fatalf("c 应开始检查并产生命令")
	}
	m.busy = false
	m, _ = updateCmd(t, m, key('C'))
	if !m.busy {
		t.Fatalf("C 应开始全量检查")
	}
	m.busy = false

	// 更新：先弹确认，确认后进入执行态。
	m = update(t, m, key('u'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "执行") {
		t.Fatalf("u 应先确认，实际 %+v", m.confirm)
	}
	m, cmd = updateCmd(t, m, key('y'))
	if m.confirm != nil || !m.busy || cmd == nil {
		t.Fatalf("确认后应关闭弹窗并开始执行：confirm=%v busy=%v", m.confirm, m.busy)
	}
	m.busy = false

	// 计划：p 与回车等价。
	m, _ = updateCmd(t, m, key('p'))
	if !m.busy {
		t.Fatalf("p 应开始生成计划")
	}
	m.busy = false
	m, _ = updateCmd(t, m, key(tea.KeyEnter))
	if !m.busy {
		t.Fatalf("回车应等价于 p")
	}
	m.busy = false

	// 卸载 / 回滚：都必须先确认（危险动作不能一键落刀）。
	m = update(t, m, key('x'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "卸载") {
		t.Fatalf("x 应先确认卸载，实际 %+v", m.confirm)
	}
	m.confirm = nil
	m = update(t, m, key('R'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "回滚") {
		t.Fatalf("R 应先确认回滚，实际 %+v", m.confirm)
	}
	m.confirm = nil

	// 导出：直接产生命令；导入：先问路径。
	if _, cmd := updateCmd(t, m, key('E')); cmd == nil {
		t.Fatalf("E 应产生导出命令")
	}
	m = update(t, m, key('I'))
	if m.prompt == nil {
		t.Fatalf("I 应弹出路径输入框")
	}
	if got := m.prompt.Input.Value(); !strings.HasSuffix(got, "upkit-manifest.json") {
		t.Fatalf("导入框应预填清单路径，实际 %q", got)
	}
	m.prompt = nil
}

// 启停要真的落盘。软件只能来自订阅，所以启用状态写在来源声明上 —— 不属于任何来源
// 的条目根本无处可存，那种情况下报错才对（这里是它的反面：有来源时必须写进去）。
func TestToggleAppEnabledPersists(t *testing.T) {
	m := newTestModelWith(t, func(o *control.Options) {
		// 启用状态要落盘，所以清单得有真实路径（否则保存失败、内存里改了盘上没改）。
		o.Apps.Path = filepath.Join(t.TempDir(), "apps.yaml")
		o.Apps.Sources = []apps.SourceSpec{{ID: "corp", Name: "企业源", Kind: apps.KindPlugin}}
		// 来源 kind 必须写成 plugin:<来源ID>：启用状态就是按这个前缀找到来源再落盘的。
		o.Apps.Apps = []apps.AppSpec{{
			ID:      "corp/demo",
			Name:    "Demo",
			Source:  map[string]any{apps.KeyKind: apps.KindPlugin + ":corp", apps.SourceKeyApp: "demo"},
			Install: apps.InstallSpec{Path: filepath.Join(t.TempDir(), "demo")},
		}}
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabOverview
	m.apps = []*engine.App{{Ref: core.AppRef{ID: "corp/demo", Name: "Demo"}, Action: core.ActionNoOp}}

	if !appEnabled(m.apps[0], m.ctrl) {
		t.Fatalf("初始应为启用")
	}
	m, _ = updateCmd(t, m, key(' '))
	if appEnabled(m.apps[0], m.ctrl) {
		t.Fatalf("空格应停用该软件")
	}
	if !strings.Contains(m.status, "启用") {
		t.Fatalf("应提示状态已更新，实际 %q", m.status)
	}
	// 再按一次转回来。
	m = update(t, m, key(' '))
	if !appEnabled(m.apps[0], m.ctrl) {
		t.Fatalf("再按一次应重新启用")
	}
}

// 批量更新：没有可更新的就提示、有就先确认。
func TestOverviewBulkUpdate(t *testing.T) {
	m := ready(t, demoApp(core.ActionNoOp))
	m.tab = tabOverview
	if m = update(t, m, key('U')); m.confirm != nil {
		t.Fatalf("没有可更新的软件时不该弹确认框")
	}
	if !strings.Contains(m.status, "没有需要更新") {
		t.Fatalf("应提示没有可更新的，实际 %q", m.status)
	}

	m.apps = []*engine.App{demoApp(core.ActionUpdate), demoApp(core.ActionInstall)}
	m = update(t, m, key('U'))
	if m.confirm == nil || !strings.Contains(m.confirm.Message, "2 个软件") {
		t.Fatalf("应确认批量更新 2 个，实际 %+v", m.confirm)
	}
}

// 光标移动与首尾跳转。
func TestCursorNavigation(t *testing.T) {
	m := ready(t, demoApp(core.ActionNoOp), demoApp(core.ActionNoOp), demoApp(core.ActionNoOp))
	m.tab = tabOverview

	m = update(t, m, key('j'))
	if m.cursor != 1 {
		t.Fatalf("j 应下移，实际 %d", m.cursor)
	}
	m = update(t, m, key('G'))
	if m.cursor != 2 {
		t.Fatalf("G 应到末尾，实际 %d", m.cursor)
	}
	// 越界不上不下：光标必须夹在界内。
	m = update(t, m, key('j'))
	if m.cursor != 2 {
		t.Fatalf("已在末尾，再按 j 不该越界，实际 %d", m.cursor)
	}
	m = update(t, m, key('g'))
	if m.cursor != 0 || m.offset != 0 {
		t.Fatalf("g 应回到开头，实际 cursor=%d offset=%d", m.cursor, m.offset)
	}
	m = update(t, m, key('k'))
	if m.cursor != 0 {
		t.Fatalf("已在开头，再按 k 不该变，实际 %d", m.cursor)
	}

	// 列表变短之后光标要收回界内。
	m.apps = m.apps[:1]
	m = update(t, m, key('G'))
	if m.cursor != 0 {
		t.Fatalf("列表只剩一条时光标应为 0，实际 %d", m.cursor)
	}
}

// 详情面板：滚动、计划、更新、esc 返回。
func TestDetailKeyFlow(t *testing.T) {
	m := ready(t, demoApp(core.ActionUpdate))
	m.tab = tabDetail

	m = update(t, m, key('j'))
	if m.detailY != 1 {
		t.Fatalf("j 应下滚一行，实际 %d", m.detailY)
	}
	m = update(t, m, key('g'))
	if m.detailY != 0 {
		t.Fatalf("g 应回到顶部，实际 %d", m.detailY)
	}
	m, _ = updateCmd(t, m, key('p'))
	if !m.busy {
		t.Fatalf("p 应开始生成计划")
	}
	m.busy = false

	if m = update(t, m, key(tea.KeyEsc)); m.tab != tabOverview {
		t.Fatalf("esc 应回到概览，实际 %v", tabTitles[m.tab])
	}
}

// 任务面板：滚动与清除已完成。
func TestJobsKeyFlow(t *testing.T) {
	m := ready(t)
	m.tab = tabJobs
	m.jobs = []*jobItem{{Name: "Demo", State: "完成"}, {Name: "Two", State: "失败"}, {Name: "Three", State: "进行中"}}

	m = update(t, m, key('j'))
	if m.jobCursor != 1 {
		t.Fatalf("j 应下移，实际 %d", m.jobCursor)
	}
	m = update(t, m, key('d'))
	if len(m.jobs) != 1 || m.jobs[0].Name != "Three" {
		t.Fatalf("d 应清掉完成与失败的，实际 %+v", m.jobs)
	}
	if m.status == "" {
		t.Fatalf("清除后应给一句提示")
	}
}

// 日志面板：切级别、关键字、跟随、滚动。
func TestLogsKeyFlow(t *testing.T) {
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabLogs
	for i := 0; i < 30; i++ {
		m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventLog,
			Level: core.LevelInfo, Msg: "第 " + string(rune('a'+i%26)) + " 行日志"}))
	}

	start := m.logLevel
	m = update(t, m, key('f'))
	if m.logLevel == start || !strings.Contains(m.status, "日志级别") {
		t.Fatalf("f 应切换级别，实际 %q（status=%q）", m.logLevel, m.status)
	}

	m = update(t, m, key('F'))
	if m.prompt == nil {
		t.Fatalf("F 应弹出关键字输入框")
	}
	m = update(t, m, key(tea.KeyEsc))

	m = update(t, m, key('g')) // 跳到顶部 = 暂停跟随
	if m.logFollow {
		t.Fatalf("翻历史应暂停跟随")
	}
	m = update(t, m, key('G'))
	if !m.logFollow {
		t.Fatalf("G 应恢复跟随")
	}
	if m.logView.View() == "" {
		t.Fatalf("日志视口应有内容")
	}
}

// 设置面板：调整、编辑、保存、恢复默认。
func TestSettingsKeyFlow(t *testing.T) {
	m := ready(t)
	m.tab = tabSettings

	// ←/→ 与 h/l 等价，都按步长走（先把光标移到数字项上：第一项是文本类的代理）。
	idx := -1
	for i, f := range m.settingsRows() {
		if f.Key == "network.timeout_seconds" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("找不到请求超时这一项")
	}
	m.setCursor = idx
	before := settingText(t, m, "network.timeout_seconds")
	m = update(t, m, key(tea.KeyRight))
	after := settingText(t, m, "network.timeout_seconds")
	if after == before {
		t.Fatalf("→ 应改变数值")
	}
	m = update(t, m, key('h'))
	if got := settingText(t, m, "network.timeout_seconds"); got != before {
		t.Fatalf("h 应等价于 ←，期望回到 %q，实际 %q", before, got)
	}

	// 保存与「没有修改」两条路。
	m = update(t, m, key('s'))
	if !strings.Contains(m.status, "已保存") {
		t.Fatalf("s 应保存并提示，实际 %q", m.status)
	}
	m = update(t, m, key('s'))
	if !strings.Contains(m.status, "没有需要保存") {
		t.Fatalf("没有修改时 s 应提示，实际 %q", m.status)
	}

	// 恢复默认要确认。
	m = update(t, m, key('D'))
	if m.confirm == nil || !strings.Contains(m.confirm.Title, "默认") {
		t.Fatalf("D 应先确认，实际 %+v", m.confirm)
	}
	m, _ = updateCmd(t, m, key('y'))
	if !m.settingsDirty() {
		t.Fatalf("恢复默认后应标记为待保存")
	}
}

// 事件要落到任务与日志上（界面唯一的进度来源）。
func TestEventFlow(t *testing.T) {
	m := ready(t)
	m.tab = tabOverview

	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventStarted, Msg: "开始更新"}))
	if m.tab != tabJobs {
		t.Fatalf("开始执行时应自动切到任务面板，实际 %v", tabTitles[m.tab])
	}
	if len(m.jobs) != 1 || m.jobs[0].State != "进行中" {
		t.Fatalf("任务状态不对: %+v", m.jobs)
	}

	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventProgress, Done: 512, Total: 1024}))
	if m.jobs[0].Done != 512 || m.jobs[0].Total != 1024 {
		t.Fatalf("进度没更新: %+v", m.jobs[0])
	}

	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventFinished, Msg: "完成"}))
	if m.jobs[0].State != "完成" {
		t.Fatalf("完成状态没更新: %+v", m.jobs[0])
	}
	if m.jobs[0].Elapsed < 0 {
		t.Fatalf("应记下耗时")
	}

	// 上一轮已标记完成，所以这次失败属于新的一条任务（同一次运行的进度才会合并到同一行；
	// 完成后重新执行是另一次运行）。
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventFailed, Err: errors.New("安装失败")}))
	if len(m.jobs) != 2 {
		t.Fatalf("完成后的失败事件应新开一行任务，实际 %d 行", len(m.jobs))
	}
	last := m.jobs[len(m.jobs)-1]
	if last.State != "失败" || m.fatal == nil {
		t.Fatalf("失败应写进任务并弹出错误：state=%q fatal=%v", last.State, m.fatal)
	}
}

// 状态提示会自己过期（底栏不该长期挂着一条旧消息）。
func TestStatusExpires(t *testing.T) {
	m := ready(t)
	m.setStatus("一会儿就消失")
	if m.status != "一会儿就消失" || m.statusT.IsZero() {
		t.Fatalf("状态没设上: %q", m.status)
	}

	// 没过期时不该被清掉。
	m = update(t, m, spinner.TickMsg{})
	if m.status == "" {
		t.Fatalf("刚设的状态不该立刻被清掉")
	}

	// 过期清理搭在 spinner 的 tick 上：把时间往回拨再 tick 一次。
	m.statusT = time.Now().Add(-statusTTL - time.Second)
	m = update(t, m, spinner.TickMsg{})
	if m.status != "" || m.fatal != nil {
		t.Fatalf("过了 TTL 应清掉状态与错误框，实际 status=%q fatal=%v", m.status, m.fatal)
	}
}
