package main

import (
	"reflect"
	"testing"
)

// --focus 本身必须从交给子进程的参数里摘掉，否则子进程会再走一遍这条分支。
// 单横线、双横线与 = 赋值三种写法都要覆盖。
func TestStripFocusFlag(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"--focus"}, nil},
		{[]string{"-focus"}, nil},
		{[]string{"--focus=true"}, nil},
		{[]string{"-focus=true"}, nil},
		{[]string{"--focus", "--ascii"}, []string{"--ascii"}},
		{[]string{"--config", "C:\\a b\\s.yaml", "--focus"}, []string{"--config", "C:\\a b\\s.yaml"}},
		{[]string{"--ascii", "--no-color"}, []string{"--ascii", "--no-color"}},
		{nil, nil},
	}
	for _, c := range cases {
		got := stripFocusFlag(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("stripFocusFlag(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// Windows 命令行引号规则：反斜杠只在引号前有转义作用。
func TestQuoteWindowsArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"upkit.exe", "upkit.exe"},
		// 不含空格等特殊字符时原样返回：反斜杠在引号之外没有转义作用，
		// 强行加引号反而会把它变成转义字符。
		{`C:\tools\upkit.exe`, `C:\tools\upkit.exe`},
		{`C:\dir\`, `C:\dir\`},
		{`C:\Program Files\upkit.exe`, `"C:\Program Files\upkit.exe"`},
		// 有引号参与时，结尾反斜杠紧邻收尾引号，必须加倍。
		{`C:\dir with space\`, `"C:\dir with space\\"`},
		// 引号前的反斜杠要加倍，引号本身再加一个反斜杠转义。
		{`a\"b`, `"a\\\"b"`},
		{"a b", `"a b"`},
		{"tab\there", "\"tab\there\""},
	}
	for _, c := range cases {
		if got := quoteWindowsArg(c.in); got != c.want {
			t.Fatalf("quoteWindowsArg(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 交给 wt 的必须是**一条**命令：wt 会把参数用空格重新拼起来再解析，
// 分开传会让带空格的路径被拆散。
func TestFocusCommandLine(t *testing.T) {
	got := focusCommandLine(`C:\Program Files\upkit\upkit.exe`,
		[]string{"--config", `C:\Users\张 三\config\settings.yaml`, "--ascii"})
	want := `"C:\Program Files\upkit\upkit.exe" --config "C:\Users\张 三\config\settings.yaml" --ascii`
	if got != want {
		t.Fatalf("focusCommandLine =\n  %s\n期望\n  %s", got, want)
	}
}
