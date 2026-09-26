//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ensureFocusMode 用「焦点模式」重新拉起自己，返回是否已完成交接。
//
// 焦点模式是 Windows Terminal 的特性：进入后标题栏与标签行一起隐藏，只剩终端
// 内容。应用没有办法改变宿主窗口的装饰，也没有接口让**当前窗口**切换到焦点模式
// （那是 toggleFocusMode 这个用户动作），唯一可行的路径是请宿主另开一个窗口。
//
// 因此这里启动新进程并让当前进程退出。返回 true 时调用方必须立即返回，否则会
// 短暂出现两个界面。
func ensureFocusMode() (bool, error) {
	// 子进程带着标记启动，据此判断「已经在焦点模式里了」。
	if os.Getenv(focusEnv) != "" {
		return false, nil
	}
	if os.Getenv("WT_SESSION") == "" {
		return false, errors.New(
			"--focus 需要 Windows Terminal，当前不在其中运行。\n" +
				"  在 Windows Terminal 里可先按 Alt+Enter 全屏；\n" +
				"  或在其 settings.json 根节点加上 \"launchMode\": \"focus\"，此后每次启动都无标题栏")
	}
	wt, err := exec.LookPath("wt.exe")
	if err != nil {
		return false, errors.New(
			"未找到 wt.exe。请确认已安装 Windows Terminal，" +
				"并在「设置 → 应用 → 高级应用设置 → 应用执行别名」中启用 wt")
	}
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("定位自身可执行文件: %w", err)
	}
	line := focusCommandLine(exe, stripFocusFlag(os.Args[1:]))
	if strings.Contains(line, ";") {
		return false, errors.New(
			"启动参数中含有分号，而 wt 用分号分隔多条命令，无法安全传递。\n" +
				"  请改用 Windows Terminal 的 \"launchMode\": \"focus\" 设置")
	}
	cmd := exec.Command(wt, "-f", line)
	cmd.Env = append(os.Environ(), focusEnv+"=1")
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("启动 Windows Terminal: %w", err)
	}
	return true, nil
}
