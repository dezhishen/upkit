package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
)

// 日志行现在带颜色，而视口是按「显示宽度」排版的可滚动区域。
//
// 这里要盯住的是：加了转义序列之后，长行仍然不能撑破面板 —— 一旦视口把转义字符
// 也算进可视宽度，窄终端里日志就会顶穿右边框。
func TestLogLinesWithColorStayInsideFrame(t *testing.T) {
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabLogs

	// 混合级别 + 一条远超面板宽度的长行（中文占 2 列，最容易算错）。
	for _, ev := range []core.Event{
		{AppID: "demo", Kind: core.EventLog, Level: core.LevelInfo, Msg: "开始检查上游版本"},
		{AppID: "demo", Kind: core.EventLog, Level: core.LevelWarn, Msg: "上游返回 403，稍后重试"},
		{AppID: "demo", Kind: core.EventLog, Level: core.LevelError, Msg: strings.Repeat("下载失败：连接重置。", 20)},
		{AppID: "demo", Kind: core.EventLog, Level: core.LevelDebug, Msg: "缓存命中"},
	} {
		m = update(t, m, eventMsg(ev))
	}

	out := m.render()
	lines := strings.Split(out, "\n")
	if len(lines) != 30 {
		t.Fatalf("渲染高度应为 30 行，实际 %d 行", len(lines))
	}
	for i, l := range lines {
		if got := Width(l); got != 100 {
			t.Fatalf("第 %d 行显示宽度为 %d，应为 100:\n%s", i, got, out)
		}
	}
	// 级别与正文都要还在（别为了塞进去把内容丢了）。debug 被默认级别过滤掉，
	// 所以这里不列它 —— 那是过滤器的职责，见 TestLogsKeyFlow。
	for _, want := range []string{"INFO", "WARN", "ERROR", "开始检查上游版本"} {
		if !strings.Contains(plain(out), want) {
			t.Errorf("日志内容 %q 丢失", want)
		}
	}
	// 级别确实上了色：error 那一行的转义序列应当不止「重置」一类。
	if !strings.Contains(out, "38;5;") {
		t.Error("日志级别没有上色")
	}
}

// 概览头部的统计要跟着数据变，而且只在需要动手时才用醒目颜色。
func TestHeaderCountsColoredOnlyWhenActionable(t *testing.T) {
	m := ready(t, demoApp(core.ActionNoOp))
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})

	quiet := m.render()
	if !strings.Contains(plain(quiet), "可更新 0") {
		t.Fatalf("没有软件可更新时应显示「可更新 0」：\n%s", plain(quiet))
	}

	m = ready(t, demoApp(core.ActionUpdate))
	loud := m.render()
	if !strings.Contains(plain(loud), "可更新 1") {
		t.Fatalf("有更新时统计应变成 1：\n%s", plain(loud))
	}
	// 找出含「可更新」的那一行：它必须比安静时多出颜色（橙+粗体），
	// 否则「有更新」这件事仍然要靠读数字才发现。
	if lineColors(quiet, "可更新") == lineColors(loud, "可更新") {
		t.Error("可更新 > 0 时应换用醒目的颜色")
	}
}

// lineColors 取包含关键字的行里的颜色序列，用于比较「有没有上色/颜色是否变强」。
func lineColors(view, key string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(plain(l), key) {
			return strings.Join(ansiSeqRe.FindAllString(l, -1), "")
		}
	}
	return ""
}
