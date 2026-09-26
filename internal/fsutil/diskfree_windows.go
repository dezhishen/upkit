//go:build windows

package fsutil

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32FreeSpace = syscall.NewLazyDLL("kernel32.dll")
	procFreeSpaceEx   = kernel32FreeSpace.NewProc("GetDiskFreeSpaceExW")
)

// diskFree 使用 Win32 GetDiskFreeSpaceExW 读取卷可用空间。
// 纯 LazyDLL 调用，不依赖任何外部命令。
func diskFree(dir string) (uint64, error) {
	pathPtr, err := syscall.UTF16PtrFromString(ensureVolumeRoot(dir))
	if err != nil {
		return 0, fmt.Errorf("解析路径 %s: %w", dir, err)
	}
	var freeAvail, total, totalFree uint64
	ret, _, callErr := procFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW(%s): %w", dir, callErr)
	}
	return freeAvail, nil
}

// ensureVolumeRoot 保证传给 API 的路径带盘符前缀（否则会被当作相对路径）。
func ensureVolumeRoot(dir string) string {
	if dir == "" {
		return `C:\`
	}
	if strings.HasPrefix(dir, `\\`) {
		return dir
	}
	if len(dir) >= 2 && dir[1] == ':' {
		return dir
	}
	return dir
}
