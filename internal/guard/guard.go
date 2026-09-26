// Package guard 实现 core.RuntimeGuard：替换前结束占用安装目录的进程。
//
// 关键：只结束「可执行文件位于安装目录内」的进程，避免误杀系统里其它同名的
// Chromium 系浏览器。实现完全基于标准库（Windows 下走原生 API）。
package guard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/process"
	"github.com/dezhishen/upkit/internal/registry"
)

// New 构造进程守卫。
func New(_ core.AppRef, _ registry.Deps) (core.RuntimeGuard, error) {
	return Guard{}, nil
}

// Guard 是默认实现。
type Guard struct{}

// Blockers 返回占用安装目录的进程。
func (Guard) Blockers(_ context.Context, app core.AppRef) ([]core.Blocker, error) {
	names := app.Processes
	if len(names) == 0 {
		names = defaultProcessNames(app)
	}
	procs, err := process.InDir(names, app.InstallPath)
	if err != nil {
		return nil, err
	}
	return toBlockers(procs), nil
}

// StopBlockers 结束进程并等待其退出，返回结束数量。
func (Guard) StopBlockers(_ context.Context, app core.AppRef, timeoutSeconds int, log func(string, ...any)) (int, error) {
	procs, err := process.InDir(processNames(app), app.InstallPath)
	if err != nil {
		return 0, err
	}
	if len(procs) == 0 {
		return 0, nil
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return process.KillAll(procs, timeout, log)
}

// IsRunning 报告是否有进程占用安装目录。
func (Guard) IsRunning(app core.AppRef) (bool, []core.Blocker, error) {
	blockers, err := Guard{}.Blockers(context.Background(), app)
	if err != nil {
		return false, nil, err
	}
	return len(blockers) > 0, blockers, nil
}

func processNames(app core.AppRef) []string {
	if len(app.Processes) > 0 {
		return app.Processes
	}
	return defaultProcessNames(app)
}

// defaultProcessNames 未显式配置时，用入口文件名作为进程名。
func defaultProcessNames(app core.AppRef) []string {
	if len(app.Entrypoints) > 0 {
		return append([]string(nil), app.Entrypoints...)
	}
	// 兜底：安装目录下的 .exe 文件名（最多取前 4 个，避免大目录遍历）
	entries, err := os.ReadDir(app.InstallPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.EqualFold(filepath.Ext(name), ".exe") {
			continue
		}
		names = append(names, name)
		if len(names) >= 4 {
			break
		}
	}
	return names
}

func toBlockers(procs []process.Info) []core.Blocker {
	out := make([]core.Blocker, 0, len(procs))
	for _, p := range procs {
		out = append(out, core.Blocker{PID: p.PID, Name: p.Name, Path: p.Path})
	}
	return out
}

// Format 生成人类可读的进程列表文本。
func Format(blockers []core.Blocker) string {
	var b strings.Builder
	for i, bl := range blockers {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s (PID %d)", bl.Name, bl.PID)
	}
	return b.String()
}
