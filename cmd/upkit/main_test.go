package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/settings"
)

// 便携布局：默认路径应为 <根目录>/config/settings.yaml，并建出全部同级目录。
func TestPortableLayoutBootstrap(t *testing.T) {
	root := t.TempDir()
	t.Setenv(settings.EnvHome, root)

	cfgPath, err := settings.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(root, settings.DirConfig, settings.FileName); cfgPath != want {
		t.Fatalf("默认设置路径应为 %s，实际 %s", want, cfgPath)
	}

	set, err := settings.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := set.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	for _, dir := range []string{
		filepath.Join(root, settings.DirConfig),
		filepath.Join(root, settings.DirLog),
		filepath.Join(root, settings.DirPlugin),
		filepath.Join(root, settings.DirData),
		filepath.Join(root, settings.DirCache),
		filepath.Join(root, settings.DirBackup),
		filepath.Join(root, settings.DirTemp),
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("目录未创建: %s（%v）", dir, err)
		}
	}

	if got := set.AppsPath(); got != filepath.Join(root, settings.DirConfig, settings.AppsFileName) {
		t.Fatalf("清单路径应为 config/apps.yaml，实际 %s", got)
	}
	if got := set.ManifestPath(); got != filepath.Join(root, settings.DirConfig, settings.ManifestFileName) {
		t.Fatalf("清单导出路径不正确: %s", got)
	}
	if set.Logs.Dir != filepath.Join(root, settings.DirLog) {
		t.Fatalf("日志目录应为 <root>/log，实际 %s", set.Logs.Dir)
	}
	if set.Plugins.Dir != filepath.Join(root, settings.DirPlugin) {
		t.Fatalf("插件目录应为 <root>/plugin，实际 %s", set.Plugins.Dir)
	}
}

func TestBootstrapWritesConfigFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv(settings.EnvHome, root)

	cfgPath, err := settings.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	set, err := settings.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := set.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	if err := set.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := os.Stat(set.Path); err != nil {
		t.Fatalf("settings.yaml 未生成: %v", err)
	}
	// 生成的设置里不应写死布局推导出的绝对路径
	data, err := os.ReadFile(set.Path)
	if err != nil {
		t.Fatalf("读取设置: %v", err)
	}
	if strings.Contains(string(data), root) {
		t.Fatalf("默认设置不应写死根目录路径:\n%s", data)
	}

	afs, err := apps.Load(set.AppsPath())
	if err != nil {
		t.Fatalf("apps.Load: %v", err)
	}
	if err := afs.Save(); err != nil {
		t.Fatalf("保存清单: %v", err)
	}
	if _, err := os.Stat(set.AppsPath()); err != nil {
		t.Fatalf("apps.yaml 未生成: %v", err)
	}
}
