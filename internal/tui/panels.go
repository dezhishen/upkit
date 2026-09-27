package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/util"
)

// View 渲染整个界面。
//
// bubbletea v2 把终端特性从 Program 选项改成了 View 的字段：备用屏幕不再是
// tea.WithAltScreen()，而是这里声明。窗口标题也在此设置 —— 标题栏/标签页显示
// 的就是它，因此界面内不再重复工具名与版本号。
//
// 尺寸未知（某些终端不会上报 TIOCGWINSZ）时退回 80x24，而不是空屏。
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "upkit " + m.Version()
	if !m.opts.NoMouse {
		// 只开「单元格移动」级别：够用（点击/滚轮），事件量又比全量移动小得多。
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// render 产出界面正文。与 View 分开是为了让渲染逻辑可以脱离 bubbletea 直接测试。
//
// 弹窗不再整屏替换正文，而是作为图层压在正文之上：底下的界面留在原处（略微
// 变淡），操作对象一目了然。这也是 v2 图层合成的直接用法。
func (m Model) render() string {
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}

	head := m.viewHeader(m.width)
	actions, _ := m.viewActions(m.width)
	foot := m.viewFooter(m.width)
	bodyH := m.bodyHeight()
	base := head + "\n" + m.viewBody(m.width, bodyH) + "\n" + actions + "\n" + foot

	switch {
	case m.prompt != nil:
		return m.overlay(base, m.viewPrompt())
	case m.confirm != nil:
		return m.overlay(base, m.viewConfirm())
	case m.help:
		return m.overlay(base, m.viewHelp())
	}
	return base
}

// overlay 把弹窗居中压在正文上，正文整体调暗。
func (m Model) overlay(base, modal string) string {
	return m.theme.Overlay(m.theme.Dimmed(base), modal, m.width, m.height)
}

// bodyHeight 返回正文面板的可用高度：总高减去头部、操作栏与底栏各占的行数。
//
// 单独拿出来是因为按键处理也得知道「一屏能放几行」—— 设置面板要据此把光标留在可见
// 范围内，而按键处理里根本没有渲染时的那个 bodyH。
func (m Model) bodyHeight() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	body := h - countLines(m.viewHeader(w)) - actionsHeight(m, w) - countLines(m.viewFooter(w))
	if body < 4 {
		body = 4
	}
	return body
}

// actionsHeight 返回操作栏占的行数（没有可用操作时为零行）。
func actionsHeight(m Model, w int) int {
	line, _ := m.viewActions(w)
	return countLines(line)
}
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// ── 头部 ──────────────────────────────────────────────────────

// viewHeader 渲染面板标签行（头部仅此一行）。
//
// 工具名与版本号已由窗口标题承载，这里不再重复。统计信息排在标签行右端：
// 单独为它占一行会让左半屏全空，而合并后头部只占一行。
// 宽度不足以同时容下两者时舍弃统计 —— 「当前在哪个面板」比计数重要。
func (m Model) viewHeader(width int) string {
	sep := m.theme.Dim().Render(m.tabSeparator())
	tabs := make([]string, 0, len(tabTitles))
	for i, t := range tabTitles {
		label := fmt.Sprintf(" %d %s ", i+1, t)
		if tabID(i) == m.tab {
			tabs = append(tabs, m.theme.SelectedRow().Render(label))
		} else {
			tabs = append(tabs, m.theme.Dim().Render(label))
		}
	}
	line := strings.Join(tabs, sep)
	if m.ctrl != nil && m.ctrl.SettingsDirty() {
		line += m.theme.Warn().Render("  ● 设置未保存")
	}

	counts := m.counts()
	if counts != "" {
		counts = m.theme.Dim().Render(counts)
		if gap := width - Width(line) - Width(counts); gap >= 2 {
			line += strings.Repeat(" ", gap) + counts
		}
	}
	// 兜底：极窄的窗口里，标签行自己也放不下。截断好过换行 —— 换行会让头部
	// 占两行，而高度预算按一行算，正文会被挤掉。
	if Width(line) > width {
		line = Truncate(line, width)
	}
	return line
}

// tabSeparator 用主题自己的竖线分隔标签，ASCII 主题下自动退化为 |。
//
// 两侧不再补空格：标签本身已带前后各一个空格，再补会拉成双倍间隔。
func (m Model) tabSeparator() string {
	if v := m.theme.Border().Left; v != "" {
		return v
	}
	return "|"
}

