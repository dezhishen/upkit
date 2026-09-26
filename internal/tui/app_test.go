package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/registry/all"
	"github.com/dezhishen/upkit/internal/settings"
)

func newTestModel(t *testing.T) Model {
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

	eng, err := engine.New(engine.Options{
		Settings: set,
		Apps:     afs,
		Registry: all.Registry(),
		Log:      mgr,
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	evSink, _ := NewSink(32)
	return New(Options{
		Engine:     eng,
		Settings:   set,
		Apps:       afs,
		Logger:     mgr,
		Sink:       evSink,
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
		key := string(rune('0' + i))
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		view := m.View()
		if strings.TrimSpace(view) == "" {
			t.Fatalf("面板 %d 渲染为空", i)
		}
		if !strings.Contains(view, tabTitles[i-1]) {
			t.Fatalf("面板 %d 未显示标题 %q:\n%s", i, tabTitles[i-1], view)
		}
	}
}

// 帮助与确认弹窗应能渲染。
func TestModals(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !strings.Contains(m.View(), "快捷键") {
		t.Fatalf("帮助弹窗未渲染")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	m.confirm = &confirmBox{Title: "测试", Message: "确认吗？", OnYes: func(*Model) tea.Cmd { return nil }}
	if !strings.Contains(m.View(), "确认吗？") {
		t.Fatalf("确认弹窗未渲染")
	}
	// y 触发确认、n 取消
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.confirm != nil {
		t.Fatalf("按 n 应关闭弹窗")
	}

	m.prompt = &promptBox{Title: "路径", Label: "输入", Buf: "abc"}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m.prompt == nil || m.prompt.Buf != "abcd" {
		t.Fatalf("输入未追加: %+v", m.prompt)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.prompt.Buf != "abc" {
		t.Fatalf("退格未生效: %+v", m.prompt)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.prompt != nil {
		t.Fatalf("按 Esc 应关闭输入框")
	}
}

// 设置面板：调整、切换与保存。
func TestSettingsPanel(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	before := m.set.Network.TimeoutSeconds
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // 移到「请求超时（秒）」
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.set.Network.TimeoutSeconds == before {
		t.Fatalf("→ 未改变数值")
	}
	if !m.setDirty {
		t.Fatalf("修改后应标记为未保存")
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if err := m.set.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	reloaded, err := settings.Load(m.set.Path)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if reloaded.Network.TimeoutSeconds != m.set.Network.TimeoutSeconds {
		t.Fatalf("设置未落盘: %d != %d", reloaded.Network.TimeoutSeconds, m.set.Network.TimeoutSeconds)
	}
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
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(m.View(), "+") {
		t.Fatalf("ASCII 模式应使用 + 作为边角")
	}
}
