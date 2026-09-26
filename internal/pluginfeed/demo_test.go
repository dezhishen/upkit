package pluginfeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件把 testdata/demo-feed.yaml 当成「活的示例」来用：它既是可以照抄的模板，
// 也是被测试真实加载、校验、安装的输入。这样示例一旦写错，测试立刻变红 ——
// 而放在仓库里当文档的静态文件（曾经的 plugins/feed.yaml）做不到这点。

const demoFeedPath = "testdata/demo-feed.yaml"

// demoPayloadFor 返回测试里充当插件包的内容。
//
// 内容与夹具里的 sha256 一一对应，改内容就必须同步改夹具 —— 也正因为如此，
// 下面第 2 个测试会直接校验「夹具里的摘要 == 真实摘要」。
func demoPayloadFor(string) []byte {
	return []byte("upkit demo plugin (windows)\n")
}

func readDemoFeed(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(demoFeedPath)
	if err != nil {
		t.Fatalf("读取夹具失败: %v", err)
	}
	return raw
}

// 夹具必须在所有受支持的平台上都能通过校验 —— 否则它就只是个好看的文档。
func TestDemoFeedFixtureValidatesOnEveryPlatform(t *testing.T) {
	raw := readDemoFeed(t)
	for _, platform := range SupportedPlatforms() {
		feed, err := Parse(raw, FormatYAML)
		if err != nil {
			t.Fatalf("解析夹具失败（%s）: %v", platform, err)
		}
		if err := feed.Validate("1.0.0", platform); err != nil {
			t.Fatalf("%s 上校验失败: %v", platform, err)
		}
		if len(feed.Plugins) != 2 {
			t.Fatalf("夹具应有两个插件，实际 %d", len(feed.Plugins))
		}
		for _, p := range feed.Plugins {
			if _, ok := p.Packages.For(platform); !ok {
				t.Fatalf("插件 %s 缺少 %s 的包", p.ID, platform)
			}
		}
	}
}

// 夹具里的摘要必须与真实内容一致，否则照着它抄的人会撞上一堵墙。
func TestDemoFeedFixtureDigestsAreReal(t *testing.T) {
	feed, err := Parse(readDemoFeed(t), FormatYAML)
	if err != nil {
		t.Fatalf("解析夹具失败: %v", err)
	}
	for _, p := range feed.Plugins {
		for _, platform := range p.Packages.Platforms() {
			pkg, _ := p.Packages.For(platform)
			want := sha256.Sum256(demoPayloadFor(pkg.URL))
			if got := NormalizeSHA256(pkg.SHA256); got != hex.EncodeToString(want[:]) {
				t.Errorf("%s/%s 的摘要与内容不符：夹具 %s，实际 %s",
					p.ID, platform, got, hex.EncodeToString(want[:]))
			}
			if pkg.Size != int64(len(demoPayloadFor(pkg.URL))) {
				t.Errorf("%s/%s 的 size 与内容不符：夹具 %d，实际 %d",
					p.ID, platform, pkg.Size, len(demoPayloadFor(pkg.URL)))
			}
		}
	}
}

// 完整链路：相对路径解析 → 平台选择 → 下载 → 摘要校验 → 落盘 + sidecar。
func TestDemoFeedInstallEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	mux.HandleFunc("/demo-feed.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(readDemoFeed(t))
	})
	// 产物按文件名分发，夹具里增删平台不需要改这里。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(demoPayloadFor(r.URL.Path))
	})

	feedURL := srv.URL + "/demo-feed.yaml"
	feed, err := Fetch(context.Background(), srv.Client(), feedURL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := feed.Validate("1.0.0", Platform()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	entries, err := Plan(feed, feedURL)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("条目数 %d，期望 2", len(entries))
	}

	// 相对路径：必须解析到订阅所在的域，且不需要额外授权。
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.Plugin.ID] = e
		if e.Location.NeedsAuthorization() {
			t.Errorf("%s 用的是相对路径，不应需要额外授权", e.Plugin.ID)
		}
		if !strings.HasPrefix(e.Location.URL, srv.URL+"/") {
			t.Errorf("%s 的相对路径没有解析到订阅所在域: %s", e.Plugin.ID, e.Location.URL)
		}
		if e.Action() != ActionInstall {
			t.Errorf("%s 的动作应为 install，实际 %q", e.Plugin.ID, e.Action())
		}
	}

	entry, ok := byID["demo-plugin"]
	if !ok {
		t.Fatalf("缺少 demo-plugin: %+v", entries)
	}
	// 平台选择必须落到当前架构对应的产物上（upkit 只有 Windows 版本）。
	wantName := "demo-plugin-windows-" + runtime.GOARCH + ".exe"
	if filepath.Base(entry.Location.URL) != wantName {
		t.Fatalf("%s 上选中的包是 %s，期望 %s", Platform(), filepath.Base(entry.Location.URL), wantName)
	}

	base := t.TempDir()
	got, err := Install(context.Background(), srv.Client(), InstallRequest{
		FeedURL:   feedURL,
		Entry:     entry,
		PluginDir: filepath.Join(base, "plugin"),
		CacheDir:  filepath.Join(base, "cache"),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// 插件本体落盘，内容与夹具摘要一致。
	data, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("读插件失败: %v", err)
	}
	if string(data) != string(demoPayloadFor(wantName)) {
		t.Fatalf("插件内容不对: %q", data)
	}

	// sidecar 让「这个插件是哪来的、装的哪一版」可以从文件本身还原。
	sidecar, err := os.ReadFile(filepath.Join(base, "plugin", "demo-plugin.plugin.yaml"))
	if err != nil {
		t.Fatalf("缺少 sidecar: %v", err)
	}
	for _, want := range []string{"version: 1.2.0", "subscription: " + feedURL, NormalizeSHA256(entry.Package.SHA256)} {
		if !strings.Contains(string(sidecar), want) {
			t.Errorf("sidecar 缺少 %q:\n%s", want, sidecar)
		}
	}

	// 装完之后再拉一次：动作判定依赖宿主注入的本机版本，这里显式走一遍。
	again, err := Fetch(context.Background(), srv.Client(), feedURL)
	if err != nil {
		t.Fatalf("再次 Fetch: %v", err)
	}
	entries2, err := Plan(again, feedURL)
	if err != nil {
		t.Fatalf("再次 Plan: %v", err)
	}
	var target Entry
	for _, e := range entries2 {
		if e.Plugin.ID == "demo-plugin" {
			target = e
		}
	}

	// 同版本同摘要：认定为已是最新，不重复下载。
	target.Installed = "1.2.0"
	target.InstalledSHA256 = NormalizeSHA256(target.Package.SHA256)
	if got := target.Action(); got != ActionCurrent {
		t.Fatalf("同版本同摘要应为 current，实际 %q", got)
	}

	// 本机比订阅还新：判为降级，默认拒绝覆盖。
	target.Installed = "9.9.9"
	if got := target.Action(); got != ActionDowngrade {
		t.Fatalf("本机版本更新时应判为降级，实际 %q", got)
	}
}
