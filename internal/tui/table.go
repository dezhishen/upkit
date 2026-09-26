package tui

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

// tableHeaderRow 是样式回调里表示「表头」的行号，取自 lipgloss/table 的约定。
const tableHeaderRow = table.HeaderRow

// newTable 造一张与主题一致的表格。
//
// 只保留表头下的一条分隔线：外层已经有板块边框，再给表格加四边会在视觉上套两层框。
//
// 两处关键设置：
//   - 列宽由 lipgloss 按**显示单元**计算（中文按 2 列）。此前用
//     fmt.Sprintf("%-20s", …) 手工对齐，而 fmt 按 rune 计数，中英混排时列会逐格
//     错开，最多差到 9 列。交给组件后这类误差不再存在。
//   - Wrap(false) 让超长内容被截断而不是折行。折行会把一行的视觉高度从 1 变成 2，
//     其后的所有行随之错位 —— 这正是「上次安装不完整」把表格撑坏的原因。
func (m Model) newTable() *table.Table {
	return table.New().
		Border(m.theme.Border()).
		BorderStyle(m.theme.Dim()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(true).
		Wrap(false)
}

// tableCell 给单元格补上左右内边距，让列与列之间留出间隔。
func (m Model) tableCell(s lipgloss.Style) lipgloss.Style {
	return s.Padding(0, 1)
}

// 自由文本列的宽度预算。
//
// 表格在内容总宽超出时会压缩各列，压缩顺序是「先压最大的列」。若某一列的长度
// 不可控（软件名、说明里的错误信息），它会把其余列一起压到退化为 “…” —— 实测
// 名称 24 列 + 说明 40 列就能把「本地版本」压成「本地…」。
//
// 因此不能给固定上界（终端有宽有窄），而要按面板宽度算预算。先扣掉固定列的开销，
// 余量再按 2:3 分给名称与说明；末尾额外留 2 列余量，避免刚好顶到边界。
func textBudget(inner int) (nameMax, noteMax int) {
	// 列宽含左右内边距：光标列 1、状态列 6+2、两个版本列各 8+2。
	const fixed = 1 + 8 + 10 + 10
	// 名称列与说明列自身还各需 2 列内边距，再加 2 列保险。
	rest := inner - fixed - 4 - 2
	if rest < 20 {
		rest = 20
	}
	nameMax = clampInt(rest*2/5, 12, 28)
	noteMax = clampInt(rest-nameMax, 8, 80)
	return nameMax, noteMax
}

// nameCell 按预算规整软件名列。
func nameCell(s string, max int) string { return Truncate(s, max) }

// noteCell 按预算规整说明/备注列。
func noteCell(s string, max int) string { return Truncate(s, max) }

// cursorCell 是光标列的样式：固定 1 列宽、不留内边距。
//
// 表格在内容总宽小于表格宽度时，会把富余宽度优先分给最窄的列，直到各列齐平。
// 光标列只有 1 列宽，若不固定，这一列会被一路撑到与其他列等宽，左边缘出现一大片
// 空白（实测约占面板宽度的六分之一）。
//
// 固定列宽没有公开 API，但 resizer 会把单元格样式的 Width 当作列宽上限，
// 且该宽度**包含内边距**，所以这里同时把内边距清零。
func cursorCell(s lipgloss.Style) lipgloss.Style {
	return s.Padding(0, 0).Width(1)
}
