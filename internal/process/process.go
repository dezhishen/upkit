// Package process 提供进程枚举、按安装目录筛选与强制结束能力。
//
// 关键设计：结束进程时不仅按名字匹配，还要求可执行文件位于目标安装目录内，
// 这样即使用户同时开着系统里其它基于 Chromium 的浏览器也不会被误杀。
// 在 Windows 上通过 Toolhelp32 + QueryFullProcessImageNameW 原生实现，
// 不依赖 tasklist/taskkill/wmic 等外部命令。
package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Info 描述一个进程。
type Info struct {
	// PID 是进程号。
	PID int
	// Name 是可执行文件名，例如 chrome.exe。
	Name string
	// Path 是可执行文件完整路径，无法获取时为空。
	Path string
}

// String 返回便于日志展示的描述。
func (i Info) String() string {
	if i.Path != "" {
		return fmt.Sprintf("%s (PID %d, %s)", i.Name, i.PID, i.Path)
	}
	return fmt.Sprintf("%s (PID %d)", i.Name, i.PID)
}

// List 枚举当前所有进程（平台相关实现）。
func List() ([]Info, error) { return list() }

// Matching 返回名称命中 names 的进程。
//
// names 中的名字大小写不敏感，Windows 下会自动补齐 .exe 后缀。
func Matching(names []string) ([]Info, error) {
	all, err := list()
	if err != nil {
		return nil, err
	}
	want := make(map[string]struct{}, len(names)*2)
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		want[n] = struct{}{}
		want[strings.TrimSuffix(n, ".exe")] = struct{}{}
		want[strings.TrimSuffix(n, ".exe")+".exe"] = struct{}{}
	}
	var out []Info
	for _, p := range all {
		if _, ok := want[strings.ToLower(p.Name)]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// InDir 返回可执行文件位于 dir 目录内、且名字命中 names 的进程。
//
// 若进程路径不可得（权限不足等），则退化为仅按名称匹配。
func InDir(names []string, dir string) ([]Info, error) {
	matched, err := Matching(names)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return matched, nil
	}
	var out []Info
	for _, p := range matched {
		if p.Path == "" {
			// 拿不到路径时保守处理：宁可提示用户，也不要漏关进程。
			out = append(out, p)
			continue
		}
		if pathWithin(dir, p.Path) {
			out = append(out, p)
		}
	}
	return out, nil
}

// pathWithin 报告 file 是否位于 dir 目录之下。
func pathWithin(dir, file string) bool {
	dirAbs, err1 := filepath.Abs(dir)
	fileAbs, err2 := filepath.Abs(file)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(dirAbs, fileAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Kill 强制结束单个进程（平台相关实现）。
func Kill(pid int) error { return kill(pid) }

// KillAll 结束一批进程并等待其退出，返回成功结束的数量。
//
// log 可为 nil；timeout 为等待所有进程消失的最长时间。
func KillAll(procs []Info, timeout time.Duration, log func(format string, a ...any)) (int, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if len(procs) == 0 {
		return 0, nil
	}
	var errs []error
	killed := 0
	for _, p := range procs {
		log("结束进程 %s", p)
		if err := kill(p.PID); err != nil {
			errs = append(errs, fmt.Errorf("结束 %s: %w", p, err))
			continue
		}
		killed++
	}
	if err := waitGone(procs, timeout); err != nil {
		errs = append(errs, err)
	}
	return killed, errors.Join(errs...)
}

// waitGone 轮询等待指定进程全部消失。
func waitGone(procs []Info, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	targets := make(map[int]struct{}, len(procs))
	for _, p := range procs {
		targets[p.PID] = struct{}{}
	}
	for {
		all, err := list()
		if err != nil {
			return fmt.Errorf("枚举进程: %w", err)
		}
		alive := 0
		for _, p := range all {
			if _, ok := targets[p.PID]; ok {
				alive++
			}
		}
		if alive == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %d 个进程退出超时（%s）", alive, timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Running 报告是否存在运行中的目标进程。
func Running(names []string) (bool, []Info, error) {
	procs, err := Matching(names)
	if err != nil {
		return false, nil, err
	}
	return len(procs) > 0, procs, nil
}

// Start 以「分离进程」的方式启动可执行文件，upkit 退出后它继续运行。
func Start(exe string, args ...string) error {
	if !fileExists(exe) {
		return fmt.Errorf("可执行文件不存在: %s", exe)
	}
	return start(exe, args)
}

// fileExists 报告路径是否为已存在的普通文件。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
