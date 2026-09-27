package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// 在设置面板里改「界面主题」必须当场换色：改完什么也没发生，
// 用户会以为这个选项是坏的（和「禁用后列表不刷新」是同一类问题）。
func TestThemeSettingAppliesImmediately(t *testing.T) {
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	idx := -1
	for i, f := range m.settingsRows() {
		if f.Key == "ui.theme" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("设置里没有 ui.theme 这一项")
	}
	m.setCursor = idx

	// 枚举项用 ←/→ 循环取值，转到 light 为止。
	for i := 0; i < 6; i++ {
		if themeSettingValue(t, m) == "light" {
			break
		}
		m = update(t, m, key('l'))
	}
	if v := themeSettingValue(t, m); v != "light" {
		t.Fatalf("枚举项没转到 light，实际 %q", v)
	}
	if got := m.theme.Variant(); got != "light" {
		t.Fatalf("改完配色方向应立即换色，实际仍为 %q", got)
	}

	// 保存不该把它改回去，其它显示选项也不该被重置。
	before := m.theme.Borders
	m = update(t, m, key('s'))
	if got := m.theme.Variant(); got != "light" {
		t.Fatalf("保存后配色方向变成了 %q", got)
	}
	if m.theme.Borders != before {
		t.Errorf("重算配色不该改动边框样式：%q → %q", before, m.theme.Borders)
	}
}

// 边框样式同样要当场生效（它和配色一样只是显示选项）。
func TestBorderSettingAppliesImmediately(t *testing.T) {
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	m.setCursor = -1
	for i, f := range m.settingsRows() {
		if f.Key == "ui.borders" {
			m.setCursor = i
		}
	}
	if m.setCursor < 0 {
		t.Fatal("设置里没有 ui.borders 这一项")
	}
	before := m.theme.Borders
	// 转到 ASCII 边框：枚举是 unicode → square → ascii，最多按两下。
	for i := 0; i < 4 && m.theme.Borders != "ascii"; i++ {
		m = update(t, m, key('l'))
	}
	if m.theme.Borders == before {
		t.Fatalf("改边框样式后应立即生效，实际仍是 %q", m.theme.Borders)
	}
	if m.theme.Borders != "ascii" {
		t.Fatalf("枚举没转到 ascii，实际 %q", m.theme.Borders)
	}
	// 画出来的框也得跟着变：ASCII 模式不该再出现制表符。
	out := m.render()
	for _, ch := range []string{"╭", "╮", "─", "│"} {
		if strings.Contains(out, ch) {
			t.Errorf("切到 ASCII 边框后仍出现制表符 %q", ch)
		}
	}
}

// 切回 auto 时要按终端背景重新判断，而不是停在上一套配色上。
func TestThemeSettingBackToAuto(t *testing.T) {
	t.Setenv("COLORFGBG", "0;15") // 白底终端
	m := ready(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.tab = tabSettings

	setThemeSetting(t, &m, "dark")
	if got := m.theme.Variant(); got != "dark" {
		t.Fatalf("写死 dark 后应为 dark，实际 %q", got)
	}

	setThemeSetting(t, &m, "auto")
	if got := m.theme.Variant(); got != "light" {
		t.Fatalf("auto 应按终端背景（白底）判为 light，实际 %q", got)
	}
}

// themeSettingValue 读设置表单里「界面主题」当前的值。
func themeSettingValue(t *testing.T, m Model) string {
	t.Helper()
	for _, f := range m.settingsRows() {
		if f.Key == "ui.theme" {
			return strings.TrimSpace(f.Text)
		}
	}
	t.Fatal("设置里没有 ui.theme 这一项")
	return ""
}

// setThemeSetting 直接用控制层的枚举调整接口把主题切到指定值，
// 再走一次「应用配色」——这也顺带验证了非界面路径（比如配置文件里写的值）。
func setThemeSetting(t *testing.T, m *Model, want string) {
	t.Helper()
	for i := 0; i < 8 && themeSettingValue(t, *m) != want; i++ {
		if err := m.ctrl.AdjustSetting("ui.theme", 1); err != nil {
			t.Fatalf("AdjustSetting: %v", err)
		}
	}
	if got := themeSettingValue(t, *m); got != want {
		t.Fatalf("枚举项没转到 %q，实际 %q", want, got)
	}
	m.applyThemeSetting()
}
