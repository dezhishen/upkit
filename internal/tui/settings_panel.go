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

func (m Model) updateSettings(key string) (tea.Model, tea.Cmd) {
	rows := m.settingsRows()
	switch key {
	case "j", "down":
		m.setCursor++
	case "k", "up":
		m.setCursor--
	case "g", "home":
		m.setCursor = 0
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
			case control.SettingText:
				m.prompt = m.settingTextPrompt(f)
			}
		}
	case "s":
		if !m.ctrl.SettingsDirty() {
			m.setStatus("没有需要保存的修改")
			return m, nil
		}
		if err := m.ctrl.SaveSettings(); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		m.setStatus("已保存到 " + m.ctrl.SettingsPath() + "（部分项重启后生效）")
		return m, nil
	case "R":
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
	if m.setCursor >= len(rows) {
		m.setCursor = len(rows) - 1
	}
	return m, nil
}

// adjustSetting 把增减意图交给控制层：范围与步长都在那边判，界面不重复一遍。
func (m *Model) adjustSetting(delta int) {
	rows := m.settingsRows()
	if m.setCursor < 0 || m.setCursor >= len(rows) {
		return
	}
	if err := m.ctrl.AdjustSetting(rows[m.setCursor].Key, delta); err != nil {
		m.setStatusErr(err)
	}
}

// settingTextPrompt 造文本项的编辑弹窗。
//
// 凭据类不预填原值（输入框按密码模式回显）：令牌铺在屏幕上会被录屏、肩窥和终端回滚
// 缓冲带走。
func (m Model) settingTextPrompt(f control.SettingItem) *promptBox {
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
