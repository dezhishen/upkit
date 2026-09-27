package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// 每个面板的右边框必须落在同一列上：终端里边框抖一列就是右侧边缘的毛刺，
// 而面板高度是算出来的，一旦某几行宽度算短了，肉眼看的是一条锯齿。
func TestPanelRightBorderAligns(t *testing.T) {
	for _, v := range []string{"dark", "light"} {
		for _, tab := range []tabID{tabOverview, tabJobs, tabLogs, tabSettings, tabSources, tabDetail} {
			m := borderFixture(t, v)
			m.tab = tab
			m = update(t, m, key(tea.KeyEnter)) // 详情页需要先选中一行
			view := m.render()

			want := -1
			for i, l := range strings.Split(view, "\n") {
				if col, ok := rightBorderCol(l); ok {
					if want < 0 {
						want = col
						continue
					}
					if col != want {
						t.Fatalf("%s/%s: 第 %d 行右边框在第 %d 列，其余在第 %d 列",
							v, tabName(tab), i, col, want)
					}
				}
			}
			if want < 0 {
				t.Fatalf("%s/%s: 没找到边框", v, tabName(tab))
			}
		}
	}
}

// rightBorderCol 返回该行最后一个竖边框所在的显示列（不含该字符本身）。
func rightBorderCol(line string) (int, bool) {
	plain := ansi.Strip(line)
	trimmed := strings.TrimRight(plain, " ")
	if trimmed == "" {
		return 0, false
	}
	last := []rune(trimmed)[len([]rune(trimmed))-1]
	switch last {
	case '│', '┃', '╮', '╯', '┐', '┘', '|', '+':
	default:
		return 0, false
	}
	return ansi.StringWidth(strings.TrimSuffix(trimmed, string(last))), true
}

// borderFixture 造一批会把列宽算法逼到边界的行：中文标签、宽字符值、超长说明。
func borderFixture(t *testing.T, variant string) Model {
	m := ready(t,
		&engine.App{
			Ref:     core.AppRef{ID: "git", Name: "Git for Windows", Source: "github-release", Method: "portable-inplace"},
			Status:  core.Status{Installed: true, Version: "2.47.1"},
			Action:  core.ActionUpdate,
			Release: core.Release{Version: "2.48.0"},
			Note:    "可更新到 2.48.0（含 32 位与 64 位两套安装包）",
		},
		&engine.App{
			Ref:      core.AppRef{ID: "vlc", Name: "甲软件（很长的中文名字用来挤列宽）", Disabled: true},
			Shadowed: true,
			Conflict: &engine.Conflict{Message: "与另一个软件的安装目录重叠"},
		},
	)
	m.theme = NewTheme(ThemeOptions{Variant: variant})
	m.status = strings.Repeat("很长的状态文字", 6)
	m.jobs = []*jobItem{{Name: "很长的任务名", State: "进行中", Phase: "下载并校验", Done: 1 << 20, Total: 8 << 20}}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}
