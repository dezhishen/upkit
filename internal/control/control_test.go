package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/manifest"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/settings"
)

// newControlTest 装配一个「没有插件宿主、没有订阅」的控制层：设置与清单落在临时目录里。
//
// 软件声明都用内置适配器（github-release 源 + dir-name 探测 + portable-inplace 方法），
// 因此除了「探测到的版本」之外全部离线可解析 —— 测试要能在断网机器上跑，也要能一眼
// 看出哪一步真的碰了网络。
//
// 里面放两条软件：
//   - demo：内置来源，安装路径带版本号（dir-name 探测据此报「已装 1.2.3」）；
//   - demo-plugin：插件来源（plugin:corp），用来验证「启停状态写在来源声明上」。
func newControlTest(t *testing.T, tweak func(*Options)) *Controller {
	t.Helper()
	root := t.TempDir()
	set, err := settings.Load(filepath.Join(root, settings.DirConfig, settings.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := set.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	afs := apps.Default()
	afs.Path = filepath.Join(root, settings.DirConfig, settings.AppsFileName)
	afs.Sources = []apps.SourceSpec{{ID: "corp", Name: "企业源", Kind: apps.KindPlugin}}
	afs.Apps = []apps.AppSpec{
		controlAppSpec(filepath.Join(root, "apps", "demo-1.2.3")),
		pluginAppSpec(filepath.Join(root, "apps", "demo-plugin-1.2.3"), "corp"),
	}

	// 真的建一个宿主（插件目录是空的）：插件来源的软件要能被解析，靠 nil 宿主
	// 会整份列表都报「插件子系统未启用」—— 那是生产里「宿主没起来」的情形，
	// 不是这里的默认场景。
	host, err := pluginhost.NewManager(pluginhost.Config{
		Dir:      filepath.Join(root, "plugin"),
		Entries:  afs.Sources,
		DataRoot: filepath.Join(root, "data"),
		LogRoot:  filepath.Join(root, "log"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(host.Close)

	opts := Options{Settings: set, Apps: afs, Host: host, Version: "dev", EventBuffer: 4}
	if tweak != nil {
		tweak(&opts)
	}
	ctrl, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ctrl
}

// controlAppSpec 是测试用的一条内置来源软件声明：四轴都选内置适配器，探测只看目录名。
func controlAppSpec(installDir string) apps.AppSpec {
	return apps.AppSpec{
		ID:     "demo",
		Name:   "Demo",
		Source: map[string]any{apps.KeyKind: "github-release", "repo": "owner/repo"},
		Unpack: map[string]any{apps.KeyKind: "zip"},
		Method: map[string]any{apps.KeyKind: "portable-inplace"},
		Detect: []string{"dir-name"},
		Install: apps.InstallSpec{
			Path:        installDir,
			Entrypoints: []string{"demo.exe"},
		},
	}
}

// pluginAppSpec 是一条插件来源的软件声明（启停状态只能写在来源声明上）。
func pluginAppSpec(installDir, sourceID string) apps.AppSpec {
	return pluginSpec("demo-plugin", sourceID, installDir)
}

// pluginSpec 同 pluginAppSpec，但可以指定 id（导入夹具要用别的 id）。
func pluginSpec(appID, sourceID, installDir string) apps.AppSpec {
	return apps.AppSpec{
		ID:      appID,
		Name:    appID,
		Source:  map[string]any{apps.KeyKind: apps.KindPlugin + ":" + sourceID, apps.SourceKeyApp: appID},
		Unpack:  map[string]any{apps.KeyKind: "zip"},
		Method:  map[string]any{apps.KeyKind: "portable-inplace"},
		Detect:  []string{"dir-name"},
		Install: apps.InstallSpec{Path: installDir, Entrypoints: []string{appID + ".exe"}},
	}
}

// 缺设置或清单时 New 必须当场报错：控制层之后每个方法都假定它们存在，
// 让一个半死的 Controller 漏出去，错误会在很远的界面层才炸。
func TestNewRejectsIncompleteOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("没有设置与清单时应当报错")
	}
	root := t.TempDir()
	set, err := settings.Load(filepath.Join(root, settings.DirConfig, settings.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := New(Options{Settings: set}); err == nil {
		t.Fatal("没有清单时应当报错")
	}
}

// 软件列表这件事由控制层转出去，界面不该自己拿引擎：这条边界塌了，
// «哪些软件算存在» 就会在界面里再实现一遍。
func TestAppSurface(t *testing.T) {
	ctrl := newControlTest(t, nil)
	ctx := context.Background()

	// Apps() 读的是引擎里已经展开过的那份列表：界面在启动与刷新后才会拿到内容，
	// 所以这里先刷一次（这也是界面的真实顺序）。
	if _, err := ctrl.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	list := ctrl.Apps()
	if len(list) != 2 {
		t.Fatalf("软件列表应有 2 条: %+v", list)
	}
	if ctrl.Find("demo") == nil || ctrl.Find("demo-plugin") == nil {
		t.Fatal("Find 应能找到两条软件")
	}
	if ctrl.Find("ghost") != nil {
		t.Fatal("Find 不该凭空造出条目")
	}

	// 清单里没写 enabled：默认启用。
	if !ctrl.AppEnabled("demo") || !ctrl.AppEnabled("demo-plugin") {
		t.Fatal("未声明时默认启用")
	}
	// 未收录的 id 视为启用（清单是白名单，但界面上不该凭空多出「已停用」）。
	if !ctrl.AppEnabled("never-seen") {
		t.Fatal("清单里没有的 id 应视为启用")
	}

	// 内置来源的软件没有「来源声明」可写，启停状态无处安放 —— 必须明确报错，
	// 而不是默默改内存（重启就回来了）。
	if err := ctrl.SetAppEnabled("demo", false); err == nil {
		t.Fatal("内置来源的软件无法持久化启停状态，应报错")
	}
	// 插件来源的软件可以：状态写在 sources[].apps[] 上。
	if err := ctrl.SetAppEnabled("demo-plugin", false); err != nil {
		t.Fatalf("SetAppEnabled: %v", err)
	}
	if ctrl.AppEnabled("demo-plugin") {
		t.Fatal("停用后应为 false")
	}
	reloaded, err := apps.Load(ctrl.Settings().AppsPath())
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	if reloaded.SourceAppEnabled("corp", "demo-plugin") {
		t.Fatal("停用状态没落到来源声明上")
	}
	if err := ctrl.SetAppEnabled("never-seen", false); err == nil {
		t.Fatal("对不存在的软件改状态应报错")
	}

	if _, err := ctrl.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if ctrl.Settings() == nil {
		t.Fatal("Settings 不该是 nil")
	}
	if ctrl.SettingsPath() != ctrl.Settings().Path {
		t.Fatalf("SettingsPath 与设置自身不一致: %q", ctrl.SettingsPath())
	}
	if want := ctrl.Settings().ManifestPath(); ctrl.ManifestPath() != want {
		t.Fatalf("ManifestPath = %q，期望 %q", ctrl.ManifestPath(), want)
	}
	paths := ctrl.SettingsPaths()
	if len(paths) == 0 {
		t.Fatal("设置面板要展示一组路径")
	}
	var hasConfigDir bool
	for _, kv := range paths {
		if strings.Contains(kv[1], settings.DirConfig) {
			hasConfigDir = true
		}
	}
	if !hasConfigDir {
		t.Fatalf("路径里应包含配置目录: %+v", paths)
	}
}

// 事件流：满了丢事件但不能阻塞引擎 —— 拖住引擎等于拖住更新本身。
func TestEventStreamDropsWhenFull(t *testing.T) {
	ctrl := newControlTest(t, func(o *Options) { o.EventBuffer = 1 })
	sink := ctrl.Sink()
	if sink == nil {
		t.Fatal("Sink 不该是 nil")
	}

	// 缓冲区只有 1：连推 3 条必须立刻返回（不能在这里等消费者）。
	sink.Emit(newEvent("a"))
	sink.Emit(newEvent("b"))
	sink.Emit(newEvent("c"))

	got := <-ctrl.Events()
	if got.AppID != "a" {
		t.Fatalf("第一条事件应被保留: %+v", got)
	}
	select {
	case extra := <-ctrl.Events():
		t.Fatalf("缓冲区满时后两条应被丢弃，实际收到 %+v", extra)
	default:
	}
}

// 检查更新：ids 为空是「全查」，非空时逐个查。未知 id 要立刻报错，不能静静跳过。
func TestCheckAndPlanErrors(t *testing.T) {
	ctrl := newControlTest(t, nil)
	ctx := context.Background()

	if _, err := ctrl.Check(ctx, []string{"ghost"}); err == nil {
		t.Fatal("未知 id 应报错")
	}
	if _, err := ctrl.Plan(ctx, "ghost"); err == nil {
		t.Fatal("未知 id 生成计划应报错")
	}
	if err := ctrl.Rollback(ctx, "ghost"); err == nil {
		t.Fatal("未知 id 回滚应报错")
	}
	if err := ctrl.Uninstall(ctx, "ghost", true); err == nil {
		t.Fatal("未知 id 卸载应报错")
	}

	// 空 id 列表：Apply 直接返回 nil（不产生任务），Check 走「全查」分支。
	if res := ctrl.Apply(ctx, nil); res != nil {
		t.Fatalf("空 id 不该产生任务: %+v", res)
	}
	// 全查：清单为空时不会碰网络，正好用来覆盖这条分支。
	empty := newControlTest(t, func(o *Options) { o.Apps.Apps = nil })
	if got, err := empty.Check(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("空清单全查应为空: %+v %v", got, err)
	}

	// 批量执行里的失败要以任务结果返回，而不是整批炸掉：界面靠这个把每条标红。
	results := ctrl.Apply(ctx, []string{"demo", "ghost"})
	if len(results) == 0 {
		t.Fatal("应返回任务结果")
	}
	var failed bool
	for _, r := range results {
		if r.Err != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("其中的未知 id 应产生失败结果: %+v", results)
	}
}

// 清单导出/导入：导出的是「清单 + 已装版本」，导入只做合并。
func TestManifestExportImport(t *testing.T) {
	ctrl := newControlTest(t, nil)
	ctx := context.Background()

	// 本地探测与上游查询是两件事：demo-plugin 的来源（plugin:corp）在没有插件
	// 进程时查不到版本，但「本机装了什么」照样能从安装路径读出来 —— 界面在离线时
	// 依赖的正是不带网络的这一半。
	if _, err := ctrl.Check(ctx, []string{"demo-plugin"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if v := ctrl.Find("demo-plugin").Status.Version; v != "1.2.3" {
		t.Fatalf("探测到的版本不对: %q", v)
	}

	path, err := ctrl.ExportManifest()
	if err != nil {
		t.Fatalf("ExportManifest: %v", err)
	}
	if path != ctrl.ManifestPath() {
		t.Fatalf("默认导出路径 = %q，期望 %q", path, ctrl.ManifestPath())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读导出文件: %v", err)
	}
	if !strings.Contains(string(data), "1.2.3") || !strings.Contains(string(data), "demo") {
		t.Fatalf("导出内容里应有已装版本: %s", data)
	}

	// 导入自己导出的清单：不会有新增，但已存在的 id 一律算「更新」——
	// 合并是按订阅/清单给的声明整条替换，不做逐字段比较。
	before := len(ctrl.afs.Apps)
	added, updated, err := ctrl.ImportManifest(ctx, path)
	if err != nil {
		t.Fatalf("ImportManifest: %v", err)
	}
	if len(added) != 0 || len(updated) != before {
		t.Fatalf("导入同一份清单应只报「更新」: added=%v updated=%v", added, updated)
	}
	if len(ctrl.afs.Apps) != before {
		t.Fatalf("合并不该产生重复条目: %d → %d", before, len(ctrl.afs.Apps))
	}

	// 导入一份带新软件的清单：新条目要能被合并进来并落盘。
	// 用真的导出接口造夹具，格式改了这里会跟着改，而不是变成一份写死的旧格式。
	extra := apps.Default()
	extra.Apps = []apps.AppSpec{pluginSpec("extra", "corp", filepath.Join(t.TempDir(), "extra"))}
	fresh := filepath.Join(t.TempDir(), "manifest.json")
	if _, err := manifest.Export(fresh, nil, extra, nil); err != nil {
		t.Fatalf("造导入夹具: %v", err)
	}
	added, _, err = ctrl.ImportManifest(ctx, fresh)
	if err != nil {
		t.Fatalf("ImportManifest: %v", err)
	}
	if len(added) != 1 || added[0] != "extra" {
		t.Fatalf("应合并进 1 条新软件: %+v", added)
	}
	if _, err := os.Stat(ctrl.Settings().AppsPath()); err != nil {
		t.Fatalf("合并后应落盘: %v", err)
	}

	if _, _, err := ctrl.ImportManifest(ctx, filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("文件不存在应报错")
	}
}

// 日志快照：给界面初始化日志面板用；没有日志子系统时返回 nil 而不是 panic。
func TestLogSnapshot(t *testing.T) {
	ctrl := newControlTest(t, nil)
	if got := ctrl.LogSnapshot(); got != nil {
		t.Fatalf("没有日志子系统时应返回 nil: %+v", got)
	}

	withLog := newControlTest(t, func(o *Options) {
		mgr, err := logging.New(logging.Options{Level: "info", Dir: o.Settings.Logs.Dir})
		if err != nil {
			t.Fatalf("logging.New: %v", err)
		}
		t.Cleanup(func() { _ = mgr.Close() })
		o.Logger = mgr
		mgr.Info("控制层已装配")
	})
	snap := withLog.LogSnapshot()
	if len(snap) == 0 {
		t.Fatal("日志环里已有内容")
	}
	if snap[0].Msg == "" || snap[0].Level == "" {
		t.Fatalf("快照条目应带级别与消息: %+v", snap[0])
	}
}

// 子系统未启用时的护栏：每个入口都要给出人话，而不是 panic 或静默成功。
//
// 这些分支真实存在（没装插件的机器上宿主为 nil、订阅功能未开启时 feed 为 nil），
// 而且是「用户点了没反应」最常见的来源。
func TestSubsystemGuards(t *testing.T) {
	ctrl := newControlTest(t, nil)
	ctx := context.Background()
	ctrl.afs = nil
	ctrl.host = nil
	ctrl.feed = nil
	ctrl.log = nil
	ctrl.set = nil
	ctrl.version = ""

	if !ctrl.AppEnabled("demo") {
		t.Fatal("清单未加载时应视为启用")
	}
	if err := ctrl.SetAppEnabled("demo", true); err == nil {
		t.Fatal("清单未加载时改状态应报错")
	}
	if ctrl.LogSnapshot() != nil {
		t.Fatal("没有日志子系统时应返回 nil")
	}

	if ctrl.FeedAvailable() {
		t.Fatal("没有订阅模块时应报告不可用")
	}
	if subs := ctrl.Subscriptions(); subs != nil {
		t.Fatalf("没有订阅模块时应返回 nil: %+v", subs)
	}
	if ctrl.FeatureAuthorized() {
		t.Fatal("没有订阅模块时功能授权应为 false")
	}
	if ctrl.HostAuthorized("example.com") {
		t.Fatal("没有订阅模块时域名授权应为 false")
	}
	if ctrl.HasBuiltinSubscription() {
		t.Fatal("没有订阅模块时不该报告「已有官方源」")
	}
	guards := map[string]error{
		"AuthorizeFeature":       ctrl.AuthorizeFeature(),
		"RemoveSubscription":     ctrl.RemoveSubscription("https://example.com/feed.yaml"),
		"SetSubscriptionEnabled": ctrl.SetSubscriptionEnabled("https://example.com/feed.yaml", true),
		"AuthorizeHost":          ctrl.AuthorizeHost("example.com", "https://example.com/feed.yaml"),
	}
	for name, err := range guards {
		if err == nil || !strings.Contains(err.Error(), "订阅模块未启用") {
			t.Fatalf("%s 应报「订阅模块未启用」，实际 %v", name, err)
		}
	}
	if _, err := ctrl.AddSubscription("https://example.com/feed.yaml"); err == nil {
		t.Fatal("没有订阅模块时添加订阅应报错")
	}
	if _, err := ctrl.InstallPlugin(ctx, "https://example.com/feed.yaml", pluginEntry("demo"), nil); err == nil {
		t.Fatal("没有订阅模块时安装插件应报错")
	}

	// 插件来源相关：清单与宿主都可能缺席。
	if err := ctrl.ReloadPlugins(ctx); err == nil || !strings.Contains(err.Error(), "插件宿主未启用") {
		t.Fatalf("宿主未启用时应报错，实际 %v", err)
	}
	if err := ctrl.ReloadSources(ctx); err == nil || !strings.Contains(err.Error(), "清单未加载") {
		t.Fatalf("清单未加载时应报错，实际 %v", err)
	}
	if err := ctrl.TrustSource(ctx, "demo", "sha"); err == nil {
		t.Fatal("清单未加载时记录信任应报错")
	}
	if err := ctrl.SetPluginConfig(ctx, "demo", "k", "v"); err == nil {
		t.Fatal("清单未加载时改插件配置应报错")
	}
	if fields, err := ctrl.PluginConfig(ctx, "demo"); err != nil || fields != nil {
		t.Fatalf("没有宿主时应返回空: %+v %v", fields, err)
	}
	if infos := ctrl.Sources(); len(infos) != 0 {
		t.Fatalf("没有清单也没有宿主时来源应为空: %+v", infos)
	}

	// 设置没加载时的几个派生值。
	if ctrl.pluginDir() != "" || ctrl.cacheDir() != "" {
		t.Fatal("设置未加载时目录应为空串")
	}
	if ctrl.autoLoadTrusted() {
		t.Fatal("设置未加载时不该自动记信任")
	}
	if ctrl.hostVersion() != "" {
		t.Fatal("空版本应被当作 dev，不参与 min_host_version 比较")
	}
	ctrl.version = "dev"
	if ctrl.hostVersion() != "" {
		t.Fatal("dev 版本同样不参与比较")
	}
	ctrl.version = " 1.2.3 "
	if ctrl.hostVersion() != "1.2.3" {
		t.Fatalf("正式版本应原样返回（去掉空白）: %q", ctrl.hostVersion())
	}

	// 订阅客户端：没有注入时按设置构造，并带上宽松的下载超时。
	client := newControlTest(t, nil).feedClient()
	if client == nil || client.Timeout == 0 {
		t.Fatalf("按设置构造的客户端应有下载超时: %+v", client)
	}
}
