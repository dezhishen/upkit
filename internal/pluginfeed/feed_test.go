package pluginfeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const demoSum = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// 同一份订阅的两种写法：JSON 与 YAML 必须解析成同样的结构。
const demoFeedYAML = `
schema: 1
name: 演示源
plugins:
  - id: demo-plugin
    name: 演示插件
    description: 用来说明订阅格式
    version: 1.2.0
    mode: catalog
    min_host_version: "1.0.0"
    packages:
      PLATFORM:
        url: ./dist/demo-plugin.bin
        sha256: ` + demoSum + `
        size: 1024
`

const demoFeedJSON = `{
  "schema": 1,
  "name": "演示源",
  "plugins": [
    {
      "id": "demo-plugin",
      "name": "演示插件",
      "description": "用来说明订阅格式",
      "version": "1.2.0",
      "mode": "catalog",
      "min_host_version": "1.0.0",
      "packages": {
        "PLATFORM": { "url": "./dist/demo-plugin.bin", "sha256": "` + demoSum + `", "size": 1024 }
      }
    }
  ]
}`

func feedText(tmpl string) []byte {
	return []byte(strings.ReplaceAll(tmpl, "PLATFORM", Platform()))
}

func parseDemo(t *testing.T, data []byte, format Format) *Feed {
	t.Helper()
	f, err := Parse(data, format)
	if err != nil {
		t.Fatalf("Parse(%s): %v", format, err)
	}
	return f
}

// JSON 与 YAML 走同一个解析器，解析结果必须完全一致。
func TestParseAcceptsBothJSONAndYAML(t *testing.T) {
	yamlFeed := parseDemo(t, feedText(demoFeedYAML), FormatYAML)
	jsonFeed := parseDemo(t, feedText(demoFeedJSON), FormatJSON)

	if yamlFeed.Schema != jsonFeed.Schema || yamlFeed.Name != jsonFeed.Name {
		t.Fatalf("两种格式的顶层字段不一致: %+v vs %+v", yamlFeed, jsonFeed)
	}
	if len(yamlFeed.Plugins) != 1 || len(jsonFeed.Plugins) != 1 {
		t.Fatalf("插件数量不一致")
	}

	a, b := yamlFeed.Plugins[0], jsonFeed.Plugins[0]
	if a.ID != b.ID || a.Version != b.Version || a.Mode != b.Mode || a.MinHostVersion != b.MinHostVersion {
		t.Fatalf("插件字段不一致:\n%+v\n%+v", a, b)
	}
	pa, _ := a.Packages.For(Platform())
	pb, _ := b.Packages.For(Platform())
	if pa.URL != pb.URL || pa.SHA256 != pb.SHA256 || pa.Size != pb.Size {
		t.Fatalf("包字段不一致: %+v vs %+v", pa, pb)
	}
	if err := yamlFeed.Validate("1.0.0", ""); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestFormatOfExtension(t *testing.T) {
	cases := map[string]Format{
		"https://x/plugins.json":     FormatJSON,
		"https://x/plugins.yaml":     FormatYAML,
		"https://x/plugins.yml":      FormatYAML,
		"https://x/plugins.JSON?v=2": FormatJSON,
		"https://x/feed":             FormatAuto,
		"https://x/feed.txt":         FormatAuto,
	}
	for url, want := range cases {
		if got := FormatOf(url); got != want {
			t.Errorf("FormatOf(%q) = %q，期望 %q", url, got, want)
		}
	}
}

// 字段名写错必须报错，而不是静默忽略 —— 静默忽略会让人以为限制生效了。
func TestParseRejectsUnknownFields(t *testing.T) {
	bad := `
schema: 1
plugins:
  - id: demo
    version: 1.0.0
    packages:
      PLATFORM:
        url: ./x.bin
        sha256: ` + demoSum + `
    typo_field: 1
`
	if _, err := Parse(feedText(bad), FormatYAML); err == nil {
		t.Fatal("未知字段应当报错")
	}
}

func TestResolveLocation(t *testing.T) {
	feed := "https://example.com/upkit/plugins.json"

	cases := []struct {
		name          string
		raw           string
		wantURL       string
		wantNeedsAuth bool
		wantErr       bool
	}{
		{"相对路径", "./dist/x.exe", "https://example.com/upkit/dist/x.exe", false, false},
		{"裸相对路径", "dist/x.exe", "https://example.com/upkit/dist/x.exe", false, false},
		{"站点内绝对路径", "/dist/x.exe", "https://example.com/dist/x.exe", false, false},
		{"同源上级目录", "../x.exe", "https://example.com/x.exe", false, false},
		{"同源绝对地址", "https://example.com/a/x.exe", "https://example.com/a/x.exe", false, false},
		{"跨域绝对地址", "https://cdn.example.org/x.exe", "https://cdn.example.org/x.exe", true, false},
		{"协议相对地址跨域", "//evil.example/x.exe", "", false, true},
		{"不支持的协议", "ftp://example.com/x.exe", "", false, true},
		{"空地址", "  ", "", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := ResolveLocation(feed, tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %+v", loc)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveLocation: %v", err)
			}
			if loc.URL != tc.wantURL {
				t.Fatalf("URL = %q，期望 %q", loc.URL, tc.wantURL)
			}
			if loc.NeedsAuthorization() != tc.wantNeedsAuth {
				t.Fatalf("NeedsAuthorization = %v，期望 %v", loc.NeedsAuthorization(), tc.wantNeedsAuth)
			}
		})
	}
}

