package control

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"io"
	"sync/atomic"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
)

// ── 订阅服务（自签证书的 https 测试服务） ───────────────────────
//
// 订阅地址强制 https —— 明文链路下中间人可以整份替换清单，而清单自带的 sha256
// 只保护清单以下的那一层。测试因此必须用 https 服务，并把信任自签证书的客户端
// 注进控制层（Options.HTTPClient 存在的意义就是这个）。

// tlsFeed 是一个假订阅服务：同时提供清单（内容可换）与插件包，外加一个「第二个
// 域名」，用来构造跨域下载的场景。
//
// 与 feed_test.go 里那个更简单的 newFeedServer 的区别：这里的清单内容可以在用例
// 中途换掉（要验 404、格式不对、宿主太旧这几种拒绝路径），而且包是真实二进制。
type tlsFeed struct {
	srv     *httptest.Server
	other   *httptest.Server
	payload []byte
	body    atomic.Value
}

func newTLSFeed(t *testing.T, payload []byte) *tlsFeed {
	t.Helper()
	fs := &tlsFeed{payload: payload}
	fs.body.Store("")

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.yaml":
			_, _ = io.WriteString(w, fs.body.Load().(string))
		case "/plugin.exe":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}
	fs.srv = httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(fs.srv.Close)
	fs.other = httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(fs.other.Close)
	return fs
}

func (fs *tlsFeed) setFeed(body string) { fs.body.Store(body) }

func (fs *tlsFeed) feedURL() string { return fs.srv.URL + "/feed.yaml" }

// feedBody 生成一份订阅清单（单个插件，相对路径引用包）。
func feedBody(id, version, sha string, size int, url string) string {
	return fmt.Sprintf(`schema: 1
name: 测试订阅
plugins:
  - id: %s
    name: 示例静态源
    version: %s
    mode: catalog
    packages:
      %s:
        url: %s
        sha256: %s
        size: %d
`, id, version, pluginfeed.Platform(), url, sha, size)
}

// 订阅仓库本身（订阅列表、三级授权）的读写：界面上的每一步都对应这里的一个方法。
func TestFeedStoreSurface(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	if !f.ctrl.FeedAvailable() {
		t.Fatal("装配了订阅仓库就应报告可用")
	}
	if subs := f.ctrl.Subscriptions(); len(subs) != 0 {
		t.Fatalf("初始没有订阅: %+v", subs)
	}
	if f.ctrl.FeatureAuthorized() {
		t.Fatal("订阅功能默认关闭（要用户显式开启）")
	}
	if err := f.ctrl.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	if !f.ctrl.FeatureAuthorized() {
		t.Fatal("开启后应报告已授权")
	}
	// 落盘了才算数。
	reloaded, err := pluginfeed.LoadStore(f.store.Path())
	if err != nil {
		t.Fatalf("重读订阅仓库: %v", err)
	}
	if !reloaded.FeatureAuthorized() {
		t.Fatal("功能授权没落盘")
	}

	if host, err := f.ctrl.FeedHost("https://example.com/feed.yaml"); err != nil || host != "example.com" {
		t.Fatalf("FeedHost = %q %v", host, err)
	}
	if _, err := f.ctrl.FeedHost("http://example.com/feed.yaml"); err == nil {
		t.Fatal("明文 http 订阅地址应被拒绝")
	}

	// 添加订阅：顺带把「信任该域名下载插件」记下来（三级授权里的第 ② 级）。
	sub, err := f.ctrl.AddSubscription("https://example.com/feed.yaml")
	if err != nil {
		t.Fatalf("AddSubscription: %v", err)
	}
	if sub.URL == "" || sub.Host != "example.com" {
		t.Fatalf("订阅应记下域名（供审计与撤销）: %+v", sub)
	}
	// 订阅本身不等于「授权跨域下载」：同源的包地址根本不需要额外授权，
	// 只有包地址跨域时才会用到第 ③ 级授权（见下面）。
	if f.ctrl.HostAuthorized("example.com") {
		t.Fatal("添加订阅不该顺手把域名记成「已授权跨域下载」")
	}
	if len(f.ctrl.Subscriptions()) != 1 {
		t.Fatalf("订阅列表应有一条: %+v", f.ctrl.Subscriptions())
	}
	if f.ctrl.HasBuiltinSubscription() {
		t.Fatal("官方源还没添加")
	}
	// 官方源（内置订阅地址）单独判定。
	if _, err := f.ctrl.AddSubscription(pluginfeed.BuiltinFeedURL); err != nil {
		t.Fatalf("添加官方源: %v", err)
	}
	if !f.ctrl.HasBuiltinSubscription() {
		t.Fatal("添加官方源之后应报告「已有官方源」")
	}

	if err := f.ctrl.SetSubscriptionEnabled("https://example.com/feed.yaml", false); err != nil {
		t.Fatalf("SetSubscriptionEnabled: %v", err)
	}
	for _, s := range f.ctrl.Subscriptions() {
		if s.URL == "https://example.com/feed.yaml" && s.EnabledValue() {
			t.Fatalf("停用没生效: %+v", s)
		}
	}
	if err := f.ctrl.RemoveSubscription("https://example.com/feed.yaml"); err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}
	if len(f.ctrl.Subscriptions()) != 1 {
		t.Fatalf("应只剩官方源: %+v", f.ctrl.Subscriptions())
	}

	// 第 ③ 级授权：跨域下载域名。
	if f.ctrl.HostAuthorized("cdn.example.org") {
		t.Fatal("没授权过的域名不该被信任")
	}
	if err := f.ctrl.AuthorizeHost("cdn.example.org", "https://example.com/feed.yaml"); err != nil {
		t.Fatalf("AuthorizeHost: %v", err)
	}
	if !f.ctrl.HostAuthorized("cdn.example.org") {
		t.Fatal("授权后应记住该域名")
	}

	if _, err := f.ctrl.AddSubscription("http://example.com/feed.yaml"); err == nil {
		t.Fatal("明文订阅地址不该被接受")
	}
	if err := f.ctrl.RemoveSubscription("https://never-added.example.com/f.yaml"); err == nil {
		t.Fatal("删除不存在的订阅应报错")
	}
	_ = ctx
}

