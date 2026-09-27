package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// colOf 返回 sub 在 line 中起始处的**显示列号**（ANSI 序列不计入）。
func colOf(t *testing.T, line, sub string) int {
	t.Helper()
	i := strings.Index(line, sub)
	if i < 0 {
		t.Fatalf("渲染结果里找不到 %q：\n%s", sub, line)
	}
	return Width(line[:i])
}

// 概览表的列必须按显示宽度对齐：中文表头与中英数据都落在同一列。
//
// 这是此前的真实缺陷：对齐用的是 fmt.Sprintf("%-20s", …)，而 fmt 的宽度按 rune
// 计数。"状态" 是 2 rune / 4 列，"可更新" 是 3 rune / 6 列，"最新" 是 2 rune / 4 列，
// 同一列填出的实际宽度各不相同，误差逐列累加，实测表头与数据最多错开 9 列。
func TestOverviewColumnsAligned(t *testing.T) {
	m := newTestModel(t)
	m.apps = []*engine.App{
		{
			Ref:     core.AppRef{ID: "git", Name: "Git for Windows"},
			Status:  core.Status{Installed: true, Version: "2.45.0"},
			Release: core.Release{Version: "2.46.1"},
			Action:  core.ActionUpdate,
		},
		{
			Ref:     core.AppRef{ID: "demo", Name: "中文软件名称"},
			Status:  core.Status{Installed: true, Version: "1.0.0"},
			Release: core.Release{Version: "1.0.0"},
			Action:  core.ActionNoOp,
		},
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	lines := strings.Split(content(m), "\n")
	var head, rowA, rowB string
	for _, l := range lines {
		switch {
		case head == "" && strings.Contains(l, "状态") && strings.Contains(l, "本地版本"):
			head = l
		case rowA == "" && strings.Contains(l, "Git for Windows"):
			rowA = l
		case rowB == "" && strings.Contains(l, "中文软件名称"):
			rowB = l
		}
	}
	if head == "" || rowA == "" || rowB == "" {
		t.Fatalf("未取到表头或数据行:\n%s", strings.Join(lines, "\n"))
	}

	// 状态列、本地版本列、上游版本列，三列都要求表头与两行数据起点一致。
	want := map[string]int{}
	for _, label := range []string{"状态", "本地版本", "上游版本"} {
		want[label] = colOf(t, head, label)
	}
	pairs := []struct{ row, header, value string }{
		{rowA, "状态", "可更新"},
		{rowB, "状态", "最新"},
		{rowA, "本地版本", "2.45.0"},
		{rowB, "本地版本", "1.0.0"},
		{rowA, "上游版本", "2.46.1"},
	}
	for _, p := range pairs {
		if got := colOf(t, p.row, p.value); got != want[p.header] {
			t.Fatalf("%s 列错位：表头在第 %d 列，数据 %q 在第 %d 列\n%s",
				p.header, want[p.header], p.value, got, p.row)
		}
	}
}

// 超长内容必须被截断，而不是折行；也不得把其他列挤到退化。
//
// 折行会把一行的视觉高度从 1 变成 2，其后的所有行随之错位 —— 此前「上次安装不完整」
// 这样的说明文字就会把整张表撑坏。
//
// 退化是另一半风险：表格压缩列时优先压最宽的列，一条长说明能把「本地版本」压成
// 「本地…」。这里同时覆盖多种终端宽度，因为预算按面板宽度计算。
func TestOverviewLongTextDoesNotBreakLayout(t *testing.T) {
	// 名称 64 列、说明 240 列，都远超面板宽度。
	longName := strings.Repeat("很长的软件名称", 8)
	longNote := strings.Repeat("很长的说明文字", 20)

	for _, w := range []int{80, 96, 110, 140, 200} {
		m := newTestModel(t)
		m.apps = []*engine.App{{
			Ref:     core.AppRef{ID: "git", Name: longName},
			Status:  core.Status{Installed: true, Version: "2.45.0"},
			Release: core.Release{Version: "2.46.1"},
			Action:  core.ActionUpdate,
			Note:    longNote,
		}}
		m = update(t, m, tea.WindowSizeMsg{Width: w, Height: 24})

		out := content(m)
		lines := strings.Split(out, "\n")
		if len(lines) != 24 {
			t.Fatalf("宽度 %d：应为 24 行，实际 %d 行:\n%s", w, len(lines), out)
		}
		for i, l := range lines {
			if got := Width(l); got > w {
				t.Fatalf("宽度 %d：第 %d 行宽 %d 列，已溢出（折行或未截断）:\n%s", w, i, got, out)
			}
		}
		if strings.Contains(out, longNote) {
			t.Fatalf("宽度 %d：超长说明未被截断:\n%s", w, out)
		}
		// 其余列的表头必须完整保留，不能被长文本挤掉。
		for _, header := range []string{"软件", "状态", "本地版本", "上游版本", "说明"} {
			if !strings.Contains(out, header) {
				t.Fatalf("宽度 %d：列名 %q 被长文本挤没了:\n%s", w, header, out)
			}
		}
	}
}

// 面板标题必须嵌在上边框里，不另占一行。
func TestPanelTitleInTopBorder(t *testing.T) {
	m := newTestModel(t)
	m.apps = []*engine.App{{Ref: core.AppRef{ID: "git", Name: "Git"}, Action: core.ActionNoOp}}
	m = update(t, m, tea.WindowSizeMsg{Width: 90, Height: 20})

	for i, title := range tabTitles {
		m.tab = tabID(i)
		lines := strings.Split(content(m), "\n")
		// 最上一行是界面与终端之间的间隔，接着才是标签行；再下一行是板块上边框。
		if !strings.Contains(lines[windowPadY], title) {
			t.Fatalf("面板 %s 的标签未出现在标签行:\n%s", title, lines[windowPadY])
		}
		if !strings.Contains(lines[windowPadY+1], title) {
			t.Fatalf("面板 %s 的标题未出现在上边框:\n%s", title, lines[windowPadY+1])
		}
	}
}

// 工具名与版本号不再占用界面行，改由窗口标题承载。
func TestWindowTitleCarriesVersion(t *testing.T) {
	m := newTestModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 90, Height: 20})
	if got := m.View().WindowTitle; got != "upkit "+m.Version() {
		t.Fatalf("窗口标题为 %q，期望 %q", got, "upkit "+m.Version())
	}
	if strings.Contains(content(m), "upkit") {
		t.Fatalf("界面内不应再重复工具名与版本号:\n%s", content(m))
	}
}

