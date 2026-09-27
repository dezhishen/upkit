package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/util"
)

// settingsRows 返回设置表单的当前状态。
//
// 值、范围、步长、选项都由控制层给出 —— 设置是它的数据，界面只负责画出来、再把按键
// 转成意图。跟前后端分家一样：前端不猜字段，也不持有可写状态。
func (m Model) settingsRows() []control.SettingItem {
	if m.ctrl == nil {
		return nil
	}
	return m.ctrl.SettingsForm()
}

// settingsPaths 返回由启动方式决定、界面上改不了的路径（只读展示）。
func (m Model) settingsPaths() [][2]string {
	if m.ctrl == nil {
		return nil
	}
	return m.ctrl.SettingsPaths()
}

// settingsDirty 报告有没有还没落盘的修改（状态由控制层记）。
func (m Model) settingsDirty() bool {
	return m.ctrl != nil && m.ctrl.SettingsDirty()
}

// settingsViewport 返回设置面板一屏能放下几行设置项。
//
// 面板内高 = 正文高 - 上下边框，再留一行给底部的「… n/m」提示。宁少算一行：窗口比实际
// 小，光标只会更早开始滚动；算多了就会让选中行跑出屏幕。
func (m Model) settingsViewport() int {
	v := m.bodyHeight() - 2 - 1
	if v < 1 {
		v = 1
	}
	return v
}

// followSettings 把滚动窗口挪到能看见选中项的位置。
//
// 设置项一屏放不下（末尾还有只读的路径信息），而 j/k 只动光标、窗口不跟的话，光标
// 走出屏幕后既看不出自己选的是哪一项，也看不出下面还有东西。
func (m *Model) followSettings(rows []control.SettingItem) {
	win := m.settingsViewport()
	if len(rows) == 0 || m.setCursor < 0 {
		m.setOffset = 0
		return
	}
	// 按行号算：分组标题与组间空行都占行，拿项下标当行号会让窗口跟光标差几行。
	lines, itemLine := m.settingsLines(rows, m.width)
	line := 0
	if m.setCursor < len(itemLine) {
		line = itemLine[m.setCursor]
	}
	if m.setOffset > line {
		m.setOffset = line // 光标跑到窗口上方：窗口跟着上移
	}
	if line >= m.setOffset+win {
		m.setOffset = line - win + 1
	}
	if max := windowMaxStart(len(lines), m.bodyHeight()-2); m.setOffset > max {
		m.setOffset = max
	}
	if m.setOffset < 0 {
		m.setOffset = 0
	}
}

func (m Model) updateSettings(key string) (tea.Model, tea.Cmd) {
	rows := m.settingsRows()
	switch key {
	case "j", "down":
		m.setCursor++
	case "k", "up":
		m.setCursor--
	case "g", "home":
		m.setCursor = 0
		m.setOffset = 0 // 「跳到开头」就该看到最上面：第一个分组的标题也在那儿
	case "G", "end":
		m.setCursor = len(rows) - 1
	case "left", "h":
		m.adjustSetting(-1)
	case "right", "l":
		m.adjustSetting(1)
	case "space", "enter":
		if m.setCursor >= 0 && m.setCursor < len(rows) {
			switch f := rows[m.setCursor]; f.Kind {
			case control.SettingBool, control.SettingEnum:
				m.adjustSetting(1)
			case control.SettingInt, control.SettingText:
				// 数字与文本都整段编辑：数字用 ←/→ 也能调，但要改大数（60 → 480）
				// 靠按步长键得按几十次。
				m.prompt = m.settingValuePrompt(f)
			}
		}
	case "s":
		if !m.settingsDirty() {
			m.setStatus("没有需要保存的修改")
			return m, nil
		}
		if err := m.ctrl.SaveSettings(); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		m.applyThemeSetting()
		m.setStatus("已保存到 " + m.ctrl.SettingsPath() + "（部分项重启后生效）")
		return m, nil
	case "D":
		m.confirm = &confirmBox{
			Title:   "恢复默认设置",
			Message: "将把设置恢复为内置默认值（不会删除软件清单）。",
			OnYes: func(mm *Model) tea.Cmd {
				if err := mm.ctrl.ResetSettings(); err != nil {
					mm.setStatusErr(err)
					return nil
				}
				mm.setStatus("已恢复默认值，按 s 保存")
				return nil
			},
		}
		return m, nil
	}
	if m.setCursor < 0 {
		m.setCursor = 0
	}
	if n := len(rows); m.setCursor >= n {
		m.setCursor = n - 1
	}
	m.followSettings(rows)
	return m, nil
}

