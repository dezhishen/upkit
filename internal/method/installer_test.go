package method

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

// installerApp 造一个「安装器类」条目：安装目录 + 入口文件 + 可选的方法选项。
func installerApp(dir string, opts map[string]string, entries ...string) core.AppRef {
	return core.AppRef{
		ID: "demo", Name: "Demo", InstallPath: dir, Entrypoints: entries,
		MethodOpts: opts,
	}
}

// 计划是纯计算：要能看出「执行安装器 + 安装后复核」两步，并提醒不能自动回滚。
func TestExeInstallerPlan(t *testing.T) {
	dir := t.TempDir()
	m, err := NewExeInstaller(installerApp(dir, nil, "demo.exe"), registry.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Name() != KindExeInstaller {
		t.Fatalf("Name 不对: %q", m.Name())
	}

	// 已是最新时计划原样返回，不塞步骤。
	noop := core.Plan{Action: core.ActionNoOp}
	if p, err := m.Plan(context.Background(), core.Request{Plan: noop}); err != nil || len(p.Steps) != 0 {
		t.Fatalf("NoOp 不该生成步骤: %+v err=%v", p, err)
	}

	p, err := m.Plan(context.Background(), core.Request{
		App:  installerApp(dir, nil, "demo.exe"),
		Plan: core.Plan{Action: core.ActionInstall, To: "1.0.0", Artifact: core.Artifact{Name: "setup.exe"}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Steps) != 2 || p.Steps[0].Kind != core.StepRun || p.Steps[1].Kind != core.StepVerify {
		t.Fatalf("步骤不对: %+v", p.Steps)
	}
	if !strings.Contains(p.Note, "无法自动回滚") {
		t.Fatalf("应提醒不能自动回滚，实际 %q", p.Note)
	}
	// 默认静默参数，且命令里是产物名（计划阶段还没有真实路径）。
	if got := strings.Join(p.Steps[0].Command, " "); !strings.Contains(got, "setup.exe") || !strings.Contains(got, "/S") {
		t.Fatalf("计划里的命令不对: %q", got)
	}
}

// 自定义参数要能引用宿主变量（版本、安装目录…），否则安装器脚本没法复用。
func TestExeInstallerCommandArgs(t *testing.T) {
	dir := t.TempDir()
	app := installerApp(dir, map[string]string{"args": "/S /DIR={target} /v{version}"}, "demo.exe")
	m, _ := NewExeInstaller(app, registry.Deps{})

	e := m.(*ExeInstaller)
	cmd := e.command(core.Request{App: app, Plan: core.Plan{}}, "setup.exe", "2.1.0")
	got := strings.Join(cmd, " ")
	if !strings.Contains(got, "setup.exe") || !strings.Contains(got, dir) || !strings.Contains(got, "2.1.0") {
		t.Fatalf("变量没替换: %q", got)
	}
}

// 真跑一个「安装器」：脚本落地入口文件 → 复核通过 → 返回结果。
//
// 用 shell 脚本代替 setup.exe：runStep 只关心「命令能不能跑通、退出码是不是 0」，
// 这正是安装器这一层在 Linux 开发机上可验证的部分。
func TestExeInstallerExecuteRunsAndVerifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本模拟安装器，只在类 Unix 上可跑")
	}
	dir := t.TempDir()
	install := filepath.Join(dir, "app")
	script := filepath.Join(dir, "setup.exe")
	writeInstaller(t, script, "mkdir -p "+install+" && echo bin > "+install+"/demo.exe")

	app := installerApp(install, nil, "demo.exe")
	m, _ := NewExeInstaller(app, registry.Deps{})
	var events []core.Event
	res, err := m.Execute(context.Background(),
		core.Request{App: app, Plan: core.Plan{Action: core.ActionInstall, To: "1.0.0"}, ArtifactPath: script},
		core.SinkFunc(func(e core.Event) { events = append(events, e) }))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.InstallPath != install || res.To != "1.0.0" {
		t.Fatalf("结果不对: %+v", res)
	}
	if len(events) == 0 || events[0].Phase != "安装" {
		t.Fatalf("应先报「安装」阶段事件: %+v", events)
	}
	if _, err := os.Stat(filepath.Join(install, "demo.exe")); err != nil {
		t.Fatalf("安装器没落地文件: %v", err)
	}
}

// 安装器退出码为 0 但入口文件没出现：必须当成失败（安装器静默失败是常见坑）。
func TestExeInstallerExecuteVerifiesEntrypoints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本模拟安装器，只在类 Unix 上可跑")
	}
	dir := t.TempDir()
	install := filepath.Join(dir, "app")
	script := filepath.Join(dir, "setup.exe")
	writeInstaller(t, script, "true") // 什么都不做，但退出码是 0

	app := installerApp(install, nil, "demo.exe")
	m, _ := NewExeInstaller(app, registry.Deps{})
	_, err := m.Execute(context.Background(),
		core.Request{App: app, Plan: core.Plan{}, ArtifactPath: script}, core.NopSink{})
	if err == nil || !strings.Contains(err.Error(), "demo.exe") {
		t.Fatalf("应报复核失败并指出缺了哪个入口文件，实际 %v", err)
	}

	// 没声明入口文件时复核不了 —— 那属于「没配」，不该判失败（复核这件事总得有人做，
	// 但责任在配置方，不在这一层）。
	app2 := installerApp(install, nil)
	m2, _ := NewExeInstaller(app2, registry.Deps{})
	if _, err := m2.Execute(context.Background(),
		core.Request{App: app2, Plan: core.Plan{}, ArtifactPath: script}, core.NopSink{}); err != nil {
		t.Fatalf("未声明入口文件时不该失败: %v", err)
	}
}

