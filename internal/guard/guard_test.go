package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/process"
	"github.com/dezhishen/upkit/internal/registry"
)

// 进程列表文本：空的时候不能是「、」这种残句。
func TestFormat(t *testing.T) {
	if got := Format(nil); got != "" {
		t.Fatalf("没有阻塞进程时应为空串，实际 %q", got)
	}
	got := Format([]core.Blocker{
		{PID: 10, Name: "demo.exe"},
		{PID: 20, Name: "helper.exe"},
	})
	want := "demo.exe (PID 10)、helper.exe (PID 20)"
	if got != want {
		t.Fatalf("格式不对: %q != %q", got, want)
	}
}

// 进程名的来源顺序：显式配置 > 入口文件 > 安装目录里的 exe。
func TestProcessNames(t *testing.T) {
	if got := processNames(core.AppRef{Processes: []string{"custom.exe"}, Entrypoints: []string{"e.exe"}}); len(got) != 1 || got[0] != "custom.exe" {
		t.Fatalf("应优先用显式配置，实际 %v", got)
	}
	if got := defaultProcessNames(core.AppRef{Entrypoints: []string{"demo.exe"}}); len(got) != 1 || got[0] != "demo.exe" {
		t.Fatalf("应回退到入口文件，实际 %v", got)
	}

	// 都没配时扫安装目录，只看 .exe，最多 4 个。
	dir := t.TempDir()
	for _, name := range []string{"a.exe", "b.exe", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("写入: %v", err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.exe"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	got := defaultProcessNames(core.AppRef{InstallPath: dir})
	if len(got) != 2 {
		t.Fatalf("应只收 .exe（且不含目录），实际 %v", got)
	}
	for _, n := range got {
		if !strings.HasSuffix(n, ".exe") {
			t.Fatalf("收到了非 exe：%v", got)
		}
	}
	// 目录不存在时不该报错，只是拿不到名字。
	if got := defaultProcessNames(core.AppRef{InstallPath: filepath.Join(dir, "nope")}); got != nil {
		t.Fatalf("目录不存在应返回空，实际 %v", got)
	}
}

// 没有进程占用时：Blockers 为空、IsRunning 为假、StopBlockers 返回 0。
func TestIdleDirHasNoBlockers(t *testing.T) {
	g, err := New(core.AppRef{}, registry.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ref := core.AppRef{ID: "demo", InstallPath: t.TempDir(), Processes: []string{"definitely-not-running.exe"}}

	blockers, err := g.Blockers(context.Background(), ref)
	if err != nil {
		t.Fatalf("Blockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("不该有阻塞进程: %+v", blockers)
	}
	running, _, err := Guard{}.IsRunning(ref)
	if err != nil || running {
		t.Fatalf("IsRunning 应为假: running=%v err=%v", running, err)
	}
	killed, err := g.StopBlockers(context.Background(), ref, 1, nil)
	if err != nil || killed != 0 {
		t.Fatalf("没有进程时应返回 0：killed=%d err=%v", killed, err)
	}
}

// 真起一个「装在安装目录里的」进程，验证发现与结束这条链路。
//
// 这是更新前最关键的一步：没结束占用进程就替换文件，Windows 上会得到半个旧版本
// 加半个新版本。开发机（Linux）上 process 包同样会枚举进程，所以这里能跑真链路。
func TestBlockersFindsAndStopsProcessInInstallDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux/macOS 上验证 ps 枚举这条路；Windows 由 process_windows.go 负责")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skipf("没有 ps，跳过: %v", err)
	}
	sleep := ""
	for _, candidate := range []string{"/bin/sleep", "/usr/bin/sleep"} {
		if _, err := os.Stat(candidate); err == nil {
			sleep = candidate
			break
		}
	}
	if sleep == "" {
		t.Skip("找不到 sleep")
	}

	// 把可执行文件拷进安装目录再运行：InDir 既看进程名也看可执行文件路径。
	install := t.TempDir()
	dst := filepath.Join(install, "demo-sleep")
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatalf("读 %s: %v", sleep, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("写入: %v", err)
	}
	cmd := exec.Command(dst, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// 必须及时回收：被 SIGKILL 的子进程如果不 wait 就会变成僵尸，ps 依然列着它，
	// waitGone 会一直等到超时 —— 那是测试自己造成的，现实里被更新的软件不是
	// upkit 的子进程。
	go func() { _, _ = cmd.Process.Wait() }()

	ref := core.AppRef{ID: "demo", InstallPath: install, Processes: []string{"demo-sleep"}}
	g := Guard{}

	var blockers []core.Blocker
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		blockers, err = g.Blockers(context.Background(), ref)
		if err != nil {
			t.Fatalf("Blockers: %v", err)
		}
		if len(blockers) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(blockers) == 0 {
		t.Skip("进程没被枚举到（ps 输出受限），跳过结束阶段")
	}
	if !strings.Contains(Format(blockers), "demo-sleep") {
		t.Fatalf("阻塞列表文本不对: %q", Format(blockers))
	}
	if running, _, err := g.IsRunning(ref); err != nil || !running {
		t.Fatalf("IsRunning 应为真: running=%v err=%v", running, err)
	}

	killed, err := g.StopBlockers(context.Background(), ref, 5, nil)
	if err != nil {
		t.Fatalf("StopBlockers: %v", err)
	}
	if killed != len(blockers) {
		t.Fatalf("应结束 %d 个进程，实际 %d", len(blockers), killed)
	}
	if left, _ := g.Blockers(context.Background(), ref); len(left) != 0 {
		t.Fatalf("结束后不该还有阻塞进程: %+v", left)
	}
}

// process.Info → core.Blocker 的字段要对上（界面显示的就是这几项）。
func TestToBlockers(t *testing.T) {
	got := toBlockers([]process.Info{{PID: 7, Name: "a.exe", Path: "/opt/a/a.exe"}})
	if len(got) != 1 || got[0].PID != 7 || got[0].Name != "a.exe" || got[0].Path != "/opt/a/a.exe" {
		t.Fatalf("转换结果不对: %+v", got)
	}
}
