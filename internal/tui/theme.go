// Package tui 是 upkit 的唯一前端（bubbletea v2）。
//
// 面板：概览 / 详情 / 任务 / 日志 / 设置 / 来源。所有耗时操作都跑在后台
// goroutine，通过 core.EventSink 把事件转成 tea.Msg 回灌给界面，界面永不阻塞。
//
// 布局一律交给 Charm v2 的官方组件：表格用 lipgloss/table（列宽按显示单元计算），
// 板块边框由 lipgloss 生成、标题用图层叠在上边框，弹窗用 Compositor 合成在背景之上。
// 本包只负责把数据整理成组件要的形状。
package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

// 颜色。lipgloss v2 把 Color 从类型改成函数（返回 image/color.Color），
// 不再是常量，因此这里只能用 var。
var (
	colPrimary = lipgloss.Color("75")
	colAccent  = lipgloss.Color("212")
	colOK      = lipgloss.Color("42")
	colWarn    = lipgloss.Color("214")
	colErr     = lipgloss.Color("203")
	colDim     = lipgloss.Color("245")
	colSelText = lipgloss.Color("232")
)

// style 组装一个基础样式；NoColor 时只保留字重，不发颜色。
func (t Theme) style(fg color.Color, bold bool) lipgloss.Style {
	s := lipgloss.NewStyle()
	if !t.NoColor && fg != nil {
		s = s.Foreground(fg)
	}
	if bold {
		s = s.Bold(true)
	}
	return s
}

// Title 板块标题。
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

// Header 表头：比正文重，但不抢眼。
func (t Theme) Header() lipgloss.Style { return t.style(colDim, true) }

// SelectedRow 选中行。
//
// 有颜色时铺浅蓝底，让高亮成为完整色块；无颜色时退化为加粗 —— 选中位置由光标符
// （Cursor）标记，不依赖背景色。此处不用反色：反色会被当成颜色输出。
func (t Theme) SelectedRow() lipgloss.Style {
	if t.NoColor {
		return lipgloss.NewStyle().Bold(true)
	}
	return lipgloss.NewStyle().Background(colPrimary).Foreground(colSelText)
}

// Cursor 返回当前行前缀。选中与否靠它体现，因此在两个主题下都保留。
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

// Frame 渲染一个带标题的边框面板。
//
// lipgloss 没有「标题嵌在上边框里」这一项，但它提供了图层合成：先用官方边框渲出
// 盒子，再把标题作为更高 z 序的图层压到上边框上。边框仍由 lipgloss 生成，这里只
// 决定标题落在第几列。
//
// 标题层内容是 " 标题 "，落在第 0 行第 2 列：第 1 列的横线得以保留，于是得到通行
// 的 ╭─ 标题 ─────╮。
//
// 注意 lipgloss v2 的 Width/Height **包含边框**（源码注释 "Include borders in
// block size"），与 v1 相反。这里直接传目标尺寸，内容区自然是 width-4 × height-2。
func (t Theme) Frame(title, body string, width, height int, active bool) string {
	if width < 4 || height < 3 {
		return body
	}
	style := lipgloss.NewStyle().
		Border(t.Border()).
		Padding(0, 1).
		Width(width).
		Height(height)
	if !t.NoColor && active {
		style = style.BorderForeground(colPrimary)
	}
	box := style.Render(body)
	if title == "" {
		return box
	}
	maxTitle := width - 6
	if maxTitle < 1 {
		return box
	}
	label := lipgloss.NewLayer(" " + t.Title().Render(Truncate(title, maxTitle)) + " ").
		X(2).Y(0).Z(1)
	return lipgloss.NewCompositor(lipgloss.NewLayer(box).Z(0), label).Render()
}

// Overlay 把前景居中压在背景之上。
//
// 用于弹窗：不再整屏替换，底下的界面留在原处，弹窗浮在其上。
func (t Theme) Overlay(background, foreground string, width, height int) string {
	fgW, fgH := lipgloss.Width(foreground), lipgloss.Height(foreground)
	if width <= fgW || height <= fgH {
		return foreground
	}
	// 背景先按整屏尺寸规整，再由 Compositor 按 z 序合成。
	bg := lipgloss.NewStyle().Width(width).Height(height).Render(background)
	layer := lipgloss.NewLayer(foreground).
		X((width - fgW) / 2).
		Y((height - fgH) / 2).
		Z(1)
	return lipgloss.NewCompositor(lipgloss.NewLayer(bg).Z(0), layer).Render()
}

// Dimmed 把背景整体调暗，用于弹窗之下的内容。
//
// 只做「变淡」一种效果：加背景色会在浅色终端上糊成一片。
func (t Theme) Dimmed(s string) string {
	if t.NoColor {
		return s
	}
	return lipgloss.NewStyle().Faint(true).Render(s)
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
		return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
	}
	on := lipgloss.NewStyle().Foreground(colOK).Render(strings.Repeat("█", filled))
	off := lipgloss.NewStyle().Foreground(colDim).Render(strings.Repeat("░", width-filled))
	return on + off
}

// ── 显示宽度 ──────────────────────────────────────────────────
//
// 对齐一律走下面几个函数，它们基于 ansi.StringWidth（中文算 2 列）。
// 此前用 fmt.Sprintf("%-20s", …) 对齐，而 fmt 的宽度按 rune 计："状态" 是
// 2 rune / 4 列，"Git for Windows" 是 15 rune / 15 列，同一列填出的实际宽度不同，
// 误差逐列累加，表头与数据最多错开 9 列。

// Width 返回文本占用的显示列数（忽略 ANSI 序列）。
func Width(s string) int { return lipgloss.Width(s) }

// Truncate 把文本截到最多 n 个显示列，超出部分以省略号结尾。
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

// Cell 把文本规整为恰好 n 个显示列的单元格：超长则截断，不足则补空格。
func Cell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Width(n).Render(Truncate(s, n))
}