func (m Model) counts() string {
	if len(m.apps) == 0 {
		return ""
	}
	var upd, inst, pend int
	for _, a := range m.apps {
		if a.Status.Installed {
			inst++
		}
		switch a.Action {
		case core.ActionUpdate:
			upd++
		case core.ActionInstall:
			pend++
		}
	}
	return fmt.Sprintf("已安装 %d · 可更新 %d · 待安装 %d", inst, upd, pend)
}

func (m Model) viewBody(w, height int) string {
	switch m.tab {
	case tabOverview:
		return m.viewOverview(w, height)
	case tabDetail:
		return m.viewDetail(w, height)
	case tabJobs:
		return m.viewJobs(w, height)
	case tabLogs:
		return m.viewLogs(w, height)
	case tabSettings:
		return m.viewSettings(w, height)
	case tabSources:
		return m.viewSources(w, height)
	}
	return ""
}

// ── 概览 ──────────────────────────────────────────────────────

// 概览表的列序，供样式回调按列着色。
const (
	colCursor = iota
	colName
	colState
	colLocal
	colUpstream
	colNote
)

func (m Model) viewOverview(w, height int) string {
	if m.busy && len(m.apps) == 0 {
		return m.theme.Frame("概览", m.spinnerText()+" 正在检查上游版本…", w, height, true)
	}
	if len(m.apps) == 0 {
		return m.theme.Frame("概览",
			"还没有任何软件。\n\n到「来源」面板（点上面的标签或按 6），\n在底部操作栏里选「官方源」，软件会随源一起出现。",
			w, height, true)
	}

	rows := make([][]string, 0, len(m.apps))
	nameMax, noteMax := textBudget(w - 4)
	for i, a := range m.apps {
		rows = append(rows, m.overviewRow(i, a, nameMax, noteMax))
	}

	tbl := m.newTable().
		Headers(" ", "软件", "状态", "本地版本", "上游版本", "说明").
		Rows(rows...).
		StyleFunc(m.overviewStyle).
		Width(w - 4).
		Height(height - 2).
		YOffset(m.offset).
		String()

	return m.theme.Frame("概览", tbl, w, height, true)
}

// overviewRow 把一条软件整理成表格行。
//
// 说明列优先展示错误：检查失败比「有新版本」更值得占用这一列。
func (m Model) overviewRow(i int, a *engine.App, nameMax, noteMax int) []string {
	name := a.Ref.DisplayName()
	if !appEnabled(a, m.ctrl) {
		name += "（停用）"
	}
	note := a.Note
	if a.CheckErr != nil {
		note = "检查失败：" + a.CheckErr.Error()
	}
	return []string{
		m.theme.Cursor(i == m.cursor),
		nameCell(name, nameMax),
		actionLabel(a),
		orDash(a.Status.Version),
		orDash(a.Release.Version),
		noteCell(note, noteMax),
	}
}

// overviewStyle 按语义给单元格着色。
//
// 不再整行染色：一屏里多个「可更新」会把橙黄变成背景噪声，颜色随之失去区分度。
// 颜色只落在状态列与出错行上，正文保持中性，靠选中底色指示当前位置。
func (m Model) overviewStyle(row, col int) lipgloss.Style {
	if col == colCursor {
		if row != tableHeaderRow && row == m.cursor {
			return cursorCell(m.theme.SelectedRow())
		}
		return cursorCell(lipgloss.NewStyle())
	}
	if row == tableHeaderRow {
		return m.tableCell(m.theme.Header())
	}
	if row < 0 || row >= len(m.apps) {
		return m.tableCell(lipgloss.NewStyle())
	}
	if row == m.cursor {
		// 内边距一并染上底色，高亮才是完整色块（漏掉内边距会让首列贴上光标符）。
		return m.tableCell(m.theme.SelectedRow())
	}
	a := m.apps[row]
	if !appEnabled(a, m.ctrl) || a.Shadowed {
		return m.tableCell(m.theme.Dim())
	}
	switch {
	case a.CheckErr != nil && col == colNote:
		return m.tableCell(m.theme.Err())
	case col == colState && a.Action != core.ActionNoOp:
		return m.tableCell(m.theme.Warn())
	}
	return m.tableCell(lipgloss.NewStyle())
}

func actionLabel(a *engine.App) string {
	if a == nil {
		return ""
	}
	if a.Shadowed {
		return "冲突"
	}
	switch a.Action {
	case core.ActionInstall:
		return "待装"
	case core.ActionUpdate:
		return "可更新"
	case core.ActionReinstall:
		return "重装"
	case core.ActionUninstall:
		return "卸载"
	default:
		return "最新"
	}
}

// ── 详情 ──────────────────────────────────────────────────────

