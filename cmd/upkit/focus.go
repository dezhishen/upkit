package main

import "strings"

// focusEnv 是「已经以焦点模式启动过」的标记变量。
//
// 焦点模式要靠重新拉起自己实现，没有标记就会无限重启。
const focusEnv = "UPKIT_FOCUS"

// stripFocusFlag 从参数里摘掉 --focus 本身。
//
// 子进程不需要再走一遍这条分支：它已经由 wt 以焦点模式启动。Go 的 flag 包允许
// 单横线、双横线与 = 赋值三种写法，都要覆盖。
func stripFocusFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--focus", "-focus", "--focus=true", "-focus=true":
			continue
		}
		out = append(out, a)
	}
	return out
}

// focusCommandLine 组装交给 wt 的命令行。
//
// wt 会把选项之后的参数用空格重新拼成一条命令，再转交 CreateProcess 解析。
// 因此不能把可执行文件与参数作为多个参数传过去 —— 带空格的路径
// （C:\Program Files\...）会在 wt 那一侧被拆开。这里自行完成引号处理，并把
// 整条命令作为**单个**参数交给 wt。
func focusCommandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsArg(exe))
	for _, a := range args {
		parts = append(parts, quoteWindowsArg(a))
	}
	return strings.Join(parts, " ")
}

// quoteWindowsArg 按 Windows 命令行的解析规则给单个参数加引号。
//
// 规则来自 CommandLineToArgvW：反斜杠只在引号前有转义作用，因此字符串末尾的
// 连续反斜杠要加倍，引号前的反斜杠也要加倍。不按这套规则处理，路径里的
// `C:\dir\` 或 `\"` 会被解析成别的意思。
func quoteWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			backslashes++
			continue
		case '"':
			// 引号前的反斜杠要加倍，再加一个用于转义引号本身。
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
		}
		b.WriteRune(r)
	}
	// 结尾的反斜杠紧邻收尾引号，必须加倍。
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}