func TestValidateRejectsBadFeeds(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"schema 太新", "schema: 99\nplugins:\n  - id: a\n    packages:\n      PLATFORM:\n        url: ./x\n        sha256: " + demoSum, "只支持"},
		{"没有插件", "schema: 1\nplugins: []", "没有任何插件"},
		{"id 非法", "schema: 1\nplugins:\n  - id: Bad/Path\n    packages:\n      PLATFORM:\n        url: ./x\n        sha256: " + demoSum, "不合法"},
		{"缺少 sha256", "schema: 1\nplugins:\n  - id: a\n    packages:\n      PLATFORM:\n        url: ./x", "sha256"},
		{"sha256 非法", "schema: 1\nplugins:\n  - id: a\n    packages:\n      PLATFORM:\n        url: ./x\n        sha256: zz", "sha256"},
		{"缺少当前平台的包", "schema: 1\nplugins:\n  - id: a\n    packages:\n      plan9/mips:\n        url: ./x\n        sha256: " + demoSum, "没有"},
		{"重复 id", "schema: 1\nplugins:\n  - id: a\n    packages:\n      PLATFORM:\n        url: ./x\n        sha256: " + demoSum + "\n  - id: a\n    packages:\n      PLATFORM:\n        url: ./y\n        sha256: " + demoSum, "重复"},
		{"宿主版本太低", "schema: 1\nplugins:\n  - id: a\n    min_host_version: \"9.0.0\"\n    packages:\n      PLATFORM:\n        url: ./x\n        sha256: " + demoSum, "要求宿主版本"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := parseDemo(t, feedText(tc.yaml), FormatYAML)
			err := f.Validate("1.0.0", Platform())
			if err == nil {
				t.Fatal("期望校验失败")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0", "1.9.9", 1},
		{"1.0", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1}, // 正式版 > 预发布
		{"1.2.10", "1.2.9", 1},    // 数值比较而非字典序
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q,%q) = %d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
	if !IsDowngrade("1.2.0", "1.1.0") || IsDowngrade("1.2.0", "1.3.0") || IsDowngrade("", "1.0") {
		t.Error("IsDowngrade 判断不正确")
	}
}

