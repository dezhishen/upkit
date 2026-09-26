// Package tui 是 upkit 的唯一前端（bubbletea）。
//
// 面板：概览 / 详情 / 任务 / 日志 / 设置。所有耗时操作都跑在后台 goroutine，
// 通过 core.EventSink 把事件转成 tea.Msg 回灌给界面，界面永远不阻塞。
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme 控制配色与边框字符，支持 --no-color 与 --ascii 降级。
type Theme struct {
	NoColor bool
	ASCII   bool
	Borders string // unicode（圆角）| square（直角）| ascii（纯 ASCII）
}

// NewTheme 构造主题。
func NewTheme(ascii, noColor bool, borders ...string) Theme {
	t := Theme{ASCII: ascii, NoColor: noColor}
	if len(borders) > 0 {
		t.Borders = borders[0]
	}
	return t
}

func (t Theme) style(fg lipgloss.Color, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle()
	if !t.NoColor && fg != "" {
		s = s.Foreground(fg)
	}
	if bold {
		s = s.Bold(true)
	}
	return s
}

// 颜色
const (
	colPrimary = lipgloss.Color("75")
	colAccent  = lipgloss.Color("212")
	colOK      = lipgloss.Color("42")
	colWarn    = lipgloss.Color("214")
	colErr     = lipgloss.Color("203")
	colDim     = lipgloss.Color("245")
)

// Title 标题。
func (t Theme) Title() lipgloss.Style { return t.style(colAccent, true) }

// Primary 强调文字。
func (t Theme) Primary() lipgloss.Style { return t.style(colPrimary, true) }

// Dim 次要文字。
func (t Theme) Dim() lipgloss.Style { return t.style(colDim, false) }

// OK 成功。
func (t Theme) OK() lipgloss.Style { return t.style(colOK, false) }

// Warn 警告。
func (t Theme) Warn() lipgloss.Style { return t.style(colWarn, false) }

// Err 错误。
func (t Theme) Err() lipgloss.Style { return t.style(colErr, false) }

// Selected 选中行（反色，无颜色时用 ">" 前缀）。
func (t Theme) Selected(text string) string {
	if t.NoColor {
		return text
	}
	return lipgloss.NewStyle().Background(colPrimary).Foreground(lipgloss.Color("232")).Render(text)
}

// Cursor 返回当前行前缀。
func (t Theme) Cursor(focus bool) string {
	if focus {
		return "▌"
	}
	return " "
}

// Border 按主题返回边框样式。
func (t Theme) Border() lipgloss.Border {
	if t.ASCII {
		return asciiBorder()
	}
	switch strings.ToLower(t.Borders) {
	case "ascii":
		return asciiBorder()
	case "square":
		return lipgloss.NormalBorder()
	default:
		return lipgloss.RoundedBorder()
	}
}

func asciiBorder() lipgloss.Border {
	return lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"}
}

// Panel 渲染一个带标题的边框面板。
func (t Theme) Panel(title, body string, width, height int, active bool) string {
	b := t.Border()
	style := lipgloss.NewStyle().Border(b).Padding(0, 1)
	if !t.NoColor && active {
		style = style.BorderForeground(colPrimary)
	}
	if width > 2 {
		style = style.Width(width - 2)
	}
	if height > 2 {
		style = style.Height(height - 2)
	}
	head := ""
	if title != "" {
		head = t.Title().Render(title) + "\n"
	}
	return style.Render(head + body)
}

// Bar 渲染文本进度条。
func (t Theme) Bar(done, total int64, width int) string {
	if width < 8 {
		width = 8
	}
	if total <= 0 {
		return "…"
	}
	ratio := float64(done) / float64(total)
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(width))
	if t.NoColor {
		return "[" + repeat("=", filled) + repeat(" ", width-filled) + "]"
	}
	on := lipgloss.NewStyle().Foreground(colOK).Render(repeat("█", filled))
	off := lipgloss.NewStyle().Foreground(colDim).Render(repeat("░", width-filled))
	return on + off
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
