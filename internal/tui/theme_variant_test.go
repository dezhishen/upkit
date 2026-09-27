package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
)

// 显式指定配色方向时不能再去看环境变量 —— 用户在设置文件里写了什么就是什么。
func TestThemeVariantExplicit(t *testing.T) {
	t.Setenv("COLORFGBG", "0;15") // 环境说这是白底，请求说深色，请求优先。
	for _, in := range []string{"light", "Light", " light "} {
		if got := NewTheme(ThemeOptions{Variant: in}).Variant(); got != "light" {
			t.Errorf("Variant(%q) = %q，应为 light", in, got)
		}
	}
	if got := NewTheme(ThemeOptions{Variant: " dark "}).Variant(); got != "dark" {
		t.Errorf("Variant(\" dark \") = %q，应为 dark", got)
	}
	// 认不出来的取值退回自动判定，而不是落进没有配色的分支。
	if got := NewTheme(ThemeOptions{Variant: "neon"}).Variant(); got != "light" {
		t.Errorf("未知取值 = %q，应退回自动判定（light）", got)
	}
}

// auto 依据 COLORFGBG 判断终端背景；判不出来时按深色处理（绝大多数终端是深色）。
func TestThemeVariantAutoFollowsColorFGBG(t *testing.T) {
	cases := []struct {
		fgbg string
		want string
	}{
		{"", "dark"},        // 未设置
		{"15;0", "dark"},    // 白字黑底
		{"0;15", "light"},   // 黑字白底
		{"0;7", "light"},    // 老式白底
		{"0;1", "dark"},     // 暗红底
		{"garbage", "dark"}, // 不是色号
		{"0;256", "dark"},   // 256 色判不出深浅，按深色
		{" 0;15 ", "light"}, // 两端空白不该影响判断
	}
	for _, c := range cases {
		t.Setenv("COLORFGBG", c.fgbg)
		if got := NewTheme(ThemeOptions{Variant: "auto"}).Variant(); got != c.want {
			t.Errorf("COLORFGBG=%q: 方向为 %q，应为 %q", c.fgbg, got, c.want)
		}
		// 空字符串与 "auto" 等价（设置文件里留空就是自动）。
		if got := NewTheme(ThemeOptions{}).Variant(); got != c.want {
			t.Errorf("COLORFGBG=%q: 默认方向为 %q，应为 %q", c.fgbg, got, c.want)
		}
	}
}

// 深色与浅色两套配色必须真的不一样：否则「支持 auto」只是句空话，
// 白底终端上该看不见的仍然看不见。
func TestThemeVariantsDifferAllStyles(t *testing.T) {
	dark := NewTheme(ThemeOptions{Variant: "dark"})
	light := NewTheme(ThemeOptions{Variant: "light"})

	cases := []struct {
		name string
		d, l string
	}{
		{"Dim", dark.Dim().Render("文字"), light.Dim().Render("文字")},
		{"Title", dark.Title().Render("文字"), light.Title().Render("文字")},
		{"Primary", dark.PrimaryPlain().Render("文字"), light.PrimaryPlain().Render("文字")},
		{"OK", dark.OK().Render("文字"), light.OK().Render("文字")},
		{"Warn", dark.WarnBold().Render("文字"), light.WarnBold().Render("文字")},
		{"Err", dark.Err().Render("文字"), light.Err().Render("文字")},
		{"面板", dark.Frame("标题", "正文", 20, 4, true), light.Frame("标题", "正文", 20, 4, true)},
		{"选中行", dark.SelectedRow().Render("文字"), light.SelectedRow().Render("文字")},
	}
	for _, c := range cases {
		if c.d == c.l {
			t.Errorf("%s: 深浅两套配色渲染结果相同（%q）", c.name, c.d)
		}
		if !hasColorSeq(c.d) || !hasColorSeq(c.l) {
			t.Errorf("%s: 有配色但没发颜色序列（深 %q / 浅 %q）", c.name, c.d, c.l)
		}
	}
}

// --no-color 必须把两套配色的颜色都关掉，只留字重 —— 否则重定向到文件时全是乱码。
func TestNoColorSuppressesBothVariants(t *testing.T) {
	for _, v := range []string{"dark", "light"} {
		th := NewTheme(ThemeOptions{Variant: v, NoColor: true})
		out := th.Title().Render("标题") +
			th.ErrBold().Render("错误") +
			th.WarnBold().Render("警告") +
			th.SelectedRow().Render("选中") +
			th.Dim().Render("次要")
		if strings.Contains(out, "38;5;") || strings.Contains(out, "48;5;") {
			t.Errorf("%s: --no-color 下仍发了颜色：%q", v, out)
		}
	}
}

