package pluginfeed

import (
	"strings"
	"testing"
)

// mkFeed 造一个最小可用订阅，用于单点校验规则的测试。
func mkFeed(id, version string) *Feed {
	return &Feed{
		Schema: SchemaVersion,
		Plugins: []Plugin{{
			ID:      id,
			Name:    "Demo",
			Version: version,
			Packages: NewPackages(map[string]Package{
				Platform(): {
					URL:    "https://example.com/demo.exe",
					SHA256: strings.Repeat("a", 64),
					Size:   1024,
				},
			}),
		}},
	}
}

func TestValidateAcceptsSafeVersion(t *testing.T) {
	// 空 version 表示未声明，应放行；其余是实际会出现的写法。
	for _, v := range []string{"", "1.2.3", "v1.2.3", "1.0.0-rc1", "1.0.0+build.5", "2026.09.26"} {
		if err := mkFeed("demo", v).Validate("1.0.0", Platform()); err != nil {
			t.Errorf("version %q 应被接受，得到 %v", v, err)
		}
	}
}

// TestValidateRejectsUnsafeVersion 是路径穿越的回归用例。
//
// version 会被拼进缓存文件名（install.go 的 cacheName），
// 放任任意字符等于把 filepath.Join 的越界能力交给订阅方。
func TestValidateRejectsUnsafeVersion(t *testing.T) {
	bad := []string{
		"../../../etc/passwd",
		"..",
		"a/b",
		`a\b`,
		"1.0.0/x",
		".hidden",
		"-1.0.0",
		"_x",
		"a b",
		"a:b",
		"a*b",
		strings.Repeat("v", 100),
	}
	for _, v := range bad {
		err := mkFeed("demo", v).Validate("1.0.0", Platform())
		if err == nil {
			t.Errorf("version %q 应被拒绝", v)
			continue
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("version %q 的报错应点名 version，实际 %v", v, err)
		}
	}
}