func TestEntryAction(t *testing.T) {
	cases := []struct {
		installed string
		offered   string
		want      Action
	}{
		{"", "1.0.0", ActionInstall},
		{"1.0.0", "1.1.0", ActionUpdate},
		{"1.0.0", "1.0.0", ActionCurrent},
		{"1.1.0", "1.0.0", ActionDowngrade},
	}
	for _, tc := range cases {
		e := Entry{Plugin: Plugin{Version: tc.offered}, Installed: tc.installed}
		if got := e.Action(); got != tc.want {
			t.Errorf("已装 %q / 提供 %q → %q，期望 %q", tc.installed, tc.offered, got, tc.want)
		}
	}
}

func TestStoreAuthorizationFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	store, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	// 默认：订阅功能关闭，添订阅必须被拒绝（不能只靠界面不点按钮）。
	if store.FeatureAuthorized() {
		t.Fatal("订阅功能默认必须是关闭的")
	}
	const feedURL = "https://example.com/plugins.json"
	if _, err := store.AddSubscription(feedURL); err == nil {
		t.Fatal("未授权功能时不应允许添加订阅")
	}

	// 用户确认启用后，添订阅会自动授权其域名。
	if err := store.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	sub, err := store.AddSubscription(feedURL)
	if err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}
	if sub.Host != "example.com" {
		t.Fatalf("订阅域名记录错误: %q", sub.Host)
	}
	if _, err := store.AddSubscription(feedURL); err == nil {
		t.Fatal("重复添加应当报错")
	}

	// 跨域下载器域名需要单独授权，且按域名记住。
	if store.HostAuthorized("cdn.example.org") {
		t.Fatal("未经确认的域名不应处于已授权状态")
	}
	if err := store.AuthorizeHost("cdn.example.org", feedURL); err != nil {
		t.Fatalf("AuthorizeHost: %v", err)
	}
	if !store.HostAuthorized("CDN.Example.org") {
		t.Fatal("域名授权应当忽略大小写")
	}
	if err := store.RevokeHost("cdn.example.org"); err != nil {
		t.Fatalf("RevokeHost: %v", err)
	}
	if store.HostAuthorized("cdn.example.org") {
		t.Fatal("撑销后不应仍为已授权")
	}

	// 落盘后重新加载，授权状态必须保留。
	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if !reloaded.FeatureAuthorized() || len(reloaded.Subscriptions()) != 1 {
		t.Fatalf("授权记录未持久化: %+v", reloaded.Authorization())
	}
	if err := reloaded.RemoveSubscription(feedURL); err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}
	if len(reloaded.Subscriptions()) != 0 {
		t.Fatal("订阅未被移除")
	}
}

