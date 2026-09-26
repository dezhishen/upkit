package tui

import (
	"strings"
	"testing"
)

// 面板标题必须嵌在上边框里，且整块宽度恰好等于给定宽度。
func TestFrameTitleInsideTopBorder(t *testing.T) {
	th := NewTheme(false, false, "unicode")
	out := th.Frame("概览", "内容", 40, 6, true)

	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("高度应为 6 行，实际 %d 行:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if got := Width(l); got != 40 {
			t.Fatalf("第 %d 行宽度为 %d，应为 40:\n%s", i, got, out)
		}
	}
	if !strings.Contains(lines[0], "概览") {
		t.Fatalf("标题未出现在上边框:\n%s", out)
	}
	if strings.Contains(lines[1], "概览") {
		t.Fatalf("标题不应另占一行:\n%s", out)
	}
}

// ASCII 与直角边框下同样要把标题嵌进上边框。
func TestFrameTitleAllBorderStyles(t *testing.T) {
	for _, name := range []string{"unicode", "square", "ascii"} {
		th := NewTheme(false, false, name)
		out := th.Frame("任务", "x", 30, 4, false)
		if !strings.Contains(strings.Split(out, "\n")[0], "任务") {
			t.Fatalf("边框样式 %s 下标题未嵌入上边框:\n%s", name, out)
		}
	}
}

// 长标题不得撑破边框。
func TestFrameLongTitleTruncated(t *testing.T) {
	th := NewTheme(false, false, "unicode")
	out := th.Frame(strings.Repeat("很长的标题", 10), "x", 30, 4, true)
	for i, l := range strings.Split(out, "\n") {
		if got := Width(l); got != 30 {
			t.Fatalf("第 %d 行宽度为 %d，应为 30:\n%s", i, got, out)
		}
	}
}

// Cell 必须按显示宽度规整：中文占 2 列。
func TestCellUsesDisplayWidth(t *testing.T) {
	cases := []struct {
		in    string
		width int
	}{
		{"状态", 8},
		{"Git for Windows", 20},
		{"可更新", 8},
		{"很长的中文内容需要被截断", 10},
		{"", 6},
	}
	for _, c := range cases {
		if got := Width(Cell(c.in, c.width)); got != c.width {
			t.Fatalf("Cell(%q, %d) 宽度为 %d", c.in, c.width, got)
		}
	}
}

// 弹窗必须叠在背景之上，且底下的内容仍然存在。
func TestOverlayKeepsBackground(t *testing.T) {
	th := NewTheme(false, false, "unicode")
	base := strings.Join([]string{
		"背景第一行",
		"背景第二行",
		"背景第三行",
		"背景第四行",
		"背景第五行",
	}, "\n")
	modal := th.Frame("确认", "继续吗？", 20, 4, true)

	out := th.Overlay(base, modal, 40, 10)
	if !strings.Contains(out, "背景第一行") {
		t.Fatalf("叠层后背景内容丢失:\n%s", out)
	}
	if !strings.Contains(out, "继续吗？") {
		t.Fatalf("叠层后弹窗内容丢失:\n%s", out)
	}
	body := out
	if !strings.Contains(body, "背景第五行") {
		t.Fatalf("叠层后背景尾部丢失:\n%s", out)
	}
}

// 无颜色模式下 Dimmed 不应引入颜色转义。
func TestDimmedNoColor(t *testing.T) {
	th := NewTheme(false, true, "unicode")
	if got := th.Dimmed("文本"); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("禁用颜色后 Dimmed 仍输出转义: %q", got)
	}
}
