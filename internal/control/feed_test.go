package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/settings"
)

// newFeedServer 起一个只服务一个插件包的订阅服务器（订阅地址强制 https）。
func newFeedServer(t *testing.T, payload []byte) (string, *http.Client) {
	t.Helper()
	sum := sha256.Sum256(payload)

	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	feedBody := fmt.Sprintf(`
schema: 1
name: 演示源
plugins:
  - id: demo-plugin
    name: 演示插件
    version: 1.2.0
    mode: catalog
    packages:
      %s:
        url: ./demo-plugin.bin
        sha256: %s
        size: %d
`, pluginfeed.Platform(), hex.EncodeToString(sum[:]), len(payload))
	mux.HandleFunc("/feed.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, feedBody)
	})
	mux.HandleFunc("/demo-plugin.bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	return srv.URL + "/feed.yaml", srv.Client()
}

// newFeedTestController 装配一个能装插件的控制层：已授权的订阅 + 空清单。
func newFeedTestController(t *testing.T, dir, feedURL string, client *http.Client, autoTrust bool) (*Controller, *apps.File) {
	t.Helper()

	store, err := pluginfeed.LoadStore(filepath.Join(dir, pluginfeed.FileName))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := store.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	// 添加订阅时授权其域名 —— 安装要走的就是这套授权。
	if _, err := store.AddSubscription(feedURL); err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}

	set := settings.Default()
	set.Plugins.Dir = filepath.Join(dir, "plugin")
	set.Storage.CacheDir = filepath.Join(dir, "cache")
	set.Logs.Dir = filepath.Join(dir, "logs")
	set.Plugins.AutoLoadTrusted = autoTrust

	afs := apps.Default()
	afs.Path = filepath.Join(dir, "apps.yaml")

	ctrl, err := New(Options{Settings: set, Apps: afs, Feed: store, HTTPClient: client})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ctrl, afs
}

// 订阅装完就把校验过的摘要记进信任，插件随即可用。
//
// 这是控制层负责的多步流程：下载校验 → 覆盖前停旧进程 → 覆盖 → 记信任 → 重建来源。
// 以前它散在界面的按键处理里，于是「装完变未信任」这种缺一步的问题真实发生过。
func TestInstallPluginRecordsTrust(t *testing.T) {
	dir := t.TempDir()
	feedURL, client := newFeedServer(t, []byte("fake-plugin-binary"))
	ctrl, afs := newFeedTestController(t, dir, feedURL, client, true)

	entries, err := ctrl.FeedEntries(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 %d，期望 1", len(entries))
	}

	res, err := ctrl.InstallPlugin(context.Background(), feedURL, entries[0], nil)
	if err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	if res.ID != "demo-plugin" || res.SHA256 == "" {
		t.Fatalf("安装结果不完整: %+v", res)
	}

	reloaded, err := apps.Load(afs.Path)
	if err != nil {
		t.Fatalf("重新加载清单: %v", err)
	}
	if len(reloaded.Sources) != 1 || reloaded.Sources[0].ID != "demo-plugin" {
		t.Fatalf("安装后应写入信任条目，实际 %+v", reloaded.Sources)
	}
	if reloaded.Sources[0].Trust != res.SHA256 {
		t.Fatalf("信任哈希不对: %+v", reloaded.Sources[0])
	}
}

// 关掉自动加载则留回逐个确认：装完保持未信任，由用户在来源面板按 t 决定。
func TestInstallPluginKeepsUntrustedWhenAutoLoadDisabled(t *testing.T) {
	dir := t.TempDir()
	feedURL, client := newFeedServer(t, []byte("fake-plugin-binary"))
	ctrl, afs := newFeedTestController(t, dir, feedURL, client, false)

	entries, err := ctrl.FeedEntries(context.Background(), feedURL)
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	if _, err := ctrl.InstallPlugin(context.Background(), feedURL, entries[0], nil); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}

	reloaded, err := apps.Load(afs.Path)
	if err != nil {
		t.Fatalf("重新加载清单: %v", err)
	}
	if len(reloaded.Sources) != 0 {
		t.Fatalf("关掉自动加载时不应写入信任，实际 %+v", reloaded.Sources)
	}
}

// 来源汇总：清单里声明的在前，仅被宿主发现的后，且带得上信任所需的哈希与路径。
func TestSourcesMergesManifestAndDiscovered(t *testing.T) {
	dir := t.TempDir()
	afs := apps.Default()
	afs.Path = filepath.Join(dir, "apps.yaml")
	afs.Sources = []apps.SourceSpec{{ID: "declared-src", Kind: apps.KindPlugin}}

	set := settings.Default()
	set.Plugins.Dir = filepath.Join(dir, "plugin")
	set.Storage.CacheDir = filepath.Join(dir, "cache")
	set.Logs.Dir = filepath.Join(dir, "logs")

	ctrl, err := New(Options{Settings: set, Apps: afs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	infos := ctrl.Sources()
	if len(infos) != 1 || infos[0].ID != "declared-src" || !infos[0].Declared {
		t.Fatalf("清单里的来源应当列出且标记为已声明，实际 %+v", infos)
	}
	if !infos[0].Enabled {
		t.Error("清单里没写 enabled 时应当视为启用")
	}
}
