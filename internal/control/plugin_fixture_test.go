package control

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/settings"
)

// 这个文件里的夹具会真的编译并启动示例插件。
//
// 为什么非得跑真插件：控制层的插件相关方法（来源汇总、信任、启停、插件配置、
// 订阅安装）在「宿主没起来」时的行为早就被测过了（全是「未启用」的错误分支），
// 真正没被测过的是「宿主起来了、插件在跑」的那一半 —— 而那才是用户每天用的路径。
// 拿桩替掉宿主是做不到的：控制器持有的是具体的 *pluginhost.Manager，不是接口
// （这个选择是有意的，见 pluginhost 的说明），所以测试就从插件进程这一层真接上。

// pluginFixture 是一次「带真插件」的装配结果。
type pluginFixture struct {
	ctrl      *Controller
	root      string
	pluginDir string
	execPath  string
	sha       string
	host      *pluginhost.Manager
	store     *pluginfeed.Store
	afs       *apps.File
	set       *settings.Settings
	// opts 是交给控制层的装配参数；tweak 可以在这里补 HTTPClient 之类。
	opts Options
}

// buildExamplePlugin 编译示例插件到 dir，返回可执行文件路径与它的 sha256。
//
// 编译不出来（离线、没有 go 工具链）就跳过：那属于环境问题，不是功能问题。
func buildExamplePlugin(t *testing.T, dir string) (string, string) {
	t.Helper()
	return buildExamplePluginVersion(t, dir, "dev")
}

// buildExamplePluginVersion 同上，但把插件自报的版本编进去。
//
// 订阅里的「升级」要能被验证，两个版本的二进制必须是不同的内容（摘要不同），
// 否则「信任摘要被更新」这件事根本看不出来。
func buildExamplePluginVersion(t *testing.T, dir, version string) (string, string) {
	t.Helper()
	out := filepath.Join(dir, "example-static"+".exe")
	cmd := exec.Command("go", "build", "-o", out,
		"-ldflags", "-X main.version="+version,
		"github.com/dezhishen/upkit/cmd/upkit-plugin-example")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("无法编译示例插件: %v\n%s", err, b)
	}
	sha, err := pluginhost.HashFile(out)
	if err != nil {
		t.Fatalf("计算插件摘要: %v", err)
	}
	return out, sha
}

// newPluginControl 装配一个「插件已信任并已加载」的控制层。
//
// 默认情形刻意做成最完整的那一种：清单里声明并信任示例插件、插件目录里有它的
// 描述文件、软件清单里有一条属于它的软件、订阅仓库已就绪。
func newPluginControl(t *testing.T, tweak func(*pluginFixture)) *pluginFixture {
	t.Helper()
	root := t.TempDir()
	set, err := settings.Load(filepath.Join(root, settings.DirConfig, settings.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := set.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	pluginDir := filepath.Join(root, settings.DirPlugin)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("建插件目录: %v", err)
	}
	execPath, sha := buildExamplePlugin(t, pluginDir)
	// 描述文件：订阅装出来的插件与手工放的插件同构，这里按「装过 1.2.0」写。
	if _, err := pluginhost.SaveManifest(pluginDir, pluginhost.Manifest{
		ID:           "example-static",
		Name:         "示例静态源",
		Description:  "从静态列表提供软件与版本",
		Exec:         filepath.Base(execPath),
		Mode:         "catalog",
		Version:      "1.2.0",
		SHA256:       sha,
		Subscription: "https://example.com/feed.yaml",
	}); err != nil {
		t.Fatalf("写插件描述: %v", err)
	}

	afs := apps.Default()
	afs.Path = filepath.Join(root, settings.DirConfig, settings.AppsFileName)
	afs.Sources = []apps.SourceSpec{{
		ID:    "example-static",
		Name:  "示例静态源",
		Kind:  apps.KindPlugin,
		Trust: sha,
	}}
	afs.Apps = []apps.AppSpec{pluginSourceAppSpec("corp-vpn", "example-static", filepath.Join(root, "apps", "CorpVPN"))}

	store, err := pluginfeed.LoadStore(filepath.Join(root, settings.DirConfig, "feed.yaml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	f := &pluginFixture{
		root: root, pluginDir: pluginDir, execPath: execPath, sha: sha,
		afs: afs, set: set, store: store,
	}

	host, err := pluginhost.NewManager(pluginhost.Config{
		Dir:      pluginDir,
		Entries:  afs.Sources,
		DataRoot: filepath.Join(root, settings.DirData),
		LogRoot:  filepath.Join(root, settings.DirLog),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(host.Close)
	f.host = host

	// 起插件：控制层里每次改清单之后做的也是这一步。
	if err := host.Reconfigure(context.Background(), afs.Sources); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	f.opts = Options{
		Settings:    set,
		Apps:        afs,
		Host:        host,
		Feed:        store,
		Version:     "1.5.0",
		EventBuffer: 16,
	}
	if tweak != nil {
		tweak(f)
	}
	ctrl, err := New(f.opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.ctrl = ctrl
	return f
}

// pluginSourceAppSpec 是一条「由插件提供」的软件声明。
//
// 生产里这类条目由宿主从插件目录枚举出来（apps.yaml 不序列化它），测试直接注入，
// 因为这里要验的是控制层的行为，而不是宿主的枚举。
func pluginSourceAppSpec(appID, sourceID, installDir string) apps.AppSpec {
	return apps.AppSpec{
		ID:     appID,
		Name:   appID,
		Source: map[string]any{apps.KeyKind: apps.KindPlugin + ":" + sourceID, apps.SourceKeyApp: appID},
		// 源走插件（版本由插件回答，离线），方法与探测走内置适配器。
		Method: map[string]any{apps.KeyKind: "portable-inplace"},
		Detect: []string{"dir-name"},
		Install: apps.InstallSpec{
			Path:        installDir,
			Entrypoints: []string{"vpn.exe"},
		},
	}
}

// writeText 写一个文本文件（失败即测试失败）。
func writeText(t *testing.T, path, body string) {
	t.Helper()
	if err := fsutil.WriteFileAtomic(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}
