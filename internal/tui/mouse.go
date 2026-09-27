package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// doubleClickWindow 是判定「双击」的时间窗。
//
// bubbletea v2 的鼠标事件里没有点击次数，只有一次次的点击，所以双击只能自己按
// 「同一位置、间隔够短」来判。
const doubleClickWindow = 400 * time.Millisecond

// ── 鼠标 ──────────────────────────────────────────────────────

// handleMouse 处理鼠标事件。
//
// 加鼠标不是为了取代键盘，而是让「记不住快捷键」不再卡住人：屏幕上看得见的标签与按钮
// 都能点，选中的行双击就是它最常用的那个动作。
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	ev := msg.Mouse()

	// 滚轮先判：不同终端把滚轮报成 MouseWheelMsg 或按键 4/5 的点击。
	switch ev.Button {
	case tea.MouseWheelUp:
		return m.scrollBy(-scrollStep)
	case tea.MouseWheelDown:
		return m.scrollBy(scrollStep)
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok {
		return m, nil // 移动/松开事件不处理
	}
	if ev.Button != tea.MouseLeft {
		return m, nil
	}

	// 弹窗盖住了下面的界面，点击只能落到弹窗上。
	// 弹窗按整屏坐标居中（见 overlay），所以这一步必须在扣边距之前。
	if m.prompt != nil || m.confirm != nil || m.help {
		return m.clickModal(ev.X, ev.Y)
	}

	// 界面四周留了一圈边距（见 windowPad*），而鼠标坐标是屏幕坐标：
	// 先减掉它，后面所有命中判定（标签、操作栏、列表行）都继续按界面自身坐标算。
	x, y := ev.X-windowPadX, ev.Y-windowPadY
	switch y {
	case 0:
		return m.clickTab(x)
	case m.innerHeight() - 2:
		if i, ok := m.actionAt(x); ok {
			return m.runAction(i)
		}
		return m, nil
	}
	return m.clickBody(x, y)
}

// scrollStep 是一次滚轮滚过的行数。
const scrollStep = 3

// scrollBy 按方向滚动 rows 行：列表移动光标，日志滚动视口。
//
// 复用按键处理：滚轮与 j/k 走同一条路径，日志的「滚上去就暂停跟随」之类的副作用
// 才不会漏。
func (m Model) scrollBy(rows int) (tea.Model, tea.Cmd) {
	key := "j"
	if rows < 0 {
		key = "k"
		rows = -rows
	}
	cur, last := m, tea.Cmd(nil)
	for i := 0; i < rows; i++ {
		next, cmd := cur.handleKey(pressKey(key))
		if mm, ok := next.(Model); ok {
			cur = mm
		}
		if cmd != nil {
			last = cmd
		}
	}
	return cur, last
}

// clickTab 点击标签行切面板。
func (m Model) clickTab(x int) (tea.Model, tea.Cmd) {
	pos := 0
	for i, t := range tabTitles {
		if i > 0 {
			pos += Width(m.tabSeparator())
		}
		w := Width(fmt.Sprintf(" %d %s ", i+1, t))
		if x >= pos && x < pos+w {
			m.tab = tabID(i)
			return m, nil
		}
		pos += w
	}
	return m, nil
}

// clickBody 把正文里的一次点击翻成「选中某行」；在同一位置再点一次 = 主操作。
func (m Model) clickBody(x, y int) (tea.Model, tea.Cmd) {
	idx, ok := m.rowAt(y)
	if !ok {
		return m, nil
	}
	dbl := m.sameClickPlace(x, y)
	m.lastClickAt, m.lastClickX, m.lastClickY = time.Now(), x, y
	m = m.selectRow(idx)
	if !dbl {
		return m, nil
	}
	// 双击 = 回车：列表里最常用的那个动作（详情看计划、来源进配置、订阅装插件…）。
	return m.handleKey(pressKey("enter"))
}

// sameClickPlace 判断这次点击与上次是否算「双击」。
func (m Model) sameClickPlace(x, y int) bool {
	return x == m.lastClickX && y == m.lastClickY &&
		!m.lastClickAt.IsZero() && time.Since(m.lastClickAt) < doubleClickWindow
}

// rowAt 把屏幕行号翻成列表行下标。
//
// 屏幕从上到下是：标签行(0)、面板上边框(1)、内容(2 起)、操作栏、底栏。内容区里哪些行
// 是「可点的条目」由各面板自己决定（概览/任务的第一行是表头，来源面板还有分组标题）。
func (m Model) rowAt(y int) (int, bool) {
	line := y - 2
	if line < 0 {
		return 0, false
	}
	switch m.tab {
	case tabOverview:
		return rowInTable(line, m.offset, len(m.apps))
	case tabJobs:
		return rowInTable(line, m.jobOffset, len(m.jobs))
	case tabSettings:
		return m.settingsRowAt(line)
	case tabSources:
		return m.sourceRowAt(line)
	}
	return 0, false
}

