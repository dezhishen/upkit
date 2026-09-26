package tui

import keybind "charm.land/bubbles/v2/key"

// keyMap 集中声明键位，由 bubbles/help 生成 ? 面板与底栏提示。
//
// 之前这些说明散在 viewHelp 的手写表格与 viewFooter 的字符串字面量里，
// 加一个键要改两处、还容易漏；现在只有一份定义。
type keyMap struct {
	SwitchPanel keybind.Binding
	Check       keybind.Binding
	Update      keybind.Binding
	Preview     keybind.Binding
	Toggle      keybind.Binding
	Remove      keybind.Binding
	Rollback    keybind.Binding
	Export      keybind.Binding
	Save        keybind.Binding
	Quit        keybind.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		SwitchPanel: keybind.NewBinding(
			keybind.WithKeys("1", "2", "3", "4", "5", "6", "tab", "shift+tab"),
			keybind.WithHelp("1-6/tab", "切换面板")),
		Check: keybind.NewBinding(
			keybind.WithKeys("c", "C"),
			keybind.WithHelp("c/C", "检查选中/全部")),
		Update: keybind.NewBinding(
			keybind.WithKeys("u", "U"),
			keybind.WithHelp("u/U", "更新选中/全部")),
		Preview: keybind.NewBinding(
			keybind.WithKeys("p"),
			keybind.WithHelp("p", "预览执行计划")),
		Toggle: keybind.NewBinding(
			keybind.WithKeys(" "),
			keybind.WithHelp("空格", "启用/停用")),
		Remove: keybind.NewBinding(
			keybind.WithKeys("x"),
			keybind.WithHelp("x", "卸载（保留数据）")),
		Rollback: keybind.NewBinding(
			keybind.WithKeys("r"),
			keybind.WithHelp("r", "回滚到最近备份")),
		Export: keybind.NewBinding(
			keybind.WithKeys("E", "I"),
			keybind.WithHelp("E/I", "导出/导入")),
		Save: keybind.NewBinding(
			keybind.WithKeys("s"),
			keybind.WithHelp("s", "保存设置")),
		Quit: keybind.NewBinding(
			keybind.WithKeys("q"),
			keybind.WithHelp("q", "退出")),
	}
}

// ShortHelp 是底栏常驻的几条提示。
func (k keyMap) ShortHelp() []keybind.Binding {
	return []keybind.Binding{k.SwitchPanel, k.Check, k.Update, k.Preview, k.Rollback, k.Quit}
}

// FullHelp 是 ? 面板里的分组说明。
func (k keyMap) FullHelp() [][]keybind.Binding {
	return [][]keybind.Binding{
		{k.SwitchPanel, k.Check, k.Update, k.Preview},
		{k.Toggle, k.Remove, k.Rollback, k.Export},
		{k.Save, k.Quit},
	}
}