// 端到端：真的用 HTTP 提供一个订阅与插件包，跑完整链路。
func TestInstallFromHTTPEndToEnd(t *testing.T) {
	payload := []byte("fake-plugin-binary")
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

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
`, Platform(), sumHex, len(payload))

	mux.HandleFunc("/feed.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = io.WriteString(w, feedBody)
	})
	mux.HandleFunc("/demo-plugin.bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})

	feedURL := srv.URL + "/feed.yaml"
	feed, err := Fetch(context.Background(), srv.Client(), feedURL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := feed.Validate("2.0.0", Platform()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	entries, err := Plan(feed, feedURL)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 %d，期望 1", len(entries))
	}
	if entries[0].Location.NeedsAuthorization() {
		t.Fatal("相对路径不应需要额外授权")
	}
	if entries[0].Action() != ActionInstall {
		t.Fatalf("动作应为 install，实际 %q", entries[0].Action())
	}

	base := t.TempDir()
	req := InstallRequest{
		FeedURL:   feedURL,
		Entry:     entries[0],
		PluginDir: filepath.Join(base, "plugin"),
		CacheDir:  filepath.Join(base, "cache"),
	}
	got, err := Install(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Cached {
		t.Fatal("首次安装不应命中缓存")
	}

	// 插件本体与 sidecar 都要落盘。
	data, err := os.ReadFile(got.Path)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("插件内容不对: %v", err)
	}
	sidecarBody, err := os.ReadFile(filepath.Join(base, "plugin", "demo-plugin.plugin.yaml"))
	if err != nil {
		t.Fatalf("缺少 sidecar: %v", err)
	}
	for _, want := range []string{"version: 1.2.0", "subscription: " + feedURL, sumHex, "mode: catalog"} {
		if !strings.Contains(string(sidecarBody), want) {
			t.Errorf("sidecar 缺少 %q:\n%s", want, sidecarBody)
		}
	}

	// 第二次安装复用缓存，不再下载。
	again, err := Install(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatalf("再次 Install: %v", err)
	}
	if !again.Cached {
		t.Error("第二次应当命中缓存")
	}

	// 摘要不匹配必须拒绝。
	bad := req
	bad.Entry.Package.SHA256 = strings.Repeat("a", 64)
	if _, err := Install(context.Background(), srv.Client(), bad); err == nil {
		t.Error("摘要不匹配应当拒绝安装")
	}

	// 降级默认拒绝。
	down := req
	down.Entry.Installed = "9.9.9"
	if _, err := Install(context.Background(), srv.Client(), down); err == nil {
		t.Error("降级应当默认被拒绝")
	}
	down.AllowDowngrade = true
	if _, err := Install(context.Background(), srv.Client(), down); err != nil {
		t.Errorf("显式允许降级后应当可以安装: %v", err)
	}
}

// 包地址跨域时必须单独授权；未授权一律拒绝下载。
func TestInstallRequiresAuthorizationForCrossOrigin(t *testing.T) {
	payload := []byte("cross-origin-payload")
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	// 包放在另一个 server 上，与订阅不同源。
	pkgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer pkgSrv.Close()

	mux := http.NewServeMux()
	feedSrv := httptest.NewServer(mux)
	defer feedSrv.Close()

	feedBody := fmt.Sprintf(`
schema: 1
plugins:
  - id: cross-plugin
    version: 1.0.0
    packages:
      %s:
        url: %s/pkg.bin
        sha256: %s
`, Platform(), pkgSrv.URL, sumHex)
	mux.HandleFunc("/feed.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, feedBody)
	})

	feedURL := feedSrv.URL + "/feed.yaml"
	feed, err := Fetch(context.Background(), feedSrv.Client(), feedURL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	entries, err := Plan(feed, feedURL)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !entries[0].Location.NeedsAuthorization() {
		t.Fatal("跨域地址应当需要额外授权")
	}

	base := t.TempDir()
	req := InstallRequest{
		FeedURL:   feedURL,
		Entry:     entries[0],
		PluginDir: filepath.Join(base, "plugin"),
		CacheDir:  filepath.Join(base, "cache"),
	}

	// 没接授权回调：直接拒绝。
	if _, err := Install(context.Background(), feedSrv.Client(), req); err == nil {
		t.Fatal("跨域未授权应当拒绝")
	} else if !strings.Contains(err.Error(), "未授权") {
		t.Fatalf("错误信息应说明需要授权: %v", err)
	}

	// 用户明确拒绝：同样不下载。
	req.Authorize = func(string) (bool, error) { return false, nil }
	if _, err := Install(context.Background(), feedSrv.Client(), req); err == nil {
		t.Fatal("用户拒绝后不应安装")
	}

	// 用户同意：安装成功，且回调拿到的正是包所在域名。
	var askedHost string
	req.Authorize = func(host string) (bool, error) {
		askedHost = host
		return true, nil
	}
	if _, err := Install(context.Background(), feedSrv.Client(), req); err != nil {
		t.Fatalf("授权后应当安装成功: %v", err)
	}
	wantHost := strings.TrimPrefix(pkgSrv.URL, "http://")
	if askedHost != wantHost {
		t.Fatalf("授权回调拿到的域名是 %q，期望 %q", askedHost, wantHost)
	}
}
