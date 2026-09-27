package tui

import keybind "charm.land/bubbles/v2/key"

// keyMap 集中声明键位，由 bubbles/help 生成 ? 面板与底栏提示。
//
// 键位的规矩是「一个键在全局只有一种含义」。此前的几处冲突就是这条规矩没守住：
//
//	r 既是回滚又是刷新，G 既是跳到末尾又是恢复跟随，q 有时退出有时返回，
//	h/l 在设置里是加减、在子视图里是返回，c 在概览是检查、在来源是打开。
//
// 现在：r 只表示刷新，R 只表示回滚（危险动作，带确认），D 只表示恢复默认；
// G 一律是「跳到末尾」（日志里到末尾就等于恢复跟随）；返回只有 esc；q 只表示退出。
type keyMap struct {
	SwitchPanel keybind.Binding
	Move        keybind.Binding
	TopBottom   keybind.Binding
	Page        keybind.Binding

	Check      keybind.Binding
	CheckAll   keybind.Binding
	Update     keybind.Binding
	UpdateAll  keybind.Binding
	Plan       keybind.Binding
	Toggle     keybind.Binding
	Rollback   keybind.Binding
	Uninstall  keybind.Binding
	Export     keybind.Binding
	Import     keybind.Binding
	Open       keybind.Binding
	Refresh    keybind.Binding
	Trust      keybind.Binding
	AddSub     keybind.Binding
	AddBuiltin keybind.Binding
	RemoveSub  keybind.Binding
	Edit       keybind.Binding
	AdjustLR   keybind.Binding
	Save       keybind.Binding
	ResetDef   keybind.Binding
	Clear      keybind.Binding

	LogLevel  keybind.Binding
	LogFilter keybind.Binding
	LogFollow keybind.Binding

	Back  keybind.Binding
	Help  keybind.Binding
	Quit  keybind.Binding
	Mouse keybind.Binding
}

func newKeyMap() keyMap {
	// b 统一构造：说明与键名一一对应，避免手写两遍对不上。
	b := func(desc string, keys ...string) keybind.Binding {
		return keybind.NewBinding(keybind.WithKeys(keys...), keybind.WithHelp(joinKeys(keys), desc))
	}
	// bAs 用于键名列表太长、或本来就描述的是鼠标这类「不是键」的条目。
	bAs := func(display, desc string, keys ...string) keybind.Binding {
		return keybind.NewBinding(keybind.WithKeys(keys...), keybind.WithHelp(display, desc))
	}
	return keyMap{
		SwitchPanel: bAs("1-6/tab", "切换面板（也可点标签）", "1", "2", "3", "4", "5", "6", "tab", "shift+tab"),
		Move:        b("上下移动", "up", "down", "j", "k"),
		TopBottom:   b("跳到首/末", "g", "G", "home", "end"),
		Page:        b("翻页", "pgup", "pgdown"),

		Check:      b("检查选中", "c"),
		CheckAll:   b("检查全部", "C"),
		Update:     b("更新选中", "u"),
		UpdateAll:  b("更新全部", "U"),
		Plan:       b("预览执行计划", "p"),
		Toggle:     b("启用/停用", " ", "space"),
		Rollback:   b("回滚到最近备份", "R"),
		Uninstall:  b("卸载（保留用户数据）", "x"),
		Export:     b("导出清单", "E"),
		Import:     b("导入清单", "I"),
		Open:       b("打开（配置/订阅详情）", "enter"),
		Refresh:    b("刷新", "r"),
		Trust:      b("信任插件", "t"),
		AddSub:     b("加订阅", "a"),
		AddBuiltin: b("加官方源", "o"),
		RemoveSub:  b("删除订阅", "d"),
		Edit:       b("编辑/切换该项", "enter"),
		AdjustLR:   b("调整数值", "left", "right", "h", "l"),
		Save:       b("保存设置", "s"),
		ResetDef:   b("恢复默认", "D"),
		Clear:      b("清除已完成任务", "d"),

		LogLevel:  b("切换日志级别", "f"),
		LogFilter: b("过滤关键字", "F"),
		LogFollow: b("跟进最新", "G"),

		Back:  b("返回上一层", "esc"),
		Help:  b("查看更多快捷键", "?"),
		Quit:  b("退出", "q", "ctrl+c"),
		Mouse: bAs("鼠标", "点标签、点行、双击打开、滚轮", "click"),
	}
}

// joinKeys 把键名拼成给人看的形式（空格键写成「空格」）。
func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += "/"
		}
		if k == " " {
			out += "空格"
			continue
		}
		out += k
	}
	return out
}

// ShortHelp 是底栏常驻的几条提示。
//
// 只留全局性的几条：面板自己的操作已经画在操作栏上了，底栏再重复一遍只会把宽度
// 占满，而窄终端下提示会被截断。
func (k keyMap) ShortHelp() []keybind.Binding {
	return []keybind.Binding{k.SwitchPanel, k.Help, k.Back, k.Quit}
}

// helpGroup 是一组键位说明。
type helpGroup struct {
	title string
	items []keybind.Binding
}

// groups 把键位按「在哪个层面用」分组。
//
// ? 面板自己按组排版，不用 help 组件的 FullHelp 渲染：那个渲染按固定列数分栏，
// 键位一多（这里 30 多条、分五组）就会把不同组的键混排进同一行，读起来全是噪声。
func (k keyMap) groups() []helpGroup {
	return []helpGroup{
		{"全局", []keybind.Binding{k.SwitchPanel, k.Move, k.TopBottom, k.Page, k.Back, k.Help, k.Quit, k.Mouse}},
		{"软件（概览 / 详情）", []keybind.Binding{
			k.Check, k.CheckAll, k.Update, k.UpdateAll, k.Plan,
			k.Toggle, k.Rollback, k.Uninstall, k.Export, k.Import,
		}},
		{"来源与插件", []keybind.Binding{
			k.Open, k.Refresh, k.Trust, k.AddSub, k.AddBuiltin, k.RemoveSub, k.Edit, k.ResetDef,
		}},
		{"日志", []keybind.Binding{k.LogLevel, k.LogFilter, k.LogFollow, k.Clear}},
		{"设置", []keybind.Binding{k.AdjustLR, k.Edit, k.Save, k.ResetDef}},
	}
}

// FullHelp 满足 help.KeyMap 接口（底栏只用 ShortHelp，? 面板走 groups）。
func (k keyMap) FullHelp() [][]keybind.Binding {
	out := make([][]keybind.Binding, 0, len(k.groups()))
	for _, g := range k.groups() {
		out = append(out, g.items)
	}
	return out
}