// 安装器失败、缺少文件：都要明确报错。
func TestExeInstallerExecuteErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本模拟安装器，只在类 Unix 上可跑")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "setup.exe")
	writeInstaller(t, script, "exit 3")

	app := installerApp(filepath.Join(dir, "app"), nil, "demo.exe")
	m, _ := NewExeInstaller(app, registry.Deps{})

	if _, err := m.Execute(context.Background(), core.Request{App: app, Plan: core.Plan{}}, core.NopSink{}); err == nil {
		t.Fatalf("没有安装器文件时应报错")
	}
	if _, err := m.Execute(context.Background(),
		core.Request{App: app, Plan: core.Plan{}, ArtifactPath: script}, core.NopSink{}); err == nil {
		t.Fatalf("安装器退出码非 0 时应报错")
	}
}

// 卸载：有声明就执行，没声明就明确说不支持；回滚一律不支持。
func TestExeInstallerUninstallAndRollback(t *testing.T) {
	dir := t.TempDir()

	noUninstall := installerApp(filepath.Join(dir, "app"), nil, "demo.exe")
	m, _ := NewExeInstaller(noUninstall, registry.Deps{})
	if err := m.Uninstall(context.Background(), core.Request{App: noUninstall, Plan: core.Plan{}}, core.UninstallOptions{}); err == nil {
		t.Fatalf("未声明卸载命令时应报不支持")
	}
	if err := m.Rollback(context.Background(), core.Request{}, ""); err == nil {
		t.Fatalf("安装器类不该支持回滚")
	}
	if bs, err := m.Backups(context.Background(), core.Request{}); err != nil || bs != nil {
		t.Fatalf("安装器类没有文件树备份: %v %v", bs, err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	// 声明了卸载脚本：应当真的跑起来（脚本落一个标记文件，便于断言）。
	marker := filepath.Join(dir, "uninstalled")
	script := filepath.Join(dir, "uninstall.sh")
	writeInstaller(t, script, "touch "+marker)
	app := installerApp(filepath.Join(dir, "app"), map[string]string{"uninstall": script}, "demo.exe")
	m2, _ := NewExeInstaller(app, registry.Deps{})
	if err := m2.Uninstall(context.Background(), core.Request{App: app, Plan: core.Plan{}}, core.UninstallOptions{KeepUserData: true}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("卸载脚本没被执行: %v", err)
	}
}

// msiexec：命令形状固定（msiexec /i|/x），卸载缺文件时明确报错。
func TestMSIExecPlanAndCommands(t *testing.T) {
	dir := t.TempDir()
	app := installerApp(dir, nil, "demo.exe")
	m, err := NewMSIExec(app, registry.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Name() != KindMSIExec || m.Caps().Rollbackable {
		t.Fatalf("msiexec 的能力位不对: %q %+v", m.Name(), m.Caps())
	}

	p, err := m.Plan(context.Background(), core.Request{
		App: app, Plan: core.Plan{Action: core.ActionInstall, To: "1.0.0", Artifact: core.Artifact{Name: "demo.msi"}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := strings.Join(p.Steps[0].Command, " ")
	if !strings.HasPrefix(got, "msiexec /i ") || !strings.Contains(got, "/qn") {
		t.Fatalf("安装命令不对: %q", got)
	}
	if p, err := m.Plan(context.Background(), core.Request{Plan: core.Plan{Action: core.ActionNoOp}}); err != nil || len(p.Steps) != 0 {
		t.Fatalf("NoOp 不该生成步骤: %+v", p)
	}

	// 卸载：没有 MSI 路径时说不清卸什么，必须报错；回滚不支持。
	if err := m.Uninstall(context.Background(), core.Request{App: app, Plan: core.Plan{}}, core.UninstallOptions{}); err == nil {
		t.Fatalf("缺少 MSI 路径时卸载应报错")
	}
	if err := m.Rollback(context.Background(), core.Request{}, ""); err == nil {
		t.Fatalf("MSI 不该支持回滚")
	}
	if bs, err := m.Backups(context.Background(), core.Request{}); err != nil || bs != nil {
		t.Fatalf("MSI 没有文件树备份: %v %v", bs, err)
	}
	if _, err := m.Execute(context.Background(), core.Request{App: app, Plan: core.Plan{}}, core.NopSink{}); err == nil {
		t.Fatalf("缺少 MSI 文件时应报错")
	}
}

// writeInstaller 写一个可执行的「安装器」脚本。
func writeInstaller(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("写入脚本: %v", err)
	}
}
