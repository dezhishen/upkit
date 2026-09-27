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
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette 是一套配色。
//
// 为什么非得两套：同一个色号在深色与浅色终端上的可见度完全相反。给深色终端挑的
// 245 号灰，到了白底终端上几乎看不见；反之给白底挑的深蓝，放到黑底上又糊成一团。
// 所以「auto」不是装饰选项，而是默认就该有的行为。
type Palette struct {
	// Border / Title 是面板边框与嵌在上边框里的标题。
	Border color.Color
	Title  color.Color
	// Primary 是强调文字（表头主色、来源名、键名）。
	Primary color.Color
	// Accent 用于需要跳出正文的地方（设置分组标题、详情里的区块标题）。
	Accent color.Color
	// OK / Warn / Err 是语义色：成功、需要注意、失败。
	OK, Warn, Err color.Color
	// Dim 是次要文字（说明列、时长、灰色状态）。
	Dim color.Color
	// SelFg / SelBg 是选中行的前景与背景。
	SelFg, SelBg color.Color
	// Base 是界面自己铺的底色与默认前景，直接写成 256 色转义序列：
	// 它要插在每一处 SGR 复位（ESC[m）之后，而 lipgloss 的 Color 取不回色号字符串。
	// 只有在用户明确指定 dark/light 时才用（auto 交给终端，别跟终端配色打架）。
	Base string
	// BaseBg 是同一个底色，值类型，交给 bubbletea 当终端背景色（OSC 11）用。
	// 与 Base 里的 48;5;N 必须是同一个颜色（有测试盯着）。
	BaseBg color.Color
}

// 深色终端（黑底）用的配色。
//
// lipgloss v2 把 Color 从类型改成函数（返回 image/color.Color），不再是常量，
// 因此这里只能用 var。
var darkPalette = Palette{
	Border:  lipgloss.Color("60"),  // 偏灰的蓝：整圈亮蓝太吵，标题会失去重点
	Title:   lipgloss.Color("212"), // 洋红：与蓝底形成对比
	Primary: lipgloss.Color("75"),  // 亮蓝
	Accent:  lipgloss.Color("212"),
	OK:      lipgloss.Color("42"),  // 绿
	Warn:    lipgloss.Color("214"), // 橙
	Err:     lipgloss.Color("203"), // 红
	Dim:     lipgloss.Color("245"), // 灰
	SelFg:   lipgloss.Color("232"),
	SelBg:   lipgloss.Color("75"),
	// 深灰底 + 浅灰字：白底终端上选 dark、或黑底终端上选 dark 时用。
	Base:   "\x1b[38;5;252;48;5;234m",
	BaseBg: color.RGBA{R: 0x1c, G: 0x1c, B: 0x1c, A: 0xff}, // = 256 色 234
}

// 浅色终端（白底）用的配色：整体压暗、提高饱和度，保证在白底上读得清。
var lightPalette = Palette{
	Border:  lipgloss.Color("245"), // 浅灰边框：白底上「有框但不抢眼」
	Title:   lipgloss.Color("127"), // 紫红
	Primary: lipgloss.Color("25"),  // 深蓝
	Accent:  lipgloss.Color("127"),
	OK:      lipgloss.Color("28"),  // 深绿
	Warn:    lipgloss.Color("130"), // 棕橙
	Err:     lipgloss.Color("160"), // 深红
	Dim:     lipgloss.Color("240"), // 中灰
	SelFg:   lipgloss.Color("231"),
	SelBg:   lipgloss.Color("25"),
	// 浅灰底 + 近黑字：黑底终端上选 light 时，整块界面会变成浅色。
	Base:   "\x1b[38;5;235;48;5;255m",
	BaseBg: color.RGBA{R: 0xee, G: 0xee, B: 0xee, A: 0xff}, // = 256 色 255
}

// ThemeOptions 是主题的几个开关：命令行与设置都汇到这里。
type ThemeOptions struct {
	ASCII   bool
	NoColor bool
	Borders string // unicode（圆角）| square（直角）| ascii（纯 ASCII）
	// Variant 是配色方向：auto（默认，按终端背景猜）/ dark / light。
	Variant string
}

// Theme 是渲染用的一整套样式。
type Theme struct {
	NoColor bool
	ASCII   bool
	Borders string
	p       Palette
	// variant 记录最终选中的方向（auto 解析之后的结果），供测试与界面说明用。
	variant string
	// paint 为真时界面自己铺底色（用户明确选了 dark/light）。
	//
	// 为什么需要：终端底色是终端的事，我们改不了。用户在白底终端上选 light、
	// 或黑底终端上选 light，如果只是换一批前景色，黑底上看到的就是「一堆看不清的
	// 暗字」—— 看起来就像设置没生效。明确指定方向时把底色一并铺上，选项的效果
	// 才一眼可见，也才能摆脱「auto 在 Windows 上永远判成深色」这个限制。
	paint bool
}

// NewTheme 构造主题。
func NewTheme(opts ThemeOptions) Theme {
	variant := resolveVariant(opts.Variant)
	return Theme{
		ASCII:   opts.ASCII,
		NoColor: opts.NoColor,
		Borders: opts.Borders,
		p:       paletteFor(variant),
		variant: variant,
		paint:   !opts.NoColor && explicitVariant(opts.Variant),
	}
}

// Variant 返回最终生效的配色方向（auto 已解析）。
func (t Theme) Variant() string { return t.variant }