func (m Model) viewDetail(w, height int) string {
	a := m.current()
	if a == nil {
		return m.theme.Frame("详情", "请先在概览里选择一个软件。", w, height, true)
	}
	var b strings.Builder
	line := func(k, v string) {
		// 标签列用 Cell 对齐：它按显示宽度补空格，中文标签也能对齐。
		fmt.Fprintf(&b, "%s %s\n", m.theme.Dim().Render(Cell(k, 10)), v)
	}
	section := func(title string) {
		b.WriteString("\n")
		b.WriteString(m.theme.Primary().Render(title))
		b.WriteString("\n")
	}

	line("名称", a.Ref.DisplayName())
	line("标识", a.Ref.ID)
	line("启用", yesNo(appEnabled(a, m.ctrl)))
	line("安装方式", a.Ref.Method)
	line("来源", a.Ref.Source)
	line("解包", a.Ref.Unpack)
	line("探测链", strings.Join(a.Ref.Detect, " → "))
	line("安装目录", orDash(a.Ref.InstallPath))
	line("入口文件", orDash(strings.Join(a.Ref.Entrypoints, ", ")))
	line("保护路径", orDash(strings.Join(a.Ref.Preserve, ", ")))
	line("固定版本", orDash(a.Ref.Pin))

	section("版本")
	line("本地", orDash(a.Status.Version)+"（来源 "+orDash(a.Status.Source)+"）")
	line("上游", orDash(a.Release.Version)+"  发布 "+fmtTime(a.Release.PublishedAt))
	line("本次动作", string(a.Action))
	line("说明", orDash(a.Note))
	if a.CheckErr != nil {
		line("检查错误", m.theme.Err().Render(a.CheckErr.Error()))
	}
	if a.Shadowed {
		line("冲突", m.theme.Warn().Render(a.Note))
	}
	if len(a.Release.Artifacts) > 0 {
		art := a.Release.Artifacts[0]
		line("产物", fmt.Sprintf("%s（%s）", art.Name, humanSize(art.Size)))
		if art.Digest != "" {
			line("上游摘要", Truncate(art.Digest, 30))
		}
	}

	if len(a.Backups) > 0 {
		section("备份")
		for i := len(a.Backups) - 1; i >= 0; i-- {
			bk := a.Backups[i]
			fmt.Fprintf(&b, "  %s  %s  %s\n", bk.CreatedAt.Local().Format("2006-01-02 15:04"),
				orDash(bk.Version), humanSize(bk.Size))
		}
	}

	if m.plan != nil && m.plan.App.ID == a.Ref.ID {
		section("执行计划")
		for i, s := range m.plan.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s.Desc)
			if len(s.Command) > 0 {
				fmt.Fprintf(&b, "%s\n", m.theme.Dim().Render("     "+strings.Join(s.Command, " ")))
			}
		}
		if len(m.plan.Steps) == 0 {
			b.WriteString("  （无步骤）\n")
		}
	}

	return m.theme.Frame("详情",
		m.scroll(strings.TrimRight(b.String(), "\n"), height-2), w, height, true)
}

// ── 任务 ──────────────────────────────────────────────────────

const (
	jobColCursor = iota
	jobColName
	jobColState
	jobColPhase
	jobColBar
	jobColPct
	jobColSpeed
	jobColNote
)

func (m Model) viewJobs(w, height int) string {
	if len(m.jobs) == 0 {
		return m.theme.Frame("任务",
			"暂无任务。\n\n在概览里按 u 更新选中软件，或按 U 批量更新。", w, height, true)
	}

	rows := make([][]string, 0, len(m.jobs))
	nameMax, noteMax := textBudget(w - 4)
	for i, j := range m.jobs {
		rows = append(rows, m.jobRow(i, j, nameMax, noteMax))
	}

	tbl := m.newTable().
		Headers(" ", "软件", "状态", "阶段", "进度", "完成", "速度", "备注").
		Rows(rows...).
		StyleFunc(m.jobStyle).
		Width(w - 4).
		Height(height - 2).
		YOffset(m.jobOffset).
		String()

	return m.theme.Frame("任务", tbl, w, height, true)
}

func (m Model) jobRow(i int, j *jobItem, nameMax, noteMax int) []string {
	bar, pct := "", ""
	if j.Total > 0 {
		bar = m.theme.Bar(j.Done, j.Total, 14)
		pct = fmt.Sprintf("%5.1f%%", float64(j.Done)/float64(j.Total)*100)
	}
	note := ""
	if j.Err != nil {
		note = j.Err.Error()
	}
	return []string{
		m.theme.Cursor(i == m.jobCursor),
		nameCell(j.Name, nameMax),
		j.State,
		j.Phase,
		bar,
		pct,
		util.HumanSpeed(j.Speed),
		noteCell(note, noteMax),
	}
}