// 概览「状态」列按语义上色：结论不同 → 颜色不同。
//
// 这一列是整个界面里唯一必须靠颜色扫的地方，落错地方（比如给正文上色）就等于没
// 美化，所以这里逐个断言分支。
func TestStateStyleDistinguishesActions(t *testing.T) {
	m := Model{theme: NewTheme(ThemeOptions{Variant: "dark"})}
	style := func(a *engine.App) string { return m.stateStyle(a).Render("状态") }

	// 零值 Action 就是「最新」；可更新与可重装属同一档（都是「有事要做」）。
	latest := style(&engine.App{Action: core.ActionNoOp})
	if latest != style(&engine.App{}) {
		t.Error("零值 Action 应与「最新」同色")
	}
	upd := style(&engine.App{Action: core.ActionUpdate})
	if upd != style(&engine.App{Action: core.ActionReinstall}) {
		t.Error("重装与可更新应同色")
	}
	if upd == latest {
		t.Error("可更新不能看起来像「最新」")
	}
	want := map[string]string{
		"可更新":  upd,
		"待安装":  style(&engine.App{Action: core.ActionInstall}),
		"冲突":   style(&engine.App{Action: core.ActionUpdate, Shadowed: true}),
		"检查失败": style(&engine.App{CheckErr: errors.New("上游不通")}),
	}
	seen := map[string]string{"最新": latest}
	for name, got := range want {
		if !hasColorSeq(got) {
			t.Errorf("%s: 没有上色（%q）", name, got)
		}
		for other, otherOut := range seen {
			if got == otherOut {
				t.Errorf("%s 与 %s 撞色（%q）", name, other, got)
			}
		}
		seen[name] = got
	}
	// 冲突要比普通「可更新」更重，检查失败要算错误 —— 这两条是排查时的抓手。
	if want["冲突"] == want["可更新"] {
		t.Error("冲突必须比可更新更醒目")
	}
	if want["检查失败"] == latest {
		t.Error("检查失败不能看起来像「最新」")
	}
}

// 停用的软件整行压暗：它不该在列表里抢眼。
func TestStateStyleDisabledFadesOut(t *testing.T) {
	m := Model{theme: NewTheme(ThemeOptions{Variant: "dark"})}
	upd := m.stateStyle(&engine.App{Action: core.ActionUpdate}).Render("状态")
	off := m.theme.Dim().Render("状态")
	if upd == off {
		t.Fatal("可更新与停用不应同色")
	}
}

// 检查中的行用「正在处理」的颜色，而不是它上次检查剩下的结论色。
func TestStateStyleChecking(t *testing.T) {
	m := Model{theme: NewTheme(ThemeOptions{Variant: "dark"}), checkLeft: map[string]bool{"demo": true}}
	checking := m.stateStyle(&engine.App{Ref: core.AppRef{ID: "demo"}, Action: core.ActionUpdate}).Render("状态")
	if checking != m.theme.PrimaryPlain().Render("状态") {
		t.Error("检查中的行应使用进行中的颜色")
	}
	if checking == m.stateStyle(&engine.App{Ref: core.AppRef{ID: "other"}}).Render("状态") {
		t.Error("检查中的颜色不能与「最新」撞色")
	}
}

// 日志级别上色：error/warn 要跳出来，debug 要退回去，info 中性。
func TestLogLevelStyles(t *testing.T) {
	m := Model{theme: NewTheme(ThemeOptions{Variant: "dark"})}
	out := map[string]string{}
	for _, lv := range []string{"error", "ERROR", "warn", "warning", "info", "debug", "trace", "unknown"} {
		out[lv] = m.logLevelStyle(lv).Render("级别")
	}
	if out["error"] != out["ERROR"] {
		t.Error("级别大小写应同样处理")
	}
	if out["warn"] != out["warning"] {
		t.Error("warn 与 warning 应同色")
	}
	if out["debug"] != out["trace"] {
		t.Error("debug 与 trace 应同色")
	}
	if out["error"] == out["info"] || out["warn"] == out["info"] {
		t.Error("error/warn 必须与 info 区分开")
	}
	if out["debug"] == out["info"] {
		t.Error("debug 应比 info 更弱")
	}
	// 错误正文上色，其余级别正文保持阅读色（不然警告内容会被红字吃掉）。
	if !hasColorSeq(m.logMessageStyle("error").Render("失败")) {
		t.Error("错误正文应该上色")
	}
	for _, lv := range []string{"warn", "info", "debug"} {
		if hasColorSeq(m.logMessageStyle(lv).Render("正文")) {
			t.Errorf("%s 正文不该上色", lv)
		}
	}
}

// hasColorSeq 判断渲染结果里是否含 256 色前景/背景序列。
func hasColorSeq(s string) bool {
	return strings.Contains(s, "38;5;") || strings.Contains(s, "48;5;")
}