// adjustSetting 把增减意图交给控制层：范围与步长都在那边判，界面不重复一遍。
func (m *Model) adjustSetting(delta int) {
	rows := m.settingsRows()
	if m.setCursor < 0 || m.setCursor >= len(rows) {
		return
	}
	f := rows[m.setCursor]
	if f.Kind == control.SettingText {
		// 文本项没有可增减的值：提一句怎么改，而不是弹个「不能用增减调整」的错误框 ——
		// 在目录项上按左右键是很容易发生的事。
		m.setStatus(f.Label + "：按 space 整段输入")
		return
	}
	if err := m.ctrl.AdjustSetting(f.Key, delta); err != nil {
		m.setStatusErr(err)
		return
	}
	// 配色这类「改了就该看见」的设置要立刻生效：改完什么也没发生，用户会以为
	// 这个选项是坏的。
	if m.applyThemeSetting() && f.Key == "ui.theme" {
		// 只换前景色时，「生效了」这件事在黑底终端上几乎看不出来，
		// 所以再补一句：现在用哪套、底色由谁定。
		m.setStatus("配色已切到 " + m.theme.Summary())
	}
}

// applyThemeSetting 把设置里的显示选项（配色方向、边框样式）应用到当前主题，
// 返回是否真的换了配色。
//
// 这些项改了就该当场看见效果：否则用户改完什么也没发生，会以为选项是坏的。
// ASCII / 无颜色这两项来自命令行，不走设置，重建时按现值带上。
func (m *Model) applyThemeSetting() bool {
	if m.ctrl == nil {
		return false
	}
	// 从表单里取当前值：枚举项的「当前是什么」就写在表单上，
	// 控制层不额外提供按 key 读枚举的接口。
	variant, borders := "", ""
	for _, f := range m.settingsRows() {
		switch f.Key {
		case "ui.theme":
			variant = strings.TrimSpace(f.Text)
		case "ui.borders":
			borders = strings.TrimSpace(f.Text)
		}
	}
	if variant == "" {
		variant = m.theme.Variant()
	}
	if borders == "" {
		borders = m.theme.Borders
	}
	next := NewTheme(ThemeOptions{
		ASCII:   m.theme.ASCII,
		NoColor: m.theme.NoColor,
		Borders: borders,
		Variant: variant,
	})
	if next.Variant() == m.theme.Variant() && next.Borders == m.theme.Borders &&
		next.Painted() == m.theme.Painted() {
		return false
	}
	m.theme = next
	return true
}

// settingValuePrompt 造文本项/数字项的编辑弹窗。
//
// 凭据类不预填原值（输入框按密码模式回显）：令牌铺在屏幕上会被录屏、肩窥和终端回滚
// 缓冲带走。
func (m Model) settingValuePrompt(f control.SettingItem) *promptBox {
	key, label, hint, secret := f.Key, f.Label, f.Hint, f.Secret
	cur := ""
	if !secret {
		cur, _ = m.ctrl.SettingValue(key)
	}
	return newPromptBox(label, hint, cur, secret, func(mm *Model, v string) tea.Cmd {
		if err := mm.ctrl.SetSetting(key, strings.TrimSpace(v)); err != nil {
			mm.setStatusErr(err)
		}
		return nil
	})
}

// ── 小工具 ────────────────────────────────────────────────────

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func yesNo(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func humanSize(n int64) string {
	if n <= 0 {
		return "—"
	}
	return util.HumanBytes(n)
}
