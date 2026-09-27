package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/settings"
)

func ref(id, name, path string, entries ...string) core.AppRef {
	return core.AppRef{ID: id, Name: name, InstallPath: path, Entrypoints: entries}
}

// 临时目录名要吃得下限定 ID（<来源ID>/<软件ID>）。
//
// MkdirTemp 拒收带路径分隔符的 pattern（mkdirtemp: pattern contains path separator），
// 而限定 ID 里的 "/" 是设计的一部分，不是谁写错了 —— 不净化的话安装会卡在一句
// 「创建临时目录」上。
func TestWorkDirAcceptsQualifiedAppID(t *testing.T) {
	set := settings.Default()
	set.Storage.TempDir = filepath.Join(t.TempDir(), "temp")
	e := &Engine{settings: set}

	dir, cleanup, err := e.workDir(core.AppRef{ID: "upkit-hub/ungoogled-chromium"})
	if err != nil {
		t.Fatalf("workDir: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("临时目录应当存在: %v", err)
	}
	if got := filepath.Base(dir); !strings.Contains(got, "upkit-hub_ungoogled-chromium") {
		t.Errorf("目录名应含净化后的 ID，实际 %q", got)
	}
}

// 同一个安装目录只保留第一个条目，其余标记为遮蔽。
func TestNormalizeConflictsSameTarget(t *testing.T) {
	list := []*App{
		{Ref: ref("a", "A", "/opt/a", "a.exe")},
		{Ref: ref("b", "B", "/opt/a/", "b.exe")},
		{Ref: ref("c", "C", "/opt/c", "c.exe")},
	}
	normalizeConflicts(list, apps.Default())

	if list[1].Shadowed != true {
		t.Fatalf("同目标条目应被遮蔽")
	}
	if list[1].Conflict == nil || list[1].Conflict.Kind != ConflictSameTarget {
		t.Fatalf("冲突类型不正确: %+v", list[1].Conflict)
	}
	if list[0].Shadowed || list[2].Shadowed {
		t.Fatalf("不应影响其它条目")
	}
	if list[1].Action != core.ActionNoOp {
		t.Fatalf("被遮蔽条目应为 noop: %v", list[1].Action)
	}
}

// 名称与入口文件相同视为疑似重复。
func TestNormalizeConflictsFuzzy(t *testing.T) {
	list := []*App{
		{Ref: ref("a", "Demo", "/opt/a", "demo.exe")},
		{Ref: ref("b", "Demo", "/opt/b", "demo.exe")},
	}
	normalizeConflicts(list, apps.Default())
	if !list[1].Shadowed || list[1].Conflict.Kind != ConflictFuzzy {
		t.Fatalf("疑似重复应被遮蔽: %+v", list[1].Conflict)
	}
}

// 用户手工声明的等价组会让后面的成员被遮蔽。
func TestNormalizeConflictsEquivalents(t *testing.T) {
	list := []*App{
		{Ref: ref("a", "A", "/opt/a", "a.exe")},
		{Ref: ref("b", "B", "/opt/b", "b.exe")},
	}
	file := apps.Default()
	file.Equivalents = []apps.Equivalence{{Canonical: "a", Members: []string{"a", "b"}, Policy: "block"}}
	normalizeConflicts(list, file)
	if !list[1].Shadowed {
		t.Fatalf("等价组成员应被遮蔽")
	}
}

// 把等价组写进 not_equivalent 后，应解除遮蔽。
func TestNormalizeConflictsNotEquivalent(t *testing.T) {
	list := []*App{
		{Ref: ref("a", "A", "/opt/a", "a.exe")},
		{Ref: ref("b", "B", "/opt/b", "b.exe")},
	}
	file := apps.Default()
	file.Equivalents = []apps.Equivalence{{Members: []string{"a", "b"}}}
	file.NotEquivalent = [][]string{{"b", "a"}}
	normalizeConflicts(list, file)
	if list[1].Shadowed {
		t.Fatalf("用户已否认等价，不应再遮蔽")
	}
}

// 动作判定：未安装 → install，落后 → update，相同 → noop。
func TestDecide(t *testing.T) {
	rel := func(v string) core.Release { return core.Release{Version: v} }
	cases := []struct {
		name  string
		ref   core.AppRef
		st    core.Status
		rel   core.Release
		force bool
		want  core.Action
	}{
		{"未安装", ref("a", "A", "/opt/a"), core.Status{}, rel("1.0.0"), false, core.ActionInstall},
		{"可更新", ref("a", "A", "/opt/a"), core.Status{Installed: true, Version: "0.9.0"}, rel("1.0.0"), false, core.ActionUpdate},
		{"已最新", ref("a", "A", "/opt/a"), core.Status{Installed: true, Version: "1.0.0"}, rel("1.0.0"), false, core.ActionNoOp},
		{"本地更新", ref("a", "A", "/opt/a"), core.Status{Installed: true, Version: "2.0.0"}, rel("1.0.0"), false, core.ActionNoOp},
		{"强制重装", ref("a", "A", "/opt/a"), core.Status{Installed: true, Version: "1.0.0"}, rel("1.0.0"), true, core.ActionReinstall},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note := decide(c.ref, c.st, c.rel, c.force)
			if got != c.want {
				t.Fatalf("期望 %v，得到 %v", c.want, got)
			}
			if note == "" {
				t.Fatalf("应给出说明文字")
			}
		})
	}
}

// 同版本不同修订号：开启 track_revision 时应视为可更新。
func TestDecideTrackRevision(t *testing.T) {
	r := ref("a", "A", "/opt/a")
	r.TrackRevision = true
	got, _ := decide(r, core.Status{Installed: true, Version: "1.0.0-1.1"}, core.Release{Version: "1.0.0-1.2"}, false)
	if got != core.ActionUpdate {
		t.Fatalf("修订号更新应判定为 update，得到 %v", got)
	}
	r.TrackRevision = false
	if got, _ := decide(r, core.Status{Installed: true, Version: "1.0.0-1.1"}, core.Release{Version: "1.0.0-1.2"}, false); got != core.ActionNoOp {
		t.Fatalf("未开启 track_revision 应判定为 noop，得到 %v", got)
	}
}

// 固定版本（pin）时不看修订号。
func TestDecidePinned(t *testing.T) {
	r := ref("a", "A", "/opt/a")
	r.TrackRevision = true
	r.Pin = "1.0.0-1.1"
	got, _ := decide(r, core.Status{Installed: true, Version: "1.0.0-1.1"}, core.Release{Version: "1.0.0-1.2"}, false)
	if got != core.ActionNoOp {
		t.Fatalf("固定版本时不应自动更新，得到 %v", got)
	}
}

// request 应把设置里的目录接到请求上。
func TestRequestWiring(t *testing.T) {
	dir := t.TempDir()
	set := settings.Default()
	set.Path = dir + "/settings.yaml"
	set.Storage.DataDir = dir
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	e := &Engine{settings: set}
	req := e.request(ref("a", "A", "/opt/a"), core.Plan{})
	if req.CacheDir != set.Storage.CacheDir || req.WorkDir != set.Storage.TempDir {
		t.Fatalf("请求未接线: %+v", req)
	}
	if req.BackupDir == "" || !req.Verify {
		t.Fatalf("备份目录与校验开关应可用: %+v", req)
	}
}
