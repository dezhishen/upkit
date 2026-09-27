package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// click 构造一次左键点击。
func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// wheel 构造一次滚轮事件（up 为真表示向上滚）。
func wheel(up bool) tea.MouseWheelMsg {
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	return tea.MouseWheelMsg{Button: b}
}

// tabHitX 返回第 i 个标签的中点列号（按与渲染相同的算法算）。
//
// 返回的是**屏幕**列号：界面四周有一圈边距（windowPad*），命中判定在
// handleMouse 里才把屏幕坐标换算成界面坐标，所以这里要自己加上。
func tabHitX(t *testing.T, m Model, i int) int {
	t.Helper()
	pos := windowPadX
	for j := 0; j < i; j++ {
		pos += Width(fmt.Sprintf(" %d %s ", j+1, tabTitles[j])) + Width(m.tabSeparator())
	}
	return pos + Width(fmt.Sprintf(" %d %s ", i+1, tabTitles[i]))/2
}

// 点标签行就能切面板 —— 不必先记住 1-6。
func TestMouseClickTabSwitchesPanel(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	for i := range tabTitles {
		m = update(t, m, click(tabHitX(t, m, i), windowPadY))
		if int(m.tab) != i {
			t.Fatalf("点第 %d 个标签应切到 %s，实际 %s", i+1, tabTitles[i], tabTitles[m.tab])
		}
	}

	// 点标签行以外的空白处不应改变面板。
	m = update(t, m, click(m.width-1, windowPadY))
	if int(m.tab) != len(tabTitles)-1 {
		t.Fatalf("点空白处不应切面板，实际 %s", tabTitles[m.tab])
	}
}

// 点列表行选中，双击等于回车（打开/编辑）。
func TestMouseRowClickAndDoubleClick(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	// 内容区从第 2 行开始（第 0 行标签、第 1 行上边框），而第一行是分组标题「网络」，
	// 所以第 1 项落在 y=3。
	m = update(t, m, click(windowPadX+4, windowPadY+3))
	if m.setCursor != 0 {
		t.Fatalf("点第 1 项应选中它，实际 %d", m.setCursor)
	}

	// 同一位置再点一次 = 双击 = 回车：网络代理是文本项，应弹出输入框。
	m = update(t, m, click(windowPadX+4, windowPadY+3))
	if m.prompt == nil {
		t.Fatalf("双击文本项应打开编辑框")
	}

	// 数字项双击 = 打开编辑框整段输入（预填当前值）。
	m.prompt = nil
	m = update(t, m, click(windowPadX+4, windowPadY+4))
	m = update(t, m, click(windowPadX+4, windowPadY+4))
	if m.prompt == nil {
		t.Fatalf("双击数字项应打开编辑框")
	}
	if got := m.prompt.Input.Value(); got != "60" {
		t.Fatalf("编辑框应预填当前值 60，实际 %q", got)
	}
}

// 滚轮按固定步长移动光标。
func TestMouseWheelMovesCursor(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	m = update(t, m, wheel(false))
	if m.setCursor != scrollStep {
		t.Fatalf("向下滚一格应前进 %d 行，实际 %d", scrollStep, m.setCursor)
	}
	m = update(t, m, wheel(true))
	if m.setCursor != 0 {
		t.Fatalf("向上滚回起点，实际 %d", m.setCursor)
	}
	if m.setCursor < 0 {
		t.Fatalf("光标不应为负：%d", m.setCursor)
	}
}

// 点操作栏上的按钮 = 按它标注的那个键。
func TestMouseClickActionBarRunsAction(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.apps = []*engine.App{{Ref: core.AppRef{ID: "demo", Name: "Demo"}, Action: core.ActionUpdate}}

	// 按名字找按钮，而不是写死「第一个」—— 操作栏会随功能增删而变。
	x := actionHitX(t, m, "检查")
	m = update(t, m, click(x, windowPadY+m.innerHeight()-2))
	if !m.busy {
		t.Fatalf("点「检查」应开始检查，实际 status=%q", m.status)
	}
	if !strings.Contains(m.status, "检查") {
		t.Fatalf("状态应提到检查，实际 %q", m.status)
	}
}

// actionHitX 返回操作栏上某个按钮的中点列号（屏幕坐标）。
func actionHitX(t *testing.T, m Model, label string) int {
	t.Helper()
	acts := m.actions()
	_, spans := m.viewActions(m.innerWidth())
	for i, a := range acts {
		if a.label == label && i < len(spans) {
			return windowPadX + (spans[i].start+spans[i].end)/2
		}
	}
	t.Fatalf("操作栏上没有「%s」按钮：%+v", label, acts)
	return 0
}

// 确认框上的两个按钮都能点。
func TestMouseClickConfirmButtons(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	yes := false
	m.confirm = &confirmBox{Title: "测试", Message: "继续吗？",
		OnYes: func(*Model) tea.Cmd { yes = true; return nil }}

	x0, y0, _, h := m.modalBox()
	// 按钮行：内容最后一行；「确定」在左边距之后。
	m = update(t, m, click(x0+2+2, y0+h-2))
	if !yes || m.confirm != nil {
		t.Fatalf("点「确定」应执行并关闭确认框，yes=%v confirm=%v", yes, m.confirm)
	}

	// 取消：确认框关掉，动作不执行。
	m.confirm = &confirmBox{Title: "测试", Message: "继续吗？",
		OnYes: func(*Model) tea.Cmd { yes = false; return nil }}
	x0, y0, _, h = m.modalBox()
	noX := x0 + 2 + Width(modalYesLabel) + 3 + 2
	m = update(t, m, click(noX, y0+h-2))
	if m.confirm != nil {
		t.Fatalf("点「取消」应关闭确认框")
	}
}

// 点输入框外面 = 取消（和按 esc 一样）。
func TestMouseClickOutsidePromptCancels(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.prompt = newPromptBox("路径", "输入", "", false, nil)

	m = update(t, m, click(windowPadX, windowPadY))
	if m.prompt != nil {
		t.Fatalf("点弹窗外面应取消输入")
	}
}

// 来源面板的点行：分组标题与空行占掉的行号必须算进去。
func TestMouseClickSourceRow(t *testing.T) {
	m := newTestModelWith(t, func(o *control.Options) {
		o.Apps.Sources = []apps.SourceSpec{
			{ID: "corp-a", Name: "第一源", Kind: apps.KindPlugin},
			{ID: "corp-b", Name: "第二源", Kind: apps.KindPlugin},
		}
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('5'))

	// 内容第 0 行是「插件来源」标题，第 1 行才是第一条来源。
	m = update(t, m, click(windowPadX+4, windowPadY+4))
	if m.srcCursor != 1 {
		t.Fatalf("点第二条来源应选中第 2 行，实际 %d", m.srcCursor)
	}
	// 标题行不是条目，点了不该动光标。
	m = update(t, m, click(windowPadX+4, windowPadY+2))
	if m.srcCursor != 1 {
		t.Fatalf("点分组标题不应改选中行，实际 %d", m.srcCursor)
	}

	// 设置面板同理：分组标题占一行，点它不改选中项。
	m.tab = tabSettings
	m.setCursor = 1
	m = update(t, m, click(windowPadX+4, windowPadY+2))
	if m.setCursor != 1 {
		t.Fatalf("点设置面板的分组标题不应改选中项，实际 %d", m.setCursor)
	}
}