func (m Model) jobStyle(row, col int) lipgloss.Style {
	if col == jobColCursor {
		if row != tableHeaderRow && row == m.jobCursor {
			return cursorCell(m.theme.SelectedRow())
		}
		return cursorCell(lipgloss.NewStyle())
	}
	if row == tableHeaderRow {
		return m.tableCell(m.theme.Header())
	}
	if row < 0 || row >= len(m.jobs) {
		return m.tableCell(lipgloss.NewStyle())
	}
	if row == m.jobCursor {
		return m.tableCell(m.theme.SelectedRow())
	}
	j := m.jobs[row]
	if j.Err != nil && col == jobColNote {
		return m.tableCell(m.theme.Err())
	}
	if col == jobColState {
		switch j.State {
		case "失败":
			return m.tableCell(m.theme.Err())
		case "完成":
			return m.tableCell(m.theme.OK())
		}
	}
	return m.tableCell(lipgloss.NewStyle())
}

// ── 日志 ──────────────────────────────────────────────────────

func (m Model) viewLogs(w, height int) string {
	if !m.logReady {
		m.resizeLogView()
	}
	head := fmt.Sprintf("级别 ≥ %s", m.logLevel)
	if m.logFilter != "" {
		head += " · 关键字 " + m.logFilter
	}
	if !m.logFollow {
		head += " · 已暂停跟随（滚回去看历史）"
	}
	return m.theme.Frame("日志（"+head+"）", m.logView.View(), w, height, true)
}

// ── 设置 ──────────────────────────────────────────────────────

func (m Model) viewSettings(w, height int) string {
	rows := m.settingsRows()
	lines := m.settingsLines(rows, w)
	// 光标停在末项时窗口贴底：末项之后还有只读的路径信息，不贴底就永远看不到那几行。
	sticky := len(rows) > 0 && m.setCursor >= len(rows)-1
	body := m.scrollWindow(lines, m.setOffset, sticky, height-2)
	return m.theme.Frame("设置",
		strings.Join(body, "\n"), w, height, true)
}

// settingsLines 把设置表单与末尾只读的路径信息摊平成可直接裁剪的行。
//
// 摊平后再滚动，而不是让表格组件去滚：路径信息是分组的文本，不是表格行，而光标只需
// 要对准表单项（路径那一段没有可操作的东西）。
func (m Model) settingsLines(rows []control.SettingItem, w int) []string {
	lines := make([]string, 0, len(rows)+8)
	for i, f := range rows {
		value := f.Text
		if f.Kind == control.SettingBool || f.Kind == control.SettingEnum {
			value = "‹ " + value + " ›"
		}
		// 标签列固定 26 列；Cell 按显示宽度补齐，中英混排也能对齐。
		row := m.theme.Cursor(i == m.setCursor) + " " + Cell(f.Label, 26) + " " + value
		if i == m.setCursor {
			// 铺满内容区（面板宽 - 左右边框 - 左右内边距），高亮才是一条完整色块。
			row = m.theme.SelectedRow().Render(Cell(row, w-4))
		}
		lines = append(lines, row)
	}

	lines = append(lines, "")
	lines = append(lines, m.theme.Primary().Render("路径（只读 · 根目录由启动方式决定）"))
	for _, kv := range m.settingsPaths() {
		lines = append(lines, fmt.Sprintf("  %s %s", m.theme.Dim().Render(Cell(kv[0], 12)), kv[1]))
	}
	if m.settingsDirty() {
		lines = append(lines, "", m.theme.Warn().Render("有未保存的修改，按 s 保存。"))
	}
	return lines
}

// ── 弹窗 ──────────────────────────────────────────────────────

func (m Model) viewConfirm() string {
	c := m.confirm
	body := c.Message + "\n\n" + m.theme.OK().Render(modalYesLabel) + "   " + m.theme.Err().Render(modalNoLabel)
	w := minInt(m.width-8, 72)
	return m.theme.Frame(c.Title, body, w, countLines(body)+2, true)
}

func (m Model) viewPrompt() string {
	// 取副本：下面要改样式，不能影响模型里保存的原件（否则用户改回彩色就回不去了）。
	p := *m.prompt
	if m.theme.NoColor {
		p.Input.SetStyles(plainTextInputStyles())
	}
	body := p.Label + "\n\n" + p.Input.View() + "\n\n" + m.theme.Dim().Render("Enter 确认，Esc 取消")
	w := minInt(m.width-8, 72)
	return m.theme.Frame(p.Title, body, w, countLines(body)+2, true)
}

