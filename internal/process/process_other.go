//go:build !windows

package process

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// 本文件是开发机（Linux/macOS）上的实现：通过 ps 枚举进程。
//
// 它不是为了支持这些平台 —— upkit 只发行 Windows 版本，行为以 process_windows.go
// 为准。这里存在的意义是让 go build / go test 在开发机上跑得起来，并能对这一层
// 的逻辑做单元测试。

// list 在类 Unix 平台上通过 ps 枚举进程，并尽力解析可执行文件路径
// （Linux 上读取 /proc/<pid>/exe，其它平台留空）。
func list() ([]Info, error) {
	cmd := exec.Command("ps", "-eo", "pid=,comm=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("执行 ps: %w", err)
	}

	var result []Info
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		name := strings.Join(fields[1:], " ")
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		result = append(result, Info{PID: pid, Name: name, Path: imagePath(pid)})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("解析 ps 输出: %w", err)
	}
	return result, nil
}

// imagePath 在 Linux 上通过 /proc/<pid>/exe 读取真实路径。
func imagePath(pid int) string {
	link := filepath.Join("/proc", strconv.Itoa(pid), "exe")
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(target, " (deleted)")
}

// kill 发送 SIGKILL 结束进程。
func kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("查找进程 %d: %w", pid, err)
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("结束进程 %d: %w", pid, err)
	}
	return nil
}

// start 以新会话方式启动子进程。
func start(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s: %w", exe, err)
	}
	return cmd.Process.Release()
}