// TestCacheNameHasNoSeparators 守住第二道防线：
// 即使以后有人放宽 schema 校验，缓存文件名也不能带出路径分隔符。
func TestCacheNameHasNoSeparators(t *testing.T) {
	e := Entry{Plugin: Plugin{ID: "demo", Version: "../../../evil"}}
	got := cacheName(e)
	for _, bad := range []string{"/", `\`, ".."} {
		if strings.Contains(got, bad) {
			t.Errorf("cacheName(%q) = %q，不应含 %q", e.Plugin.Version, got, bad)
		}
	}
}

// ── 域名声明（schema 2）────────────────────────────────────────

const declaredHostsDoc = `
schema: 2
name: 带声明的源
plugins:
  - id: demo
    version: 1.0.0
    download_hosts:
      - github.com
      - cdn.example.com
    plugin_hosts:
      - api.github.com
    packages:
      windows/amd64:
        url: https://github.com/demo-windows-amd64.exe
        sha256: "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"
      windows/arm64:
        url: https://github.com/demo-windows-arm64.exe
        sha256: "` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `"
`

func TestDeclaredHostsParse(t *testing.T) {
	feed, err := Parse([]byte(declaredHostsDoc), FormatYAML)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := feed.Validate("1.0.0", Platform()); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	p := feed.Plugins[0]
	if len(p.DownloadHosts) != 2 || p.DownloadHosts[0] != "github.com" || p.DownloadHosts[1] != "cdn.example.com" {
		t.Errorf("download_hosts 没解析对: %+v", p.DownloadHosts)
	}
	if len(p.PluginHosts) != 1 || p.PluginHosts[0] != "api.github.com" {
		t.Errorf("plugin_hosts 没解析对: %+v", p.PluginHosts)
	}
}

// 声明是给人看、给机器比的，写错要在发布前暴露 —— 不合法的主机名一律拒绝。
func TestDeclaredHostsValidation(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"大写", "GitHub.com"},
		{"带协议", "https://github.com"},
		{"带路径", "github.com/dl"},
		{"带端口", "github.com:443"},
		{"通配符", "*.github.com"},
		{"空串", ""},
		{"含下划线", "my_host.example.com"},
		{"只有点", "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			feed := mkFeed("demo", "1.0.0")
			feed.Plugins[0].DownloadHosts = []string{c.host}
			if err := feed.Validate("1.0.0", Platform()); err == nil {
				t.Fatalf("非法声明 %q 应当被拒绝", c.host)
			}
		})
	}

	// 重复项同样拒绝：它是「生成器写重了」的信号，不该静默去重。
	feed := mkFeed("demo", "1.0.0")
	feed.Plugins[0].DownloadHosts = []string{"github.com", "github.com"}
	if err := feed.Validate("1.0.0", Platform()); err == nil {
		t.Fatal("重复声明应当被拒绝")
	}
}

// 旧清单（schema 1，没有声明）必须继续能用：声明是可选的。
func TestSchemaOneFeedStillValid(t *testing.T) {
	doc := `
schema: 1
plugins:
  - id: demo
    version: 1.0.0
    packages:
      windows/amd64:
        url: https://example.com/demo-windows-amd64.exe
        sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      windows/arm64:
        url: https://example.com/demo-windows-arm64.exe
        sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
`
	feed, err := Parse([]byte(doc), FormatYAML)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := feed.Validate("1.0.0", Platform()); err != nil {
		t.Fatalf("schema 1 清单应当继续可用: %v", err)
	}
	// 未声明 → 一律放行（老规则：跨域下载逐次确认）。
	if ok, _ := feed.Plugins[0].AllowsDownload("https://anywhere.example/x.exe"); !ok {
		t.Error("未声明域名时不应限制下载")
	}
	if got := feed.DeclaredFingerprint(); got == "" {
		t.Error("指纹不该为空")
	}
}

func TestAllowsDownload(t *testing.T) {
	p := Plugin{DownloadHosts: []string{"github.com", "cdn.example.com"}}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/x.exe", true},
		{"https://GitHub.com/x.exe", true}, // URL 主机大小写不敏感
		{"https://cdn.example.com/a/b.zip", true},
		{"https://evil.example.com/x.exe", false},
		{"https://github.com.evil.com/x.exe", false}, // 后缀冒充不算匹配
		{"https://sub.github.com/x.exe", false},      // 子域不自动放行
		{"", false},                                  // 解析不出主机名
	}
	for _, c := range cases {
		ok, host := p.AllowsDownload(c.url)
		if ok != c.want {
			t.Errorf("AllowsDownload(%q) = %v（host=%q），期望 %v", c.url, ok, host, c.want)
		}
	}
}

func TestDeclaredFingerprint(t *testing.T) {
	base := mkFeed("demo", "1.0.0")
	base.Plugins[0].DownloadHosts = []string{"github.com", "cdn.example.com"}
	base.Plugins[0].PluginHosts = []string{"api.github.com"}

	// 顺序变了，指纹不变：否则生成器换个顺序就让所有人的确认失效。
	reordered := mkFeed("demo", "1.0.0")
	reordered.Plugins[0].DownloadHosts = []string{"cdn.example.com", "github.com"}
	if a, b := base.DeclaredFingerprint(), reordered.DeclaredFingerprint(); a != b {
		t.Errorf("仅顺序不同时指纹应当一致：%s vs %s", a, b)
	}

	// plugin_hosts 只是告知，变它不值得打扰用户。
	infoOnly := mkFeed("demo", "1.0.0")
	infoOnly.Plugins[0].DownloadHosts = []string{"github.com", "cdn.example.com"}
	infoOnly.Plugins[0].PluginHosts = []string{"other.example.com"}
	if a, b := base.DeclaredFingerprint(), infoOnly.DeclaredFingerprint(); a != b {
		t.Errorf("plugin_hosts 变化不应改变指纹：%s vs %s", a, b)
	}

	// 下载域名变了，指纹必须变（否则该重新确认时不会重新确认）。
	changed := mkFeed("demo", "1.0.0")
	changed.Plugins[0].DownloadHosts = []string{"github.com", "cdn.example.com", "new.example.com"}
	if base.DeclaredFingerprint() == changed.DeclaredFingerprint() {
		t.Error("新增下载域名后指纹应当变化")
	}

	if got := (*Feed)(nil).DeclaredFingerprint(); got != "" {
		t.Errorf("nil 订阅的指纹应为空，实际 %q", got)
	}
}
