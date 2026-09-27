package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/manifest"
)

// 剩下这些分支都是「出错时怎么办」：写盘失败、清单展开失败、插件目录读不了。
// 它们不会天天发生，但一旦发生就是「用户以为改成了、其实没改」这类最坏结果。
func TestFailureBranches(t *testing.T) {
	ctx := context.Background()

	// 1) 启停来源写盘失败必须报错（内存改了、磁盘没改，重启就回去了）。
	broken := newPluginControl(t, nil)
	_ = os.Remove(broken.afs.Path)
	if err := os.MkdirAll(broken.afs.Path, 0o755); err != nil {
		t.Fatalf("占住清单路径: %v", err)
	}
	if err := broken.ctrl.SetSourceEnabled(ctx, "example-static", false); err == nil ||
		!strings.Contains(err.Error(), "保存清单") {
		t.Fatalf("写盘失败应报错: %v", err)
	}

	// 2) 装成功但「记信任」写不进去：必须把失败报出来，不能报成安装成功。
	f := newPluginControl(t, nil)
	payload, err := os.ReadFile(f.execPath)
	if err != nil {
		t.Fatalf("读插件: %v", err)
	}
	fs := newTLSFeed(t, payload)
	fs.setFeed(feedBody("example-static", "2.0.0", f.sha, len(payload), "plugin.exe"))
	f.opts.HTTPClient = fs.srv.Client()
	// 清单目录先占住：安装本身（写插件文件）会成功，「记信任」那一步写盘失败。
	if err := os.MkdirAll(f.afs.Path, 0o755); err != nil {
		t.Fatalf("占住清单路径: %v", err)
	}
	ctrl := newControllerWith(t, f)
	entries, err := ctrl.FeedEntries(ctx, fs.feedURL())
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	res, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entries[0], nil)
	if err == nil || !strings.Contains(err.Error(), "信任") {
		t.Fatalf("记信任失败应报错: %v", err)
	}
	if res == nil || res.SHA256 == "" {
		t.Fatalf("安装结果仍应返回，交由调用方处置: %+v", res)
	}

	// 3) 导入清单：落盘失败与展开失败都要报错。
	ctrl2 := newSettingsTestController(t)
	extra := apps.Default()
	extra.Apps = []apps.AppSpec{pluginSpec("extra", "corp", filepath.Join(t.TempDir(), "extra"))}
	fresh := filepath.Join(t.TempDir(), "manifest.json")
	if _, err := manifest.Export(fresh, nil, extra, nil); err != nil {
		t.Fatalf("造夹具: %v", err)
	}
	_ = os.Remove(ctrl2.Settings().Path)
	if err := os.MkdirAll(filepath.Join(ctrl2.Settings().ConfigDir(), "apps.yaml"), 0o755); err != nil {
		t.Fatalf("占住清单路径: %v", err)
	}
	if _, _, err := ctrl2.ImportManifest(ctx, fresh); err == nil {
		t.Fatal("清单写不进去时应报错")
	}

	// 展开失败：导入进来的软件没有安装路径，清单层会拒绝。
	ctrl3 := newSettingsTestController(t)
	broken2 := apps.Default()
	broken2.Apps = []apps.AppSpec{{ID: "no-path", Name: "缺路径"}}
	fresh2 := filepath.Join(t.TempDir(), "manifest2.json")
	if _, err := manifest.Export(fresh2, nil, broken2, nil); err != nil {
		t.Fatalf("造夹具: %v", err)
	}
	if _, _, err := ctrl3.ImportManifest(ctx, fresh2); err == nil ||
		!strings.Contains(err.Error(), "install.path") {
		t.Fatalf("展开失败应报错: %v", err)
	}

	// 4) 插件目录读不出来时，订阅列表照常返回（只是标注不出本机已装版本）。
	noDir := newPluginControl(t, nil)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	writeText(t, blocker, "x")
	noDir.opts.Settings.Plugins.Dir = blocker
	payload2, err := os.ReadFile(noDir.execPath)
	if err != nil {
		t.Fatalf("读插件: %v", err)
	}
	fs2 := newTLSFeed(t, payload2)
	fs2.setFeed(feedBody("example-static", "2.0.0", noDir.sha, len(payload2), "plugin.exe"))
	noDir.opts.HTTPClient = fs2.srv.Client()
	entries2, err := newControllerWith(t, noDir).FeedEntries(ctx, fs2.feedURL())
	if err != nil {
		t.Fatalf("插件目录读不了不该让订阅列表失败: %v", err)
	}
	if len(entries2) != 1 || entries2[0].Installed != "" {
		t.Fatalf("读不到描述文件时不该强行标注已装版本: %+v", entries2)
	}
}

// 分组与查找的兜底：认不出的前缀、认不出的分组标题、不存在的来源。
func TestLookupFallbacks(t *testing.T) {
	if got := groupOf("nothing.here"); got != "" {
		t.Fatalf("认不出的前缀应归到空分组: %q", got)
	}
	if got := groupOrder("不存在的分组"); got != len(settingGroups) {
		t.Fatalf("认不出的分组应排最后: %d", got)
	}
	if got := groupOf("storage.install_root"); got != "存储与位置" {
		t.Fatalf("已知前缀应归到对应分组: %q", got)
	}

	ctrl := newSettingsTestController(t)
	if _, ok := ctrl.sourceSpec("nope"); ok {
		t.Fatal("不存在的来源不该被找到")
	}
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Fatalf("应取第一个非空值: %q", got)
	}
	if got := firstNonEmpty("", "   "); got != "" {
		t.Fatalf("全空白应返回空串: %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("没有参数时应返回空串: %q", got)
	}
}