// 头部必须只占一行，且统计信息仍然可见。
func TestHeaderIsSingleLineWithCounts(t *testing.T) {
	m := newTestModel(t)
	m.apps = []*engine.App{
		{Ref: core.AppRef{ID: "git", Name: "Git"}, Action: core.ActionUpdate, Status: core.Status{Installed: true}},
		{Ref: core.AppRef{ID: "demo", Name: "Demo"}, Action: core.ActionInstall},
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 90, Height: 20})

	head := m.viewHeader(90)
	if countLines(head) != 1 {
		t.Fatalf("头部应为 1 行，实际 %d 行:\n%s", countLines(head), head)
	}
	if !strings.Contains(head, "1 概览") {
		t.Fatalf("头部缺少标签行:\n%s", head)
	}
	if !strings.Contains(head, "已安装 1") || !strings.Contains(head, "可更新 1") {
		t.Fatalf("头部缺少统计信息:\n%s", head)
	}

	// 窄终端下先舍弃统计，标签行必须完整保留。
	narrow := m.viewHeader(40)
	if !strings.Contains(narrow, "1 概览") {
		t.Fatalf("窄终端下标签行被舍弃:\n%s", narrow)
	}
	if Width(narrow) > 40 {
		t.Fatalf("窄终端下头部宽度为 %d，已溢出", Width(narrow))
	}
}

// 弹窗必须叠在正文之上，底下的内容仍然可见。
func TestModalOverlaysBody(t *testing.T) {
	m := newTestModel(t)
	m.apps = []*engine.App{{
		Ref:     core.AppRef{ID: "git", Name: "Git for Windows"},
		Action:  core.ActionUpdate,
		Status:  core.Status{Installed: true, Version: "2.45.0"},
		Release: core.Release{Version: "2.46.1"},
	}}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})

	m.confirm = &confirmBox{Title: "确认更新", Message: "继续吗？", OnYes: func(*Model) tea.Cmd { return nil }}
	out := content(m)
	if !strings.Contains(out, "继续吗？") {
		t.Fatalf("弹窗未渲染:\n%s", out)
	}
	if !strings.Contains(out, "Git for Windows") {
		t.Fatalf("弹窗把正文整屏替换掉了，叠层失效:\n%s", out)
	}
	// 标签行应仍然存在，说明是叠加而不是替换。
	if !strings.Contains(out, "1 概览") {
		t.Fatalf("叠层后头部丢失:\n%s", out)
	}
}
