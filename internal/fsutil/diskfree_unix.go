//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package fsutil

import (
	"fmt"
	"syscall"
)

// diskFree 通过 statfs 系统调用读取目录所在文件系统的可用空间。
//
// 保留 unix 实现是为了让开发机上的测试能跑真实路径（而不是走 diskfree_other 的
// 空实现）：upkit 只发行 Windows 版本，生产行为以 diskfree_windows.go 为准。
func diskFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
