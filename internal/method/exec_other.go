//go:build !windows

package method

import "os/exec"

// hideWindow 在类 Unix 平台上无需处理。
//
// 这个文件与 hideWindow_{unix} 分支的存在都是为了开发机上的编译与测试，
// 不是支持那些平台：upkit 只发行 Windows 版本，行为以 exec_windows.go 为准。
func hideWindow(*exec.Cmd) {}
