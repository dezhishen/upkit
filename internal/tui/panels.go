package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/util"
)

// View 渲染整个界面。
//
// 尺寸未知（某些终端不会上报 TIOCGWINSZ）时退回 80x24，而不是空屏。
func (m Model) View() string {
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}
	if m.help {
		return m.viewHelp()
	}
	if m.confirm != nil {
		return m.viewConfirm()
	}
	if m.prompt != nil {
		return m.viewPrompt()
	}
	head := m.viewHeader()
	foot := m.viewFooter(m.width)
	bodyH := m.height - countLines(head) - countLines(foot) - 1
	if bodyH < 4 {
		bodyH = 4
	}
	body := m.viewBody(m.width, bodyH)
	return head + "\n" + body + "\n" + foot
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (m Model) viewHeader() string {
	var b strings.Builder
	title := m.theme.Title().Render("upkit ") + m.theme.Dim().Render(m.Version())
	counts := m.counts()
	if counts != "" {
		title += m.theme.Dim().Render("   " + counts)
	}
	b.WriteString(title)
	b.WriteString("\n")

	var tabs []string
	for i, t := range tabTitles {
		label := fmt.Sprintf("%d %s", i+1, t)
		if tabID(i) == m.tab {
			tabs = append(tabs, m.theme.Selected(" "+label+" "))
		} else {
			tabs = append(tabs, m.theme.Dim().Render(" "+label+" "))
		}
	}
	b.WriteString(strings.Join(tabs, ""))
	if m.setDirty {
		b.WriteString(m.theme.Warn().Render("  ● 设置未保存"))
	}
	return b.String()
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

func (m Model) viewOverview(w, height int) string {
	if m.busy && len(m.apps) == 0 {
		return m.theme.Panel("概览", m.spinnerText()+" 正在检查上游版本…", w, height, true)
	}
	if len(m.apps) == 0 {
		return m.theme.Panel("概览", "还没有任何软件。\n\n按 6 到「来源」面板，再按 o 添加官方源，\n软件会随源一起出现。",
			w, height, true)
	}

	var b strings.Builder
	header := fmt.Sprintf("%-1s %-20s %-8s %-16s %-16s %s", "", "软件", "状态", "本地版本", "上游版本", "说明")
	b.WriteString(m.theme.Dim().Render(header))
	b.WriteString("\n")

	visible := height - 4
	if visible < 1 {
		visible = 1
	}
	end := m.offset + visible
	if end > len(m.apps) {
		end = len(m.apps)
	}
	for i := m.offset; i < end; i++ {
		a := m.apps[i]
		focus := i == m.cursor
		enabled := appEnabled(a, m.afs)
		name := a.Ref.DisplayName()
		if !enabled {
			name += "（停用）"
		}
		state := actionLabel(a)
		local := orDash(a.Status.Version)
		upstream := orDash(a.Release.Version)
		note := a.Note
		if a.CheckErr != nil {
			note = "检查失败：" + util.Truncate(a.CheckErr.Error(), 40)
		}
		row := fmt.Sprintf("%-1s %-20s %-8s %-16s %-16s %s",
			m.theme.Cursor(focus), util.Truncate(name, 20), state, local, upstream, note)
		if focus {
			row = m.theme.Selected(row)
		} else if !enabled || a.Shadowed {
			row = m.theme.Dim().Render(row)
		} else if a.Action == core.ActionUpdate || a.Action == core.ActionInstall {
			row = m.theme.Warn().Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	if len(m.apps) > visible {
		fmt.Fprintf(&b, "%s\n", m.theme.Dim().Render(fmt.Sprintf("… 第 %d/%d 行", m.cursor+1, len(m.apps))))
	}
	return m.theme.Panel("概览", b.String(), w, height, true)
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
		return m.theme.Panel("详情", "请先在概览里选择一个软件。", w, height, true)
	}
	var b strings.Builder
	line := func(k, v string) {
		fmt.Fprintf(&b, "%s %s\n", m.theme.Dim().Render(fmt.Sprintf("%-12s", k)), v)
	}
	line("名称", a.Ref.DisplayName())
	line("标识", a.Ref.ID)
	line("启用", yesNo(appEnabled(a, m.afs)))
	line("安装方式", a.Ref.Method)
	line("来源", a.Ref.Source)
	line("解包", a.Ref.Unpack)
	line("探测链", strings.Join(a.Ref.Detect, " → "))
	line("安装目录", a.Ref.InstallPath)
	line("入口文件", strings.Join(a.Ref.Entrypoints, ", "))
	line("保护路径", strings.Join(a.Ref.Preserve, ", "))
	line("固定版本", orDash(a.Ref.Pin))
	b.WriteString("\n")
	line("本地版本", orDash(a.Status.Version)+"（来源 "+orDash(a.Status.Source)+"）")
	line("上游版本", orDash(a.Release.Version)+"  发布时间 "+fmtTime(a.Release.PublishedAt))
	line("本次动作", string(a.Action))
	line("说明", a.Note)
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
			line("上游摘要", util.Truncate(art.Digest, 30))
		}
	}
	if len(a.Backups) > 0 {
		b.WriteString("\n")
		b.WriteString(m.theme.Primary().Render("备份"))
		b.WriteString("\n")
		for i := len(a.Backups) - 1; i >= 0; i-- {
			bk := a.Backups[i]
			fmt.Fprintf(&b, "  %s  %s  %s\n", bk.CreatedAt.Local().Format("2006-01-02 15:04"),
				orDash(bk.Version), humanSize(bk.Size))
		}
	}
	if m.plan != nil && m.plan.App.ID == a.Ref.ID {
		b.WriteString("\n")
		b.WriteString(m.theme.Primary().Render("执行计划"))
		b.WriteString("\n")
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
	return m.theme.Panel("详情（p 生成计划，u 立即执行）", m.scroll(b.String(), height-2), w, height, true)
}

// ── 任务 ──────────────────────────────────────────────────────

func (m Model) viewJobs(w, height int) string {
	var b strings.Builder
	if len(m.jobs) == 0 {
		b.WriteString("暂无任务。在概览里按 u 更新选中软件，或按 U 批量更新。\n")
	} else {
		visible := height - 4
		if visible < 1 {
			visible = 1
		}
		start := 0
		if m.jobCursor >= visible {
			start = m.jobCursor - visible + 1
		}
		end := start + visible
		if end > len(m.jobs) {
			end = len(m.jobs)
		}
		for i := start; i < end; i++ {
			j := m.jobs[i]
			focus := i == m.jobCursor
			prefix := m.theme.Cursor(focus)
			pct := ""
			bar := ""
			if j.Total > 0 {
				pct = fmt.Sprintf("%5.1f%%", float64(j.Done)/float64(j.Total)*100)
				bar = m.theme.Bar(j.Done, j.Total, 20)
			}
			line := fmt.Sprintf("%-1s %-20s %-6s %-8s %s %s %s",
				prefix, util.Truncate(j.Name, 20), j.State, j.Phase, bar, pct, util.HumanSpeed(j.Speed))
			if j.Err != nil {
				line += "  " + m.theme.Err().Render(util.Truncate(j.Err.Error(), 40))
			}
			if focus {
				line = m.theme.Selected(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return m.theme.Panel("任务（d 清除已完成）", b.String(), w, height, true)
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
	return m.theme.Panel("日志（"+head+"，f 切级别，F 关键字）", m.logView.View(), w, height, true)
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
		row := fmt.Sprintf("%-1s %-24s %s", m.theme.Cursor(focus), f.Label, value)
		if focus {
			row = m.theme.Selected(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Primary().Render("路径"))
	b.WriteString("\n")
	for _, kv := range m.paths() {
		fmt.Fprintf(&b, "%s %s\n", m.theme.Dim().Render(fmt.Sprintf("  %-10s", kv[0])), kv[1])
	}
	if m.setDirty {
		b.WriteString("\n")
		b.WriteString(m.theme.Warn().Render("有未保存的修改，按 s 保存。"))
	}
	return m.theme.Panel("设置（←/→ 调整，s 保存，R 恢复默认）", m.scroll(b.String(), height-2), w, height, true)
}

// ── 弹窗 ──────────────────────────────────────────────────────

func (m Model) viewConfirm() string {
	c := m.confirm
	body := c.Message + "\n\n" + m.theme.OK().Render("[y] 确定") + "   " + m.theme.Err().Render("[n] 取消")
	w := minInt(m.width-4, 72)
	return m.theme.Panel(c.Title, body, w, countLines(body)+4, true)
}

func (m Model) viewPrompt() string {
	p := m.prompt
	body := p.Label + "\n\n" + p.Input.View() + "\n\n" + m.theme.Dim().Render("Enter 确认，Esc 取消")
	w := minInt(m.width-4, 72)
	return m.theme.Panel(p.Title, body, w, countLines(body)+4, true)
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.helpView.View(m.keys))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Dim().Render("按任意键返回"))
	return m.theme.Panel("快捷键", b.String(), minInt(m.width-4, 72), countLines(b.String())+4, true)
}

func (m Model) viewFooter(width int) string {
	left := m.status
	if m.fatal != nil {
		left = m.theme.Err().Render("错误：" + m.fatal.Error())
	} else if m.busy {
		left = m.spinnerText() + " " + left
	}
	hint := m.helpView.ShortHelpView(m.keys.ShortHelp())
	pad := width - lipgloss.Width(left) - lipgloss.Width(hint)
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
