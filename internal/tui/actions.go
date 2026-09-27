package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// action 是操作栏上的一个按钮。
//
// key 是它等价的按键：点它和执行那个键走同一条处理路径（handleKey），不另写一份逻辑 ——
// 否则「按钮能做的事」和「快捷键能做的事」迟早会分叉。label 是给人看的名字，界面上
// 渲染成 `[c] 检查`，于是不必先记住有哪些键才能操作。
type action struct {
	key   string
	label string
}

// actions 返回当前面板可用的操作（顺序即操作栏上的顺序）。
//
// 只放常用的几个：全查/全更这类低频动作留给底栏与 ? 面板。危险动作（卸载、回滚、
// 恢复默认）照旧会弹确认框，操作栏上点错了也不会直接落刀。
func (m Model) actions() []action {
	switch m.tab {
	case tabOverview:
		acts := []action{
			{"enter", "详情"}, {"c", "检查"}, {"u", "更新"}, {"p", "计划"},
			{"space", "启用/停用"}, {"R", "回滚"}, {"x", "卸载"},
		}
		// 筛选按钮的文案随状态变：写「隐藏停用」时点它才是隐藏，别让用户猜。
		if m.hideDisabled {
			acts = append(acts, action{"h", "显示停用"})
		} else {
			acts = append(acts, action{"h", "隐藏停用"})
		}
		return acts
	case tabDetail:
		return []action{
			{"p", "计划"}, {"u", "更新"}, {"R", "回滚"}, {"esc", "返回"},
		}
	case tabJobs:
		return []action{{"d", "清除已完成"}}
	case tabLogs:
		return []action{
			{"f", "切级别"}, {"F", "关键字"}, {"G", "跟进最新"},
		}
	case tabSettings:
		return []action{
			{"left", "− 减"}, {"right", "+ 加"}, {"enter", "编辑"},
			{"s", "保存"}, {"D", "恢复默认"},
		}
	case tabSources:
		return m.sourceActions()
	}
	return nil
}

// sourceActions 按「当前在看哪一层」给出来源面板的操作。
func (m Model) sourceActions() []action {
	if m.feedFor != "" {
		return []action{{"enter", "安装/更新"}, {"r", "重新拉取"}, {"esc", "返回"}}
	}
	if m.cfgFor != "" {
		return []action{{"enter", "编辑"}, {"D", "恢复默认"}, {"esc", "返回"}}
	}
	acts := []action{
		{"enter", "打开"}, {"space", "启用/停用"}, {"t", "信任"},
		{"r", "刷新"}, {"a", "加订阅"}, {"o", "官方源"},
	}
	if row, ok := m.currentSource(); ok && row.isSubscription {
		// 删除只对订阅有意义：插件来源来自清单与插件目录，界面删不掉。
		acts = append(acts, action{"d", "删除订阅"})
	}
	return acts
}

// currentSource 返回来源列表里选中的那一行。
func (m Model) currentSource() (sourceRow, bool) {
	rows := m.sourceRows()
	if m.srcCursor < 0 || m.srcCursor >= len(rows) {
		return sourceRow{}, false
	}
	return rows[m.srcCursor], true
}

// actionSpan 是操作栏上一个按钮占的列区间（左闭右开，按显示宽度计）。
type actionSpan struct{ start, end int }

// viewActions 渲染操作栏，并给出每个按钮的列区间供鼠标命中。
//
// 渲染与命中判定共用同一次宽度累加：分开算的话，换个字宽（中文标签、主题换边框）就会
// 点歪，而且这类偏差只在真终端上才看得出来。
func (m Model) viewActions(width int) (string, []actionSpan) {
	acts := m.actions()
	var b strings.Builder
	spans := make([]actionSpan, 0, len(acts))
	x := 0
	for _, a := range acts {
		plain := fmt.Sprintf("[%s] %s", a.key, a.label)
		gap := 0
		if x > 0 {
			gap = 2 // 按钮之间的间隔也占列，超宽判断要算进去
		}
		if x+gap+Width(plain) > width {
			break // 放不下就少放几个，宁可少一个按钮也不要换行
		}
		if gap > 0 {
			b.WriteString("  ")
			x += gap
		}
		spans = append(spans, actionSpan{x, x + Width(plain)})
		b.WriteString(m.theme.Title().Render("[" + a.key + "]"))
		b.WriteString(" ")
		b.WriteString(m.theme.Primary().Render(a.label))
		x += Width(plain)
	}
	return b.String(), spans
}

// actionAt 判断点击落在哪个按钮上。
func (m Model) actionAt(x int) (int, bool) {
	_, spans := m.viewActions(m.width)
	for i, s := range spans {
		if x >= s.start && x < s.end {
			return i, true
		}
	}
	return 0, false
}

// runAction 执行操作栏上第 i 个按钮：等价于按下它标注的那个键。
//
// 走按键处理而不是直接调控制层，是为了让两条入口永远做同一件事 —— 操作栏只是把
// 快捷键画出来而已。
func (m Model) runAction(i int) (tea.Model, tea.Cmd) {
	acts := m.actions()
	if i < 0 || i >= len(acts) {
		return m, nil
	}
	return m.handleKey(pressKey(acts[i].key))
}

// pressKey 把键名转成一次按键消息。
//
// 与真实终端一致：可打印字符同时填 Code 与 Text（String() 优先取 Text），功能键只填
// Code。操作栏里的按钮就是靠它把「点」变成「按」。
func pressKey(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	r := []rune(name)
	if len(r) != 1 {
		return tea.KeyPressMsg{}
	}
	k := tea.KeyPressMsg{Code: r[0]}
	if unicode.IsPrint(r[0]) {
		k.Text = name
	}
	return k
}