// rowInTable 处理「表头占一行、然后才是数据」的表格：行号减去表头，再加滚动偏移。
func rowInTable(line, offset, total int) (int, bool) {
	if line < 1 {
		return 0, false // 表头那一行
	}
	idx := line - 1 + offset
	if idx < 0 || idx >= total {
		return 0, false
	}
	return idx, true
}

// selectRow 把某个列表行设为当前选中行。
func (m Model) selectRow(idx int) Model {
	switch m.tab {
	case tabOverview:
		m.cursor = idx
		m.clampCursor()
	case tabJobs:
		m.jobCursor = idx
		m.clampJobCursor()
	case tabSettings:
		m.setCursor = idx
		m.followSettings(m.settingsRows())
	case tabSources:
		if m.feedFor != "" {
			m.feedCursor = idx
			m.clampFeedCursor()
			return m
		}
		if m.cfgFor != "" {
			m.cfgCursor = idx
			m.clampConfigCursor()
			return m
		}
		m.srcCursor = idx
		m.clampSrcCursor()
	}
	return m
}

// ── 弹窗 ──────────────────────────────────────────────────────

// clickModal 处理弹窗上的点击。键盘永远是主力：这里只让「看得见的按钮能点」。
func (m Model) clickModal(x, y int) (tea.Model, tea.Cmd) {
	if m.help {
		m.help = false
		return m, nil
	}
	if m.confirm != nil {
		switch m.confirmButtonAt(x, y) {
		case modalYes:
			return m.handleKey(pressKey("y"))
		case modalNo:
			return m.handleKey(pressKey("n"))
		}
		return m, nil
	}
	// 输入框：点框外等于取消（和按 esc 一样），框内交给输入框自己。
	if m.prompt != nil {
		px, py, pw, ph := m.promptBox()
		if x < px || y < py || x >= px+pw || y >= py+ph {
			return m.handleKey(pressKey("esc"))
		}
	}
	return m, nil
}

// promptBox 返回输入弹窗的矩形（与 viewPrompt 的尺寸算法一致）。
func (m Model) promptBox() (x0, y0, w, h int) {
	p := m.prompt
	if p == nil {
		return 0, 0, 0, 0
	}
	body := p.Label + "\n\n" + p.Input.View() + "\n\n" + "Enter 确认，Esc 取消"
	w = minInt(m.width-8, 72)
	h = countLines(body) + 2
	return (m.width - w) / 2, (m.height - h) / 2, w, h
}

// 确认弹窗上的两个按钮。
const (
	modalNone = iota
	modalYes
	modalNo
)

// modalBox 返回确认弹窗的矩形（左上角与尺寸）。
//
// 几何必须和 viewConfirm 的 Frame 尺寸、theme.Overlay 的居中算法一致 —— 三处表达的
// 是同一件事，所以集中在这里算一次。
func (m Model) modalBox() (x0, y0, w, h int) {
	c := m.confirm
	if c == nil {
		return 0, 0, 0, 0
	}
	body := c.Message + "\n\n" + modalYesLabel + "   " + modalNoLabel
	w = minInt(m.width-8, 72)
	h = countLines(body) + 2
	return (m.width - w) / 2, (m.height - h) / 2, w, h
}

// 确认弹窗上的两个按钮（文案与命中判定共用一份，免得只改一处）。
const (
	modalYesLabel = "[y] 确定"
	modalNoLabel  = "[n] 取消"
)

// confirmButtonAt 判断点击落在确认框的哪个按钮上。
func (m Model) confirmButtonAt(x, y int) int {
	c := m.confirm
	if c == nil {
		return modalNone
	}
	x0, y0, w, h := m.modalBox()
	if x < x0 || x >= x0+w || y < y0 || y >= y0+h {
		return modalNone
	}
	// 按钮行是内容区的最后一行（内容 = 消息 + 空行 + 按钮行）。
	if y != y0+h-2 {
		return modalNone
	}
	start := x0 + 2 // 边框 1 列 + 内边距 1 列
	yesW := Width(modalYesLabel)
	noStart := start + yesW + 3
	switch {
	case x >= start && x < start+yesW:
		return modalYes
	case x >= noStart && x < noStart+Width(modalNoLabel):
		return modalNo
	}
	return modalNone
}