// viewHelp 渲染 ? 面板：按组标题 + 「键名/说明」两列排。
//
// 键位条数比一屏多，所以这里也走滚动窗口（↑↓/PgUp/PgDn 翻，esc 退出）—— 面板自己
// 长得超过屏幕、底下的键位被裁掉却看不出来，那正好是这轮要修的那类问题。
func (m Model) viewHelp() string {
	const keyW = 20
	lines := make([]string, 0, 48)
	for gi, g := range m.keys.groups() {
		if gi > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.theme.Primary().Render(g.title))
		for _, it := range g.items {
			h := it.Help()
			lines = append(lines, "  "+m.theme.Dim().Render(Cell(h.Key, keyW))+" "+h.Desc)
		}
	}
	// 鼠标单独写一段：它的操作方式用键名表格说不清楚。
	lines = append(lines, "",
		m.theme.Primary().Render("鼠标"),
		"  "+m.theme.Dim().Render("点标签切面板、点一行选中、同一行再点一次打开/编辑"),
		"  "+m.theme.Dim().Render("滚轮滚动列表、点操作栏按钮、点弹窗上的确定/取消"),
		"  "+m.theme.Dim().Render("用 --no-mouse 启动可关掉鼠标上报，恢复终端原生拖选"),
	)

	w := minInt(m.width-8, 72)
	// 弹窗不能比屏幕高：高了 Overlay 就不再居中，内容直接溢出终端。
	frameH := minInt(len(lines)+3, m.height-2)
	if frameH < 5 {
		frameH = 5
	}
	body := m.scrollWindow(lines, m.helpOffset, false, frameH-2)
	title := "快捷键（↑↓ 翻看 · esc 返回）"
	return m.theme.Frame(title, strings.Join(body, "\n"), w, frameH, true)
}

func (m Model) viewFooter(width int) string {
	left := m.status
	if m.fatal != nil {
		left = m.theme.Err().Render("错误：" + m.fatal.Error())
	} else if m.busy {
		left = m.spinnerText() + " " + left
	}
	// 状态文字可能是任意长的错误信息（引擎、网络、插件都能抛出长句），
	// 先按终端宽度裁一次，否则它自己就会把整行撑破。
	if maxLeft := width - 8; maxLeft > 0 && Width(left) > maxLeft {
		left = Truncate(left, maxLeft)
	}
	// 把剩余宽度交给 help 组件，由它按宽度自行截断并补省略号。
	// 不设宽度（默认 0）时组件不做任何截断，窄终端下提示会换行，把正文挤掉一行。
	avail := width - Width(left) - 1
	if avail < 1 {
		avail = 1
	}
	m.helpView.SetWidth(avail)
	hint := m.helpView.ShortHelpView(m.keys.ShortHelp())
	// help 有个边界缺陷：当已累计的宽度恰好等于上限时，省略号本身放不下，
	// 它会继续往下追加而不是停下，于是整行溢出。这里用官方 ansi.Truncate
	// 兜一层（按显示宽度、且不会截断转义序列），保证任何宽度都不溢出。
	if Width(hint) > avail {
		hint = Truncate(hint, avail)
	}
	pad := avail - Width(hint)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + m.theme.Dim().Render(hint)
}

// ── 小工具 ────────────────────────────────────────────────────

func (m Model) scroll(content string, height int) string {
	if height <= 0 {
		return content
	}
	return strings.Join(m.scrollWindow(strings.Split(content, "\n"), m.detailY, false, height), "\n")
}

// scrollWindow 从 lines 里裁出可见窗口：offset 是窗口起点，sticky 为真时贴到底部。
//
// 需要滚动时必须留一行给底部的「… n/m」提示。以前是「先切 height 行、再补一行提示」，
// 而内容区正好只有 height 行，提示被边框截掉 —— 于是「下面还有内容」这件事在屏幕上
// 完全看不出来：设置项超出屏幕后只剩半屏，也没有任何可滚动的迹象。
func (m Model) scrollWindow(lines []string, offset int, sticky bool, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	visible := height - 1
	max := len(lines) - visible
	if sticky {
		offset = max
	}
	if offset > max {
		offset = max
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]string, 0, visible+1)
	out = append(out, lines[offset:offset+visible]...)
	out = append(out, m.theme.Dim().Render(fmt.Sprintf("… %d/%d", offset+1, len(lines))))
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return t.Local().Format("2006-01-02")
	}
	return fmt.Sprintf("%s（%s前）", t.Local().Format("2006-01-02"), util.HumanDuration(d))
}
