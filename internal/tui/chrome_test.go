package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
)

// ansiSeqRe 用来剥掉 SGR 序列：按「第几列」断言时要先去掉转义字符，
// 否则 strings.Index 算出来的是转义序列的长度。
var ansiSeqRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain 去掉 ANSI 序列。

// 详情是概览的下级页面，不是与它并列的页签。
//
// 之前「详情」占着标签行上的第二个位置，看着像和概览平级 —— 而它本来就只能在选中
// 某个软件之后才有意义。现在：标签行只有 5 个面板，概览里回车进详情，esc 回来，
// 头部把「概览 › 详情」的层级画出来。
func TestDetailIsSubPageOfOverview(t *testing.T) {
	m := ready(t, demoApp(core.ActionUpdate))
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	head := headerLine(m)
	if strings.Contains(head, "详情") {
		t.Fatalf("标签行上不该有「详情」：\n%s", head)
	}
	if !strings.Contains(head, "1 概览") || !strings.Contains(head, "5 来源") {
		t.Fatalf("标签行应是 1..5 五个面板：\n%s", head)
	}

	// 数字键只到 5：按 6 不再是任何面板。
	if got := update(t, m, key('6')); got.tab != m.tab {
		t.Fatalf("6 不该再切面板，实际 %s", tabName(got.tab))
	}

	// 回车进详情；头部把层级画出来（高亮仍是概览那一格 + 面包屑「详情」）。
	m = update(t, m, key(tea.KeyEnter))
	if m.tab != tabDetail {
		t.Fatalf("概览里回车应进详情，实际 %s", tabName(m.tab))
	}
	head = headerLine(m)
	if !strings.Contains(head, "概览") || !strings.Contains(head, "详情") {
		t.Fatalf("详情页头部应同时出现父级与自身：\n%s", head)
	}

	m = update(t, m, key(tea.KeyEscape))
	if m.tab != tabOverview {
		t.Fatalf("详情里 esc 应回概览，实际 %s", tabName(m.tab))
	}

	// Tab 循环只在标签行上的五个面板之间转，不该落到详情页。
	seen := map[tabID]int{}
	cur := m
	for i := 0; i < len(tabTitles); i++ {
		cur = update(t, cur, key(tea.KeyTab))
		seen[cur.tab]++
	}
	if seen[tabDetail] > 0 {
		t.Fatal("Tab 循环不该切到详情页")
	}
	if len(seen) != len(tabTitles) {
		t.Fatalf("Tab 应轮完五个面板：%v", seen)
	}

	// 在详情页按 Tab：从概览那格起算，落到任务面板。
	m.tab = tabDetail
	m = update(t, m, key(tea.KeyTab))
	if m.tab != tabJobs {
		t.Fatalf("从详情页按 Tab 应回到顶层页签的下一格，实际 %s", tabName(m.tab))
	}
}

// 鼠标点标签行也要按新的五个标签来（第 5 个是来源）。
func TestTabClickCoversFiveTabs(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	for i := range tabTitles {
		got := update(t, m, click(tabHitX(t, m, i), windowPadY))
		if int(got.tab) != i {
			t.Fatalf("点第 %d 个标签应切到 %s，实际 %s", i+1, tabTitles[i], tabName(got.tab))
		}
	}
}

// 界面与终端边缘之间留一圈间隔，且整块正好铺满终端。
//
// 贴着边上（尤其是左侧顶到窗口）看着很挤；而留白之后，每一行仍必须是整屏宽度、
// 行数正好等于终端高度 —— 否则末帧会残留上一帧的内容，弹窗的背景也会短一块。
func TestWindowPaddingLeavesMargin(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 20}} {
		w, h := size[0], size[1]
		m := ready(t, demoApp(core.ActionUpdate))
		m = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})

		lines := strings.Split(content(m), "\n")
		if len(lines) != h {
			t.Fatalf("%dx%d：应渲染 %d 行，实际 %d 行", w, h, h, len(lines))
		}
		for i, l := range lines {
			if got := Width(l); got != w {
				t.Fatalf("%dx%d：第 %d 行宽 %d 列，期望 %d", w, h, i, got, w)
			}
		}
		// 上边与下边留空。
		for _, idx := range []int{0, h - 1} {
			if strings.TrimSpace(lines[idx]) != "" {
				t.Fatalf("%dx%d：第 %d 行应是间隔行：%q", w, h, idx, lines[idx])
			}
		}
		// 左边留一列：界面本体从第 windowPadY 行、第 windowPadX 列开始。
		head := plain(lines[windowPadY])
		if !strings.HasPrefix(head, strings.Repeat(" ", windowPadX)) {
			t.Fatalf("%dx%d：标签行左侧应有间隔：%q", w, h, head)
		}
		// 面板上边框落在标签行的下一行，且不顶到左边界。
		border := plain(lines[windowPadY+1])
		if !strings.Contains(border, "╭") {
			t.Fatalf("%dx%d：第二行应是面板上边框：%q", w, h, border)
		}
		if idx := strings.Index(border, "╭"); idx != windowPadX {
			t.Fatalf("%dx%d：边框应从第 %d 列开始，实际 %d", w, h, windowPadX, idx)
		}
	}
}

// 弹窗仍然按整屏居中：它浮在最上层，不该被那圈边距约束。
func TestModalStillCenteredOnScreen(t *testing.T) {
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.help = true

	lines := strings.Split(content(m), "\n")
	if len(lines) != 30 {
		t.Fatalf("开了弹窗仍是 30 行，实际 %d", len(lines))
	}
	// 合成之后允许行尾被裁掉（Compositor 不补右侧空白），但绝不能超出屏幕。
	for i, l := range lines {
		if got := Width(l); got > 100 {
			t.Fatalf("开了弹窗后第 %d 行宽 %d，超出屏幕宽度", i, got)
		}
	}
	out := plain(content(m))
	if !strings.Contains(out, "全局") {
		t.Fatalf("弹窗内容应叠在底稿上：\n%s", out)
	}
	// 居中：弹窗的左边框不能落在界面本体那一列上（否则是左对齐而不是居中）。
	var modalCols []int
	for _, l := range lines {
		if idx := strings.Index(plain(l), "╭"); idx > windowPadX {
			modalCols = append(modalCols, idx)
		}
	}
	if len(modalCols) == 0 {
		t.Fatal("没找到弹窗的左边框")
	}
	if modalCols[0] <= windowPadX {
		t.Fatalf("弹窗应相对屏幕居中，实际从第 %d 列开始", modalCols[0])
	}
}

// headerLine 返回标签行（界面本体从第 windowPadY 行开始）。
func plain(s string) string { return ansiSeqRe.ReplaceAllString(s, "") }

func headerLine(m Model) string {
	lines := strings.Split(content(m), "\n")
	if len(lines) <= windowPadY {
		return ""
	}
	return lines[windowPadY]
}
