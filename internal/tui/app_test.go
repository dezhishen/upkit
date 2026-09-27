package tui

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/settings"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	return newTestModelWith(t, nil)
}

// newTestModelWith 在默认装配之上再改一改控制层选项。
//
// 插件宿主与订阅仓库由控制层持有，测试要注入它们得从这里走，而不是往 Model 上
// 挂字段 —— 挂上去的话，界面用的是测试塞的那份，控制层用的是另一份，两边就岔了。
func newTestModelWith(t *testing.T, tweak func(*control.Options)) Model {
	t.Helper()
	dir := t.TempDir()

	set := settings.Default()
	set.Path = filepath.Join(dir, "settings.yaml")
	set.Storage.DataDir = filepath.Join(dir, "data")
	set.Logs.Dir = filepath.Join(dir, "logs")
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	mgr, err := logging.New(logging.Options{Level: "debug", Dir: set.Logs.Dir})
	if err != nil {
		t.Fatalf("logging.New: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	afs := apps.Default()
	afs.Apps = []apps.AppSpec{{
		ID:      "demo",
		Name:    "Demo",
		Source:  map[string]any{"kind": "github-release", "repo": "owner/repo"},
		Install: apps.InstallSpec{Path: filepath.Join(dir, "demo"), Entrypoints: []string{"demo.exe"}},
	}}

	opts := control.Options{Settings: set, Apps: afs, Logger: mgr, EventBuffer: 32}
	if tweak != nil {
		tweak(&opts)
	}
	ctrl, err := control.New(opts)
	if err != nil {
		t.Fatalf("control.New: %v", err)
	}

	return New(Options{
		Ctrl:       ctrl,
		Version:    "test",
		ConfigPath: set.Path,
	})
}

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update 返回了非 Model 类型: %T", next)
	}
	return got
}

// 每个面板都应能渲染出内容且不 panic。
func TestRenderAllTabs(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	for i := 1; i <= int(tabCount); i++ {
		m = update(t, m, key(rune('0'+i)))
		out := content(m)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("面板 %d 渲染为空", i)
		}
		if !strings.Contains(out, tabTitles[i-1]) {
			t.Fatalf("面板 %d 未显示标题 %q:\n%s", i, tabTitles[i-1], out)
		}
	}
}

// 帮助与确认弹窗应能渲染。
func TestModals(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = update(t, m, key('?'))
	if !strings.Contains(content(m), "快捷键") {
		t.Fatalf("帮助弹窗未渲染")
	}
	m = update(t, m, key('x'))

	m.confirm = &confirmBox{Title: "测试", Message: "确认吗？", OnYes: func(*Model) tea.Cmd { return nil }}
	if !strings.Contains(content(m), "确认吗？") {
		t.Fatalf("确认弹窗未渲染")
	}
	// y 触发确认、n 取消
	m = update(t, m, key('n'))
	if m.confirm != nil {
		t.Fatalf("按 n 应关闭弹窗")
	}

	m.prompt = newPromptBox("路径", "输入", "abc", false, nil)
	m = update(t, m, key('d'))
	if m.prompt == nil || m.prompt.Input.Value() != "abcd" {
		t.Fatalf("输入未追加: %+v", m.prompt)
	}
	m = update(t, m, key(tea.KeyBackspace))
	if m.prompt.Input.Value() != "abc" {
		t.Fatalf("退格未生效: %+v", m.prompt)
	}
	m = update(t, m, key(tea.KeyEsc))
	if m.prompt != nil {
		t.Fatalf("按 Esc 应关闭输入框")
	}
}

// 输入框必须接受非 ASCII：Windows 中文用户名下的路径（C:\Users\张三\...）极常见，
// 手写实现用 len(key)==1 判单键，多字节 rune 会被静默丢弃。
func TestPromptAcceptsNonASCII(t *testing.T) {
	m := Model{}
	m.prompt = newPromptBox("路径", "输入", "", false, nil)

	m = update(t, m, text("张三"))
	if got := m.prompt.Input.Value(); got != "张三" {
		t.Fatalf("中文未输入: %q", got)
	}

	// 一次退格只删一个字符，不能按字节截断成非法 UTF-8。
	m = update(t, m, key(tea.KeyBackspace))
	if got := m.prompt.Input.Value(); got != "张" {
		t.Fatalf("退格后应为 %q，实际 %q", "张", got)
	}
	if !utf8.ValidString(m.prompt.Input.Value()) {
		t.Fatalf("输入内容不是合法 UTF-8: %q", m.prompt.Input.Value())
	}
}

// 设置面板：调整、切换与保存。
func TestSettingsPanel(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	before := settingText(t, m, "network.timeout_seconds")
	m = update(t, m, key('j')) // 移到「请求超时（秒）」
	m = update(t, m, key(tea.KeyRight))
	after := settingText(t, m, "network.timeout_seconds")
	if after == before {
		t.Fatalf("→ 未改变数值")
	}
	if !m.ctrl.SettingsDirty() {
		t.Fatalf("修改后应标记为未保存")
	}

	m = update(t, m, key('s'))
	if m.ctrl.SettingsDirty() {
		t.Fatalf("保存后不应仍标记为未保存")
	}
	reloaded, err := settings.Load(m.ctrl.SettingsPath())
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if strconv.Itoa(reloaded.Network.TimeoutSeconds) != after {
		t.Fatalf("设置未落盘: %d != %s", reloaded.Network.TimeoutSeconds, after)
	}
}

