package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
)

// 明确指定 dark/light 时界面自己铺底色；auto（含空值）交给终端。
//
// auto 不铺底是有意的：用户可能给终端配了 solarized、gruvbox 之类的底色，
// 我们铺一层自认为的「深色」反而会跟终端打架。
func TestPaintedOnlyWhenPinned(t *testing.T) {
	cases := []struct {
		variant string
		painted bool
	}{
		{"", false},
		{"auto", false},
		{"AUTO", false},
		{"dark", true},
		{" light ", true},
	}
	for _, c := range cases {
		if got := NewTheme(ThemeOptions{Variant: c.variant}).Painted(); got != c.painted {
			t.Errorf("Variant=%q: Painted()=%v，应为 %v", c.variant, got, c.painted)
		}
	}
	// --no-color 是「不要任何颜色」，铺底色也算颜色。
	if NewTheme(ThemeOptions{Variant: "light", NoColor: true}).Painted() {
		t.Error("--no-color 下不该铺底色")
	}
}

// 铺底之后每行都要盖上底色、宽度不变：右边露出一条终端底色就白铺了，
// 宽度变了鼠标命中就全错位。
func TestPaintedViewCoversEveryLine(t *testing.T) {
	for variant, base := range map[string]string{
		"dark":  "\x1b[38;5;252;48;5;234m",
		"light": "\x1b[38;5;235;48;5;255m",
	} {
		m := ready(t, demoApp(core.ActionUpdate), demoApp(core.ActionNoOp))
		m.theme = NewTheme(ThemeOptions{Variant: variant})
		m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

		view := m.render()
		lines := strings.Split(view, "\n")
		if len(lines) != 30 {
			t.Fatalf("%s: 应为 30 行，实际 %d", variant, len(lines))
		}
		for i, l := range lines {
			if !strings.HasPrefix(l, base) {
				t.Fatalf("%s: 第 %d 行没铺底色：%q", variant, i, l)
			}
			if got := Width(l); got != 100 {
				t.Fatalf("%s: 第 %d 行宽度 %d，应为 100", variant, i, got)
			}
			// 每次 SGR 复位都会把底色一并清掉，所以复位之后必须补回来。
			if strings.Contains(l, "\x1b[m") && !strings.Contains(l, "\x1b[m"+base) {
				t.Fatalf("%s: 第 %d 行复位之后没有补底色：%q", variant, i, l)
			}
		}
	}
}

// 弹窗走的是合成器，它会裁掉行尾空白；铺底要把那段空白补回来，
// 否则弹窗右侧会露出一条终端底色。
func TestPaintedModalStillCovered(t *testing.T) {
	m := ready(t, demoApp(core.ActionNoOp))
	m.theme = NewTheme(ThemeOptions{Variant: "light"})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = update(t, m, key('?'))

	view := m.render()
	for i, l := range strings.Split(view, "\n") {
		if got := Width(l); got != 100 {
			t.Fatalf("弹窗第 %d 行宽度 %d，应为 100", i, got)
		}
		if l != "" && !strings.HasPrefix(l, "\x1b[38;5;235;48;5;255m") {
			t.Fatalf("弹窗第 %d 行没铺底色：%q", i, l)
		}
	}
}

// Base 里的 48;5;N 与 BaseBg（交给终端的 OSC 11 背景色）必须是同一个颜色，
// 各写一份迟早会漂。这里从 256 色号反推灰度再与 BaseBg 比。
func TestBaseColorMatchesBackgroundColor(t *testing.T) {
	for variant, want := range map[string]struct {
		code int
		hex  string
	}{
		"dark":  {234, "#1c1c1c"},
		"light": {255, "#eeeeee"},
	} {
		th := NewTheme(ThemeOptions{Variant: variant})
		if !strings.Contains(th.p.Base, fmt.Sprintf("48;5;%d", want.code)) {
			t.Fatalf("%s: Base 里没有 48;5;%d：%q", variant, want.code, th.p.Base)
		}
		// 232..255 是灰阶：8 + (n-232)*10。
		v := 8 + (want.code-232)*10
		if got := fmt.Sprintf("#%02x%02x%02x", v, v, v); got != want.hex {
			t.Fatalf("色号 %d 应该是 %s，实际算成 %s", want.code, want.hex, got)
		}
		rgba := color.RGBAModel.Convert(th.Background()).(color.RGBA)
		if got := fmt.Sprintf("#%02x%02x%02x", rgba.R, rgba.G, rgba.B); got != want.hex {
			t.Fatalf("%s: 终端背景色是 %s，应为 %s", variant, got, want.hex)
		}
	}
	// 不铺底时不能去动终端的背景色。
	if bg := NewTheme(ThemeOptions{Variant: "auto"}).Background(); bg != nil {
		t.Errorf("auto 不该设置终端背景色：%v", bg)
	}
}
