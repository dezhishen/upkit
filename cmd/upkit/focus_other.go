//go:build !windows

package main

import "errors"

// ensureFocusMode 在非 Windows 平台上不可用。
//
// 这个文件与 focus_windows.go 的分支都只为开发机上的编译与测试而存在，
// 不是支持那些平台：upkit 只发行 Windows 版本，行为以 focus_windows.go 为准。
func ensureFocusMode() (bool, error) {
	return false, errors.New("--focus 依赖 Windows Terminal，仅 Windows 可用")
}