// Painted 报告界面是否自己铺底色（而不是交给终端）。
func (t Theme) Painted() bool { return t.paint && t.p.Base != "" }

// Background 返回界面自己的底色（不铺底时为 nil，即不动终端背景）。
func (t Theme) Background() color.Color {
	if !t.Painted() {
		return nil
	}
	return t.p.BaseBg
}

// Summary 用一句话说明当前配色，供状态栏显示。
//
// 「选了 light 但看不出变化」是这一轮要修的问题：只在文档里写「light 适用于白底
// 终端」不够，界面上得能直接看到现在用的是哪套、底色由谁定。
func (t Theme) Summary() string {
	name := "深色"
	if t.variant == "light" {
		name = "浅色"
	}
	if t.Painted() {
		return name + "（界面自己铺底色）"
	}
	return name + "（跟随终端底色）"
}

// Paint 把底色与默认前景铺满整块。
//
// 只能事后补：lipgloss 给每段文字单发一对「转义 + ESC[m 复位」，复位会把底色
// 一起清掉，外层再套一层 Background 也不会生效（ANSI 没有作用域概念）。
// 于是每行开头补一次、每次复位之后再补一次，保证没有一处文字落在终端自己的底色上；
// 行尾补齐到整屏宽度，右侧才不会露出一条终端底色。
func (t Theme) Paint(block string, width int) string {
	if !t.Painted() {
		return block
	}
	base := t.p.Base
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		if pad := width - Width(l); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		lines[i] = base + strings.ReplaceAll(l, "\x1b[m", "\x1b[m"+base)
	}
	return strings.Join(lines, "\n")
}

// explicitVariant 报告用户是不是明确指定了方向（auto / 空 都算没指定）。
func explicitVariant(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dark", "light":
		return true
	}
	return false
}

// paletteFor 按方向取配色。
func paletteFor(variant string) Palette {
	if variant == "light" {
		return lightPalette
	}
	return darkPalette
}

// resolveVariant 把 auto 解析成 dark 或 light。
//
// 终端不会告诉你它的背景色，但很多终端会设 COLORFGBG（形如 "15;0"，最后一段是
// 背景色号）。有它就用它，没有就按深色处理 —— 绝大多数终端是深色底，猜错的那一半
// 也能用 ui.theme 手动改。
func resolveVariant(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "light":
		return "light"
	case "dark":
		return "dark"
	}
	if bg, ok := backgroundColorIndex(); ok {
		// 0..6 与 8 是深色；7 与 15 是白/亮白。其余（含 256 色）按深色处理：
		// 没有更可靠的判据时，深色是更安全的默认。
		if bg == 7 || bg == 15 {
			return "light"
		}
	}
	return "dark"
}

// backgroundColorIndex 从 COLORFGBG 里取背景色号。
func backgroundColorIndex() (int, bool) {
	v := strings.TrimSpace(os.Getenv("COLORFGBG"))
	if v == "" {
		return 0, false
	}
	parts := strings.Split(v, ";")
	last := strings.TrimSpace(parts[len(parts)-1])
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0, false
	}
	return n, true
}

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
func (t Theme) Title() lipgloss.Style { return t.style(t.p.Title, true) }

// Primary 强调文字。
func (t Theme) Primary() lipgloss.Style { return t.style(t.p.Primary, true) }

// Dim 次要文字。
func (t Theme) Dim() lipgloss.Style { return t.style(t.p.Dim, false) }

// OK 成功。
func (t Theme) OK() lipgloss.Style { return t.style(t.p.OK, false) }

// Warn 警告。
func (t Theme) Warn() lipgloss.Style { return t.style(t.p.Warn, false) }

// Err 错误。
func (t Theme) Err() lipgloss.Style { return t.style(t.p.Err, false) }

// OKBold / WarnBold / ErrBold 是语义色的加重版：用在状态列这种「一眼扫过去」的地方。
func (t Theme) OKBold() lipgloss.Style   { return t.style(t.p.OK, true) }
func (t Theme) WarnBold() lipgloss.Style { return t.style(t.p.Warn, true) }
func (t Theme) ErrBold() lipgloss.Style  { return t.style(t.p.Err, true) }

// PrimaryPlain 是不加粗的强调色：用在键名、数字这类小块文字上。
func (t Theme) PrimaryPlain() lipgloss.Style { return t.style(t.p.Primary, false) }

// Accent 用于跳出来的小标题（设置分组、详情区块）。
func (t Theme) AccentStyle() lipgloss.Style { return t.style(t.p.Accent, true) }

// Header 表头：比正文重，但不抢眼。
func (t Theme) Header() lipgloss.Style { return t.style(t.p.Dim, true) }

// SelectedRow 选中行。
//
// 有颜色时铺浅蓝底，让高亮成为完整色块；无颜色时退化为加粗 —— 选中位置由光标符
// （Cursor）标记，不依赖背景色。此处不用反色：反色会被当成颜色输出。
func (t Theme) SelectedRow() lipgloss.Style {
	if t.NoColor {
		return lipgloss.NewStyle().Bold(true)
	}
	return lipgloss.NewStyle().Background(t.p.SelBg).Foreground(t.p.SelFg)
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
		style = style.BorderForeground(t.p.Border)
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
	on := lipgloss.NewStyle().Foreground(t.p.OK).Render(strings.Repeat("█", filled))
	off := lipgloss.NewStyle().Foreground(t.p.Dim).Render(strings.Repeat("░", width-filled))
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
