package tui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// key 构造一次「按下某个键」的消息。
//
// bubbletea v2 把 KeyMsg 从结构体改成了接口，v1 的 {Type, Runes} 对应 v2 的
// KeyPressMsg{Code, Text}。可打印字符要同时填 Code 与 Text（真实终端即如此，
// msg.String() 优先取 Text），功能键（Esc / 方向键 / 退格）只填 Code。
func key(r rune) tea.KeyPressMsg {
	k := tea.KeyPressMsg{Code: r}
	if unicode.IsPrint(r) {
		k.Text = string(r)
	}
	return k
}

// text 构造一次多字符输入。
//
// 粘贴或输入法上屏会一次带多个字符，v2 的 KeyPressMsg.Text 是 string（v1 是
// []rune），正好用于覆盖这类输入。
func text(s string) tea.KeyPressMsg {
	if r := []rune(s); len(r) == 1 {
		return key(r[0])
	}
	return tea.KeyPressMsg{Text: s}
}

// content 取出渲染后的正文。
//
// v2 的 View() 返回 tea.View，正文字符串在 Content 字段里。
func content(m Model) string { return m.View().Content }