// settingText 取设置表单里某一项的展示文本（表单由控制层给出）。
func settingText(t *testing.T, m Model, key string) string {
	t.Helper()
	for _, f := range m.settingsRows() {
		if f.Key == key {
			return f.Text
		}
	}
	t.Fatalf("设置项 %s 不存在", key)
	return ""
}

// 事件应进入任务面板与日志面板。
func TestHandleEvent(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventStarted, Level: core.LevelInfo, Msg: "开始更新"}))
	m = update(t, m, eventMsg(core.Event{AppID: "demo", Kind: core.EventProgress, Phase: "下载", Done: 512, Total: 1024, Speed: 4096}))

	if len(m.jobs) != 1 {
		t.Fatalf("期望 1 个任务，得到 %d", len(m.jobs))
	}
	if m.jobs[0].Done != 512 || m.jobs[0].State == "" {
		t.Fatalf("任务状态未更新: %+v", m.jobs[0])
	}
	if len(m.logs) == 0 {
		t.Fatalf("日志面板应收到事件")
	}
}

// 中文界面在 ASCII 模式下也应正常渲染。
func TestASCIIMode(t *testing.T) {
	m := newTestModel(t)
	m.theme = NewTheme(true, true, "ascii")
	m.helpView = newHelpModel(true)
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	out := content(m)
	if !strings.Contains(out, "+") {
		t.Fatalf("ASCII 模式应使用 + 作为边角")
	}
}

// 禁用颜色后界面不得残留「设置颜色」的 ANSI 序列。
//
// help.New() 自带一套深色配色，若忘了替换，底栏与 ? 面板会继续输出颜色转义，
// --no-color 就只是「部分生效」。
//
// 加粗（ESC[1m）、重置（ESC[m）与反色（ESC[7m）不在检查范围内：--no-color 的
// 语义是禁用颜色，这三者都不是颜色。加粗自 v1 起就一直开着；反色用于光标，
// 清掉后光标将不可见。
func TestNoColorEmitsNoColorANSI(t *testing.T) {
	m := newTestModel(t)
	m.theme = NewTheme(false, true, "unicode")
	m.helpView = newHelpModel(true)
	m.apps = []*engine.App{{Ref: core.AppRef{ID: "demo", Name: "Demo"}, Action: core.ActionUpdate}}
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	assertNoColor := func(what, out string) {
		t.Helper()
		stripped := strings.NewReplacer("\x1b[1m", "", "\x1b[m", "", "\x1b[7m", "", "\x1b[27m", "").Replace(out)
		if strings.ContainsRune(stripped, 0x1b) {
			t.Fatalf("%s 在禁用颜色后仍输出颜色转义序列:\n%q", what, out)
		}
	}

	for _, tab := range []tabID{tabOverview, tabDetail, tabJobs, tabLogs, tabSettings, tabSources} {
		m.tab = tab
		assertNoColor("面板 "+tabTitles[tab], content(m))
	}

	m.help = true
	assertNoColor("? 面板", content(m))
	m.help = false

	m.confirm = &confirmBox{Title: "确认", Message: "继续吗？", OnYes: func(*Model) tea.Cmd { return nil }}
	assertNoColor("确认弹窗", content(m))
	m.confirm = nil

	m.prompt = newPromptBox("路径", "输入", "abc", false, nil)
	assertNoColor("输入弹窗", content(m))
}

// 底栏提示必须落在终端宽度内，不能换行。
//
// help 组件的宽度默认是 0（等于不截断），必须显式设定，否则窄终端下提示会
// 折成两行，把正文挤掉一行。左侧状态文字也要限幅：它是错误信息，长度不可控。
func TestFooterFitsWidth(t *testing.T) {
	m := newTestModel(t)
	for w := 20; w <= 200; w++ {
		m = update(t, m, tea.WindowSizeMsg{Width: w, Height: 24})
		foot := m.viewFooter(w)
		if got := lipgloss.Width(foot); got > w {
			t.Fatalf("宽度 %d 下底栏渲染为 %d 列，已溢出", w, got)
		}
	}
}

// 超长错误信息不得把底栏撑破。
func TestFooterFitsWidthWithLongError(t *testing.T) {
	m := newTestModel(t)
	m.fatal = errors.New(strings.Repeat("上游返回 502，已重试 3 次仍未成功；", 12))
	for w := 20; w <= 200; w++ {
		if got := lipgloss.Width(m.viewFooter(w)); got > w {
			t.Fatalf("宽度 %d 下带长错误的底栏渲染为 %d 列，已溢出", w, got)
		}
	}
}
