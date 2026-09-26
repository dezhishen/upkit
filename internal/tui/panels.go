package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/util"
)

// View 渲染整个界面。
//
// bubbletea v2 把终端特性从 Program 选项改成了 View 的字段：备用屏幕不再是
// tea.WithAltScreen()，而是在这里声明。
//
// 尺寸未知（某些终端不会上报 TIOCGWINSZ）时退回 80x24，而不是空屏。
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
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
	foot := m.viewFooter(m.width)
	bodyH := m.height - countLines(head) - countLines(foot)
	if bodyH < 4 {
		bodyH = 4
	}
	base := head + "\n" + m.viewBody(m.width, bodyH) + "\n" + foot

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

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// ── 头部 ──────────────────────────────────────────────────────

// viewHeader 渲染标题行与面板标签行。
//
// 计数信息右对齐：左标题右统计是通行的读法，也让宽度变化时两行都稳定。
func (m Model) viewHeader(width int) string {
	left := m.theme.Title().Render("upkit") + " " + m.theme.Dim().Render(m.Version())
	counts := m.counts()
	if counts != "" {
		gap := width - Width(left) - Width(counts)
		if gap < 1 {
			gap = 1
		}
		left += strings.Repeat(" ", gap) + m.theme.Dim().Render(counts)
	}

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
	if m.setDirty {
		line += m.theme.Warn().Render("  ● 设置未保存")
	}
	return left + "\n" + line
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
			"还没有任何软件。\n\n按 6 到「来源」面板，再按 o 添加官方源，\n软件会随源一起出现。",
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

	return m.theme.Frame("概览（c 检查 · u 更新 · space 停用）", tbl, w, height, true)
}

// overviewRow 把一条软件整理成表格行。
//
// 说明列优先展示错误：检查失败比「有新版本」更值得占用这一列。
func (m Model) overviewRow(i int, a *engine.App, nameMax, noteMax int) []string {
	name := a.Ref.DisplayName()
	if !appEnabled(a, m.afs) {
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
	if !appEnabled(a, m.afs) || a.Shadowed {
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
	line("启用", yesNo(appEnabled(a, m.afs)))
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

	return m.theme.Frame("详情（p 生成计划 · u 立即执行）",
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

	return m.theme.Frame("任务（d 清除已完成）", tbl, w, height, true)
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
		head += " · 已暂停跟随（G 恢复）"
	}
	return m.theme.Frame("日志（"+head+" · f 切级别 · F 关键字）", m.logView.View(), w, height, true)
}

// ── 设置 ──────────────────────────────────────────────────────

func (m Model) viewSettings(w, height int) string {
	var b strings.Builder
	for i, f := range m.fields() {
		focus := i == m.setCursor
		value := f.Value()
		if f.Kind == kindBool || f.Kind == kindEnum {
			value = "‹ " + value + " ›"
		}
		// 标签列固定 26 列；Cell 按显示宽度补齐，中英混排也能对齐。
		row := m.theme.Cursor(focus) + " " + Cell(f.Label, 26) + " " + value
		if focus {
			// 铺满内容区（面板宽 - 左右边框 - 左右内边距），高亮才是一条完整色块。
			row = m.theme.SelectedRow().Render(Cell(row, w-4))
		}
		b.WriteString(row)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.theme.Primary().Render("路径"))
	b.WriteString("\n")
	for _, kv := range m.paths() {
		fmt.Fprintf(&b, "  %s %s\n", m.theme.Dim().Render(Cell(kv[0], 12)), kv[1])
	}
	if m.setDirty {
		b.WriteString("\n")
		b.WriteString(m.theme.Warn().Render("有未保存的修改，按 s 保存。"))
	}
	return m.theme.Frame("设置（←/→ 调整 · s 保存 · R 恢复默认）",
		m.scroll(strings.TrimRight(b.String(), "\n"), height-2), w, height, true)
}

// ── 弹窗 ──────────────────────────────────────────────────────

func (m Model) viewConfirm() string {
	c := m.confirm
	body := c.Message + "\n\n" + m.theme.OK().Render("[y] 确定") + "   " + m.theme.Err().Render("[n] 取消")
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

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.helpView.View(m.keys))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Dim().Render("按任意键返回"))
	w := minInt(m.width-8, 72)
	return m.theme.Frame("快捷键", b.String(), w, countLines(b.String())+2, true)
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
	lines := strings.Split(content, "\n")
	if height <= 0 {
		return content
	}
	max := len(lines) - height
	if max < 0 {
		max = 0
	}
	if m.detailY > max {
		m.detailY = max
	}
	if m.detailY < 0 {
		m.detailY = 0
	}
	if m.detailY >= len(lines) {
		return content
	}
	end := m.detailY + height
	if end > len(lines) {
		end = len(lines)
	}
	out := lines[m.detailY:end]
	if max > 0 {
		out = append(out, m.theme.Dim().Render(fmt.Sprintf("… %d/%d", m.detailY+1, max+1)))
	}
	return strings.Join(out, "\n")
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
