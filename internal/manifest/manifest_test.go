package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/settings"
)

// 夹具：一份设置 + 一份清单。
func newFixture(t *testing.T) (*settings.Settings, *apps.File) {
	t.Helper()
	root := t.TempDir()
	set := settings.Default()
	set.Path = filepath.Join(root, settings.DirConfig, settings.FileName)
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	afs := apps.Default()
	afs.Path = set.AppsPath()
	afs.Apps = []apps.AppSpec{{
		ID:      "demo",
		Name:    "Demo",
		Source:  map[string]any{apps.KeyKind: "github-release", "repo": "owner/repo"},
		Install: apps.InstallSpec{Path: filepath.Join(root, "demo"), Entrypoints: []string{"demo.exe"}},
	}}
	return set, afs
}

// 导出 → 读回：设置与清单都要完整（这是「换台机器接着用」唯一的载体）。
func TestExportLoadRoundTrip(t *testing.T) {
	set, afs := newFixture(t)
	path := filepath.Join(t.TempDir(), "manifest.json")

	got, err := Export(path, set, afs, map[string]string{"demo": "1.2.3"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if got != path {
		t.Fatalf("返回的落盘路径不对: %q", got)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Format != Version || f.Tool != "upkit" || f.ExportedAt.IsZero() {
		t.Fatalf("文件头不对: %+v", f)
	}
	if f.Apps == nil || len(f.Apps.Apps) != 1 || f.Apps.Apps[0].ID != "demo" {
		t.Fatalf("清单没带上: %+v", f.Apps)
	}
	if f.Settings == nil || f.Settings.Storage.BudgetMB != set.Storage.BudgetMB {
		t.Fatalf("设置没带上: %+v", f.Settings)
	}
	if f.Settings.Path != "" {
		t.Fatalf("导出的设置不该带本机的文件路径: %q", f.Settings.Path)
	}
	if f.Installed["demo"] != "1.2.3" {
		t.Fatalf("已装版本没带上: %v", f.Installed)
	}
}

// 不指定路径时落到 <根目录>/config/upkit-manifest.json。
func TestExportUsesSettingsPath(t *testing.T) {
	set, afs := newFixture(t)

	path, err := Export("", set, afs, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if path != set.ManifestPath() {
		t.Fatalf("默认路径应为 %q，实际 %q", set.ManifestPath(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件没写出来: %v", err)
	}
}

// 没有设置时也要能导出（只导清单）—— 之前这里会空指针，因为无条件解引用了设置。
func TestExportWithoutSettings(t *testing.T) {
	_, afs := newFixture(t)
	path := filepath.Join(t.TempDir(), "manifest.json")

	if _, err := Export(path, nil, afs, nil); err != nil {
		t.Fatalf("Export: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Settings != nil {
		t.Fatalf("没有设置时不该写出设置段: %+v", f.Settings)
	}
}

// 读回时的错误要能指认出问题（文件损坏、缺清单）。
func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := Load(filepath.Join(dir, "nope.json")); err == nil {
		t.Fatalf("文件不存在应报错")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "解析") {
		t.Fatalf("损坏的 JSON 应报解析错误，实际 %v", err)
	}

	noApps := filepath.Join(dir, "noapps.json")
	if err := os.WriteFile(noApps, []byte(`{"format":1,"tool":"upkit"}`), 0o644); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if _, err := Load(noApps); err == nil || !strings.Contains(err.Error(), "不含软件清单") {
		t.Fatalf("缺清单应明确报错，实际 %v", err)
	}
}

// 合并：同 id 覆盖、新 id 追加，等价组也带过来。
func TestMergeApps(t *testing.T) {
	_, cur := newFixture(t)
	cur.Apps[0].Name = "旧名字"

	f := &File{Apps: &apps.File{Apps: []apps.AppSpec{
		{ID: "demo", Name: "新名字"},
		{ID: "extra", Name: "Extra"},
	}, Equivalents: []apps.Equivalence{{
		Canonical: "demo", Members: []string{"demo", "demo-cli"}, Policy: "block",
	}}}}

	added, updated := f.MergeApps(cur)
	if len(updated) != 1 || updated[0] != "demo" {
		t.Fatalf("demo 应记为覆盖，实际 %v", updated)
	}
	if len(added) != 1 || added[0] != "extra" {
		t.Fatalf("extra 应记为新增，实际 %v", added)
	}
	if len(cur.Apps) != 2 {
		t.Fatalf("合并后应有 2 条，实际 %d", len(cur.Apps))
	}
	if cur.Apps[0].Name != "新名字" {
		t.Fatalf("同 id 应被覆盖，实际 %q", cur.Apps[0].Name)
	}
	if len(cur.Equivalents) != 1 || cur.Equivalents[0].Canonical != "demo" {
		t.Fatalf("等价组没带过来: %+v", cur.Equivalents)
	}
}

// 导出时间要是本次导出（而不是零值），否则「什么时候导的」无从判断。
func TestExportedAtIsNow(t *testing.T) {
	set, afs := newFixture(t)
	path := filepath.Join(t.TempDir(), "m.json")
	if _, err := Export(path, set, afs, nil); err != nil {
		t.Fatalf("Export: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d := time.Since(f.ExportedAt); d < 0 || d > time.Minute {
		t.Fatalf("导出时间不对: %v", f.ExportedAt)
	}
}
