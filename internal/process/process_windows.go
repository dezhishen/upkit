//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Win32 常量。
const (
	th32csSnapProcess              = 0x00000002
	invalidHandleValue             = ^uintptr(0)
	processTerminate               = 0x0001
	processQueryLimitedInformation = 0x1000
	synchronize                    = 0x00100000
	detachedProcess                = 0x00000008
	createNewProcessGroup          = 0x00000200
	waitTimeout                    = 3000
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot   = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW            = kernel32.NewProc("Process32FirstW")
	procProcess32NextW             = kernel32.NewProc("Process32NextW")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procTerminateProcess           = kernel32.NewProc("TerminateProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procWaitForSingleObject        = kernel32.NewProc("WaitForSingleObject")
)

// processEntry32 对应 Win32 的 PROCESSENTRY32W 结构。
type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [syscall.MAX_PATH]uint16
}

// list 使用 Toolhelp32 快照枚举进程，并尽力解析可执行文件路径。
func list() ([]Info, error) {
	snapshot, _, callErr := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snapshot == invalidHandleValue {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot 失败: %w", callErr)
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	ret, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil, nil
	}

	out := make([]Info, 0, 128)
	for {
		if entry.ProcessID != 0 {
			out = append(out, Info{
				PID:  int(entry.ProcessID),
				Name: syscall.UTF16ToString(entry.ExeFile[:]),
				Path: imagePath(entry.ProcessID),
			})
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ret, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
	return out, nil
}

// imagePath 通过 QueryFullProcessImageNameW 取得进程映像路径。
// 受权限限制失败时返回空字符串。
func imagePath(pid uint32) string {
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return ""
	}
	defer procCloseHandle.Call(handle)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageNameW.Call(
		handle, 0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 || size == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

// kill 强制结束进程并短暂等待其回收。
func kill(pid int) error {
	handle, _, callErr := procOpenProcess.Call(
		uintptr(processTerminate|processQueryLimitedInformation|synchronize),
		0,
		uintptr(uint32(pid)),
	)
	if handle == 0 {
		return fmt.Errorf("OpenProcess(%d) 失败: %w", pid, callErr)
	}
	defer procCloseHandle.Call(handle)

	if ret, _, callErr := procTerminateProcess.Call(handle, 1); ret == 0 {
		return fmt.Errorf("TerminateProcess(%d) 失败: %w", pid, callErr)
	}
	procWaitForSingleObject.Call(handle, waitTimeout)
	return nil
}

// start 以 DETACHED_PROCESS 方式启动子进程，使其不随 upkit 退出而结束。
func start(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s: %w", exe, err)
	}
	return cmd.Process.Release()
}
