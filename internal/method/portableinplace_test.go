package method

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return string(data)
}

func setupPortable(t *testing.T) (core.AppRef, core.Request, string) {
	t.Helper()
	root := t.TempDir()
	install := filepath.Join(root, "app")
	src := filepath.Join(root, "src")

	write(t, filepath.Join(install, "app.exe"), "old")
	write(t, filepath.Join(install, "stale.dll"), "stale")
	write(t, filepath.Join(install, "keep.txt"), "user-data")

	write(t, filepath.Join(src, "app.exe"), "new")
	write(t, filepath.Join(src, "extra.txt"), "new")
	write(t, filepath.Join(src, "keep.txt"), "from-package")

	app := core.AppRef{
		ID:          "demo",
		Name:        "Demo",
		InstallPath: install,
		Entrypoints: []string{"app.exe"},
		Preserve:    []string{"keep.txt"},
	}
	req := core.Request{
		App:        app,
		Plan:       core.Plan{App: app, Action: core.ActionUpdate, From: "1.0.0", To: "2.0.0"},
		BackupDir:  filepath.Join(root, "backups"),
		KeepBackup: true,
		MaxBackups: 2,
		SourceRoot: src,
	}
	return app, req, root
}

// 绿色版安装：备份 → 清理 → 覆盖 → 复核，保护路径不被覆盖。
func TestPortableInPlaceExecute(t *testing.T) {
	app, req, _ := setupPortable(t)
	m, err := NewPortableInPlace(app, registryDeps())
	if err != nil {
		t.Fatalf("NewPortableInPlace: %v", err)
	}

	plan, err := m.Plan(context.Background(), req)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) == 0 || plan.Note == "" {
		t.Fatalf("计划应包含步骤与说明: %+v", plan)
	}
	req.Plan = plan

	res, err := m.Execute(context.Background(), req, core.NopSink{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := read(t, filepath.Join(app.InstallPath, "app.exe")); got != "new" {
		t.Fatalf("入口文件未更新: %q", got)
	}
	if got := read(t, filepath.Join(app.InstallPath, "keep.txt")); got != "user-data" {
		t.Fatalf("保护路径被覆盖: %q", got)
	}
	if _, err := os.Stat(filepath.Join(app.InstallPath, "stale.dll")); !os.IsNotExist(err) {
		t.Fatalf("旧文件未被清理")
	}
	if _, err := os.Stat(filepath.Join(app.InstallPath, "extra.txt")); err != nil {
		t.Fatalf("新文件未写入: %v", err)
	}
	if res.BackupPath == "" {
		t.Fatalf("应生成备份")
	}

	backups, err := m.Backups(context.Background(), req)
	if err != nil {
		t.Fatalf("Backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("期望 1 份备份，得到 %d", len(backups))
	}
	if backups[0].Version != "1.0.0" {
		t.Fatalf("备份应记录旧版本号: %+v", backups[0])
	}
}

// 备份缺失/未启用时，入口文件不存在的失败应被检出（不是静默成功）。
func TestPortableInPlaceFailsOnMissingEntrypoint(t *testing.T) {
	_, req, _ := setupPortable(t)
	req.SourceRoot = t.TempDir() // 空目录：没有 app.exe
	req.KeepBackup = false

	m, err := NewPortableInPlace(req.App, registryDeps())
	if err != nil {
		t.Fatalf("NewPortableInPlace: %v", err)
	}
	if _, err := m.Execute(context.Background(), req, core.NopSink{}); err == nil {
		t.Fatalf("缺少入口文件时应报错")
	}
}

// 回滚应把安装目录恢复到更新前（保护路径继续保留）。
func TestPortableInPlaceRollback(t *testing.T) {
	app, req, _ := setupPortable(t)
	m, err := NewPortableInPlace(app, registryDeps())
	if err != nil {
		t.Fatalf("NewPortableInPlace: %v", err)
	}
	res, err := m.Execute(context.Background(), req, core.NopSink{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := m.Rollback(context.Background(), req, res.BackupPath); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := read(t, filepath.Join(app.InstallPath, "app.exe")); got != "old" {
		t.Fatalf("回滚后入口文件应为旧版本: %q", got)
	}
	if _, err := os.Stat(filepath.Join(app.InstallPath, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("回滚后不应残留新版本文件")
	}
	if got := read(t, filepath.Join(app.InstallPath, "keep.txt")); got != "user-data" {
		t.Fatalf("回滚不应影响保护路径: %q", got)
	}
}

// 卸载：KeepUserData 为真时只删程序文件。
func TestPortableInPlaceUninstall(t *testing.T) {
	app, req, _ := setupPortable(t)
	m, err := NewPortableInPlace(app, registryDeps())
	if err != nil {
		t.Fatalf("NewPortableInPlace: %v", err)
	}
	if err := m.Uninstall(context.Background(), req, core.UninstallOptions{KeepUserData: true}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.InstallPath, "app.exe")); !os.IsNotExist(err) {
		t.Fatalf("程序文件应被删除")
	}
	if _, err := os.Stat(filepath.Join(app.InstallPath, "keep.txt")); err != nil {
		t.Fatalf("用户数据应被保留: %v", err)
	}
}

// 计划模板变量应可展开。
func TestExpandTemplate(t *testing.T) {
	app := core.AppRef{ID: "demo", Name: "Demo", InstallPath: "/opt/demo"}
	req := core.Request{
		ArtifactPath: "/tmp/setup-1.2.3.exe",
		WorkDir:      "/tmp/work",
		Plan:         core.Plan{To: "1.2.3", App: app},
	}
	got := expandTemplate("{artifact} {target} {version} {name} {workdir}", app, req, "1.2.3")
	want := "/tmp/setup-1.2.3.exe /opt/demo 1.2.3 demo /tmp/work"
	if got != want {
		t.Fatalf("模板展开不正确:\n got=%q\nwant=%q", got, want)
	}
}

// registryDeps 返回安装方式需要的最小依赖。
func registryDeps() registry.Deps {
	return registry.Deps{Clock: time.Now}
}