// 拉取订阅并展开：本机已装版本要标注出来（界面靠它显示「可更新」）。
func TestFeedEntries(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	payload, _ := os.ReadFile(f.execPath)
	fs := newTLSFeed(t, payload)
	fs.setFeed(feedBody("example-static", "2.0.0", f.sha, len(payload), "plugin.exe"))
	f.opts.HTTPClient = fs.srv.Client()
	ctrl := newControllerWith(t, f)

	entries, err := ctrl.FeedEntries(ctx, fs.feedURL())
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应展开出 1 条: %+v", entries)
	}
	e := entries[0]
	if e.Plugin.ID != "example-static" || e.Plugin.Version != "2.0.0" {
		t.Fatalf("条目不对: %+v", e)
	}
	if !e.Location.Relative || !e.Location.SameOrigin {
		t.Fatalf("相对路径应算同源: %+v", e.Location)
	}
	// 插件目录里的描述文件说装着 1.2.0：界面据此显示「可更新」。
	if e.Installed != "1.2.0" || e.InstalledSHA256 != f.sha {
		t.Fatalf("应标注本机已装版本: %+v", e)
	}

	// 清单本身取不到 / 格式不对 / 宿主太旧：都要报错，不能当成「没有插件」。
	if _, err := ctrl.FeedEntries(ctx, fs.srv.URL+"/missing.yaml"); err == nil {
		t.Fatal("404 应报错")
	}
	fs.setFeed("schema: 1\nplugins:\n  - id: broken\n")
	if _, err := ctrl.FeedEntries(ctx, fs.feedURL()); err == nil {
		t.Fatal("没有平台包的清单应报错")
	}
	body := strings.Replace(feedBody("example-static", "2.0.0", f.sha, len(payload), "plugin.exe"),
		"mode: catalog", "mode: catalog\n    min_host_version: \"99.0.0\"", 1)
	fs.setFeed(body)
	if _, err := ctrl.FeedEntries(ctx, fs.feedURL()); err == nil ||
		!strings.Contains(err.Error(), "99.0.0") {
		t.Fatalf("宿主版本太旧应报错: %v", err)
	}

	// 宿主版本报「dev」时不参与比较：本地构建不该被官方源挡在门外。
	dev := newPluginControl(t, nil)
	dev.opts.HTTPClient = fs.srv.Client()
	dev.opts.Version = "dev"
	devCtrl := newControllerWith(t, dev)
	if _, err := devCtrl.FeedEntries(ctx, fs.feedURL()); err != nil {
		t.Fatalf("dev 版本不该被 min_host_version 挡住: %v", err)
	}
}

