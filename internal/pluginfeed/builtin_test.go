package pluginfeed

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 内置订阅默认指向官方清单仓库。
func TestBuiltinFeedDefaults(t *testing.T) {
	if BuiltinFeedURL != DefaultBuiltinFeedURL {
		t.Fatalf("未覆盖时 BuiltinFeedURL 应等于默认值，实际 %q", BuiltinFeedURL)
	}
	if !strings.Contains(DefaultBuiltinFeedURL, "upkit-hub") {
		t.Fatalf("默认地址应指向官方清单仓库: %q", DefaultBuiltinFeedURL)
	}
	if !IsBuiltinFeed(BuiltinFeedURL) || !IsBuiltinFeed("  "+DefaultBuiltinFeedURL+"  ") {
		t.Fatal("IsBuiltinFeed 应当识别默认地址（含首尾空白）")
	}
	if IsBuiltinFeed("https://example.com/feed.yaml") {
		t.Fatal("别的地址不应被当成本订阅")
	}
}

// 构建时用 -ldflags -X 覆盖内置地址必须真的生效。
//
// 值得为此跑一次真实构建：-X 打错变量路径（比如包名写错、字段名大小写不对）时
// **不会报任何错**，只是静默保留默认值。那样用户以为改掉了，实际没有。
func TestBuiltinFeedURLCanBeOverriddenAtBuildTime(t *testing.T) {
	if testing.Short() {
		t.Skip("需要调用 go build，-short 模式下跳过")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("环境里没有 go，跳过")
	}

	const want = "https://mirror.internal.example.com/upkit/feed.yaml"
	dir := t.TempDir()
	out := filepath.Join(dir, "probe")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}

	cmd := exec.Command("go", "build",
		"-ldflags", "-X "+builtinFeedURLSymbol+"="+want,
		"-o", out, "./testdata/probe")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建探针失败: %v\n%s", err, b)
	}

	got, err := exec.Command(out).Output()
	if err != nil {
		t.Fatalf("运行探针失败: %v", err)
	}
	if string(got) != want {
		t.Fatalf("-X 覆盖没有生效（这可是静默失败，最危险的那种）\n实际: %q\n期望: %q",
			string(got), want)
	}
}

// 构建脚本必须提供同一个覆盖入口，否则「可以在构建时覆盖」只对手动 go build 成立。
func TestBuildScriptExposesFeedURLOverride(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build.sh"))
	if err != nil {
		t.Skipf("读不到 build.sh: %v", err)
	}
	if !strings.Contains(string(raw), builtinFeedURLSymbol) {
		t.Fatalf("build.sh 里没有引用 %s，--feed-url 不会真的生效", builtinFeedURLSymbol)
	}
	if !strings.Contains(string(raw), "--feed-url") {
		t.Fatal("build.sh 里找不到 --feed-url 选项")
	}
}
