package pluginfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"
)

// 发布流水线用 scripts/gen-feed.sh 生成内置订阅清单（它是 release 附件）。
//
// 这个测试锁定一件事：脚本生成的东西必须真的能被宿主读进来、并且通过校验 ——
// 否则「一键添加官方源」会在用户那里失败，而我们在 CI 上毫无察觉。
func TestGenFeedScriptProducesValidFeed(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("环境里没有 bash，跳过发布脚本测试")
	}
	script := filepath.Join("..", "..", "scripts", "gen-feed.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("找不到 %s: %v", script, err)
	}

	// ── 造一批「构建产物」 ──
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	payload := []byte("fake plugin binary")
	for _, name := range []string{
		"upkit-hub-windows-amd64.exe",
		"upkit-hub-windows-arm64.exe",
		"corp-agent-windows-amd64.exe",
		"corp-agent-windows-arm64.exe",
	} {
		if err := os.WriteFile(filepath.Join(pluginsDir, name), payload, 0o644); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
	}
	// 非产物文件应当被跳过，而不是让脚本失败。
	if err := os.WriteFile(filepath.Join(pluginsDir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写 README: %v", err)
	}

	// ── 跑脚本 ──
	out := filepath.Join(dir, "feed.yaml")
	baseURL := "https://github.com/dezhishen/upkit/releases/download/v1.2.3"
	cmd := exec.Command(bash, script,
		"--plugins-dir", pluginsDir,
		"--version", "1.2.3",
		"--base-url", baseURL,
		"-o", out,
		"--name", "upkit-hub=示例插件集",
		"--min-host-version", "1.0.0",
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gen-feed.sh 失败: %v\n%s", err, b)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读生成结果: %v", err)
	}

	// ── 生成物必须能被宿主解析并通过校验 ──
	feed, err := Parse(raw, FormatYAML)
	if err != nil {
		t.Fatalf("生成的清单无法解析: %v\n%s", err, raw)
	}
	for _, platform := range SupportedPlatforms() {
		if err := feed.Validate("1.2.3", platform); err != nil {
			t.Fatalf("%s 上校验失败: %v", platform, err)
		}
	}
	if len(feed.Plugins) != 2 {
		t.Fatalf("插件数 %d，期望 2：%+v", len(feed.Plugins), feed.Plugins)
	}

	// ── 摘要、地址、大小必须与产物严格对应 ──
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	var hub *Plugin
	for i := range feed.Plugins {
		if feed.Plugins[i].ID == "upkit-hub" {
			hub = &feed.Plugins[i]
		}
	}
	if hub == nil {
		t.Fatalf("缺少 upkit-hub: %+v", feed.Plugins)
	}
	if hub.Name != "示例插件集" {
		t.Errorf("展示名覆盖没生效: %q", hub.Name)
	}
	if hub.Version != "1.2.3" || hub.Mode != upkitplugin.ModeCatalog {
		t.Errorf("版本/模式不对: version=%q mode=%q", hub.Version, hub.Mode)
	}
	if hub.MinHostVersion != "1.0.0" {
		t.Errorf("min_host_version 丢失: %q", hub.MinHostVersion)
	}
	for _, platform := range SupportedPlatforms() {
		pkg, ok := hub.Packages.For(platform)
		if !ok {
			t.Fatalf("缺少 %s 的包", platform)
		}
		if got := NormalizeSHA256(pkg.SHA256); got != want {
			t.Errorf("%s 的摘要与产物不符: %q", platform, got)
		}
		if pkg.Size != int64(len(payload)) {
			t.Errorf("%s 的 size 不对: %d", platform, pkg.Size)
		}
		if !strings.HasPrefix(pkg.URL, baseURL+"/") {
			t.Errorf("%s 的地址没有拼上 base-url: %s", platform, pkg.URL)
		}
		if !strings.HasSuffix(pkg.URL, "-windows-"+strings.TrimPrefix(platform, "windows/")+".exe") {
			t.Errorf("%s 的地址指向了别的产物: %s", platform, pkg.URL)
		}
	}
}

// 漏发一个架构的产物，对应架构的用户会在「校验订阅」时失败 —— 发布前就该拦住。
func TestGenFeedScriptRejectsIncompletePlatformCoverage(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("环境里没有 bash，跳过发布脚本测试")
	}
	script := filepath.Join("..", "..", "scripts", "gen-feed.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("找不到 %s: %v", script, err)
	}

	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	// 只造 amd64：arm64 的用户将无法使用这份订阅。
	if err := os.WriteFile(filepath.Join(pluginsDir, "upkit-hub-windows-amd64.exe"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写产物: %v", err)
	}

	args := []string{script,
		"--plugins-dir", pluginsDir,
		"--version", "1.2.3",
		"--base-url", "https://example.com/dl",
		"-o", filepath.Join(dir, "feed.yaml"),
	}
	if out, err := exec.Command(bash, args...).CombinedOutput(); err == nil {
		t.Fatalf("缺架构时应当失败，实际成功:\n%s", out)
	} else if !strings.Contains(string(out), "windows/arm64") {
		t.Errorf("错误信息里应指出缺哪个平台:\n%s", out)
	}

	// 显式放行时应当成功。
	if out, err := exec.Command(bash, append(args, "--allow-partial")...).CombinedOutput(); err != nil {
		t.Fatalf("--allow-partial 时不应失败: %v\n%s", err, out)
	}
}

// 脚本里写死的平台列表必须与代码里的 SupportedPlatforms() 一致，否则会出现
// 「代码认为支持 arm64、流水线却没检查它」这种静默偏差。
func TestGenFeedScriptPlatformListMatchesCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "gen-feed.sh"))
	if err != nil {
		t.Skipf("读不到 gen-feed.sh: %v", err)
	}
	re := regexp.MustCompile(`(?m)^SUPPORTED_PLATFORMS="([^"]*)"`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatal("gen-feed.sh 里找不到 SUPPORTED_PLATFORMS")
	}
	if got, want := strings.Fields(string(m[1])), SupportedPlatforms(); !slices.Equal(got, want) {
		t.Fatalf("脚本里的平台列表 %v 与代码里的 %v 不一致", got, want)
	}
}