// 安装插件：这是控制层里最长的一条多步流程。
//
// 下载校验 → 覆盖前停掉旧进程 → 覆盖 → 记信任 → 重建来源，缺一步都会留下上不了
// 台面的状态（「装完变未信任」「更新报 Access is denied」都真实发生过）。
func TestInstallPlugin(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()

	// 订阅里的包换成「2.0.0 版」的二进制：内容不同，摘要才会不同。
	newDir := t.TempDir()
	newPath, newSHA := buildExamplePluginVersion(t, newDir, "2.0.0")
	payload, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("读新插件: %v", err)
	}
	fs := newTLSFeed(t, payload)
	fs.setFeed(feedBody("example-static", "2.0.0", newSHA, len(payload), "plugin.exe"))
	f.opts.HTTPClient = fs.srv.Client()
	ctrl := newControllerWith(t, f)

	entries, err := ctrl.FeedEntries(ctx, fs.feedURL())
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	if entries[0].Installed != "1.2.0" {
		t.Fatalf("应看到本机已装 1.2.0: %+v", entries[0])
	}

	var progressCalls int
	var lastTotal int64
	res, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entries[0], func(_, total int64) {
		progressCalls++
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	if res == nil || res.ID != "example-static" || res.Version != "2.0.0" {
		t.Fatalf("安装结果不对: %+v", res)
	}
	if res.SHA256 != newSHA {
		t.Fatalf("摘要应为订阅声明的那个: %q", res.SHA256)
	}
	if res.Path != f.execPath {
		t.Fatalf("应覆盖原来的插件文件: %q", res.Path)
	}
	if res.Cached {
		t.Fatal("第一次安装不该命中缓存")
	}
	if progressCalls == 0 || lastTotal <= 0 {
		t.Fatalf("应报告下载进度: calls=%d total=%d", progressCalls, lastTotal)
	}
	// sidecar 要写成新版本，且记下它来自哪条订阅。
	manifests, err := pluginhost.Discover(f.pluginDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var found bool
	for _, m := range manifests {
		if m.ID == "example-static" {
			found = true
			if m.Version != "2.0.0" || m.SHA256 != newSHA {
				t.Fatalf("描述文件没更新: %+v", m)
			}
			if m.Subscription != fs.feedURL() {
				t.Fatalf("描述文件应记下订阅地址: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("应写出插件描述文件")
	}
	// 信任要跟着新摘要走：内容换了，老的信任值必须失效。
	reloaded, err := apps.Load(f.afs.Path)
	if err != nil {
		t.Fatalf("重读清单: %v", err)
	}
	if reloaded.Sources[0].Trust != newSHA {
		t.Fatalf("信任摘要没更新: %q", reloaded.Sources[0].Trust)
	}
	// 宿主也要重载：装完插件应当已经在跑（版本来自插件自报）。
	for _, s := range ctrl.Sources() {
		if s.ID == "example-static" {
			if s.State != pluginhost.StateOK {
				t.Fatalf("装完应已加载: %+v", s)
			}
			if s.Version != "2.0.0" {
				t.Fatalf("跑起来的应是新版本: %+v", s)
			}
		}
	}

	// 再装一次：命中缓存，不再下载。
	res2, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entries[0], nil)
	if err != nil {
		t.Fatalf("重复安装: %v", err)
	}
	if !res2.Cached {
		t.Fatal("第二次应复用缓存里的包")
	}
}

// 安装的各种拒绝路径：降级、摘要不符、跨域未授权。
func TestInstallPluginRejections(t *testing.T) {
	f := newPluginControl(t, nil)
	ctx := context.Background()
	payload, err := os.ReadFile(f.execPath)
	if err != nil {
		t.Fatalf("读插件: %v", err)
	}
	fs := newTLSFeed(t, payload)
	fs.setFeed(feedBody("example-static", "2.0.0", f.sha, len(payload), "plugin.exe"))
	f.opts.HTTPClient = fs.srv.Client()
	ctrl := newControllerWith(t, f)

	entry := pluginfeed.Entry{
		Plugin: pluginfeed.Plugin{ID: "example-static", Version: "1.0.0"},
		Package: pluginfeed.Package{
			URL: "plugin.exe", SHA256: f.sha, Size: int64(len(payload)),
		},
		Location: pluginfeed.Location{
			URL: fs.srv.URL + "/plugin.exe", Host: strings.TrimPrefix(fs.srv.URL, "https://"),
			SameOrigin: true, Relative: true,
		},
		// 本机装着 2.0.0，订阅给的是 1.0.0：降级是典型的供应链攻击手法。
		Installed: "2.0.0",
	}
	if _, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entry, nil); err == nil ||
		!strings.Contains(err.Error(), "降级") {
		t.Fatalf("降级应被拒绝: %v", err)
	}

	entry.Installed = "1.2.0"
	entry.Package.SHA256 = strings.Repeat("0", 64)
	if _, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entry, nil); err == nil {
		t.Fatal("摘要不符应被拒绝")
	}

	// 跨域：订阅之外的域名要单独授权，未授权时一律拒绝。
	entry.Package.SHA256 = f.sha
	entry.Location = pluginfeed.Location{
		URL: fs.other.URL + "/plugin.exe", Host: strings.TrimPrefix(fs.other.URL, "https://"),
		SameOrigin: false,
	}
	if _, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entry, nil); err == nil ||
		!strings.Contains(err.Error(), "未授权") {
		t.Fatalf("跨域未授权应被拒绝: %v", err)
	}
	// 用户确认之后（第 ③ 级授权）应当能装。授权链路有顺序：订阅功能（①）必须先
	// 打开，域名授权（③）才有地方记。
	hostHeader := strings.TrimPrefix(fs.other.URL, "https://")
	if err := ctrl.AuthorizeHost(hostHeader, fs.feedURL()); err == nil {
		t.Fatal("订阅功能未启用时不该能授权域名")
	}
	if err := ctrl.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	if err := ctrl.AuthorizeHost(hostHeader, fs.feedURL()); err != nil {
		t.Fatalf("AuthorizeHost: %v", err)
	}
	entry.Plugin.Version = "3.0.0"
	if _, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entry, nil); err != nil {
		t.Fatalf("授权后应能跨域安装: %v", err)
	}
	// 安装失败时也要重建来源：覆盖前可能已经把旧进程停掉了，不重建就把它留在
	// 「已停止」。这里断言「失败之后来源仍在跑」。
	for _, s := range ctrl.Sources() {
		if s.ID == "example-static" && s.State == pluginhost.StateDisabled {
			t.Fatalf("失败/成功后来源都应在跑: %+v", s)
		}
	}
}

// 关掉「装完直接记信任」之后，摘要不该被写进清单：用户要逐个确认。
func TestInstallPluginKeepsUntrustedWithLiveHost(t *testing.T) {
	f := newPluginControl(t, func(f *pluginFixture) { f.set.Plugins.AutoLoadTrusted = false })
	ctx := context.Background()

	newDir := t.TempDir()
	newPath, newSHA := buildExamplePluginVersion(t, newDir, "2.0.0")
	payload, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("读新插件: %v", err)
	}
	fs := newTLSFeed(t, payload)
	fs.setFeed(feedBody("example-static", "2.0.0", newSHA, len(payload), "plugin.exe"))
	f.opts.HTTPClient = fs.srv.Client()
	ctrl := newControllerWith(t, f)

	entries, err := ctrl.FeedEntries(ctx, fs.feedURL())
	if err != nil {
		t.Fatalf("FeedEntries: %v", err)
	}
	if _, err := ctrl.InstallPlugin(ctx, fs.feedURL(), entries[0], nil); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	// 清单这时可能还没落过盘（没有信任要写，就没触发 Save），所以按 id 找而不是
	// 直接索引第一个。
	if reloaded, err := apps.Load(f.afs.Path); err == nil {
		for _, src := range reloaded.Sources {
			if src.ID == "example-static" && src.Trust == newSHA {
				t.Fatal("关掉自动信任之后不该把新摘要写进清单")
			}
		}
	}
	// 内存里的信任也不该被动过。
	if f.afs.Sources[0].Trust != f.sha {
		t.Fatalf("内存里的信任不该被改写: %q", f.afs.Sources[0].Trust)
	}
}

// 代理设置要真的进到订阅客户端里：只认环境变量会让「软件更新正常、订阅全部失败」
// 变得难以排查。
func TestFeedClientUsesProxySetting(t *testing.T) {
	f := newPluginControl(t, nil)
	if tr, ok := f.ctrl.feedClient().Transport.(*http.Transport); ok && tr.Proxy != nil {
		t.Fatal("没配代理时不该挂代理函数")
	}

	withProxy := newPluginControl(t, func(f *pluginFixture) {
		f.set.Network.Proxy = "http://127.0.0.1:3128"
	})
	client := withProxy.ctrl.feedClient()
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("配了代理之后应挂上代理函数: %+v", client.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	u, err := tr.Proxy(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:3128" {
		t.Fatalf("代理地址不对: %v %v", u, err)
	}
	if client.Timeout == 0 {
		t.Fatal("下载超时应设置（插件包可能几十 MB）")
	}
}

// newControllerWith 用夹具当前（可能已被测试改过的）装配参数再建一个控制层。
//
// 有些用例要在夹具就绪之后再补 HTTPClient / 版本号，重建一次比让夹具长出一堆
// 可选参数清楚。
func newControllerWith(t *testing.T, f *pluginFixture) *Controller {
	t.Helper()
	ctrl, err := New(f.opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ctrl
}

// 让 filepath 参与（插件目录在断言里用到），同时保留可读性。
var _ = filepath.Join
