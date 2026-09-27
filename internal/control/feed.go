package control

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
)

// downloadTimeout 是插件下载的超时：包可能几十 MB，给得很宽松。
const downloadTimeout = 30 * time.Minute

// feedTimeout 是订阅拉取（清单本身很小）的超时。
const feedTimeout = 60 * time.Second

// ── 订阅与授权 ────────────────────────────────────────────────

// FeedAvailable 报告订阅模块是否可用（未启用时界面上的订阅入口应给出提示）。
func (c *Controller) FeedAvailable() bool { return c.feed != nil }

// Subscriptions 返回已记录的订阅。
func (c *Controller) Subscriptions() []pluginfeed.Subscription {
	if c.feed == nil {
		return nil
	}
	return c.feed.Subscriptions()
}

// FeatureAuthorized 报告订阅功能是否已启用（默认关闭）。
func (c *Controller) FeatureAuthorized() bool {
	return c.feed != nil && c.feed.FeatureAuthorized()
}

// AuthorizeFeature 启用订阅功能。这是三级授权里的第 ① 级，只能由用户确认触发。
func (c *Controller) AuthorizeFeature() error {
	if c.feed == nil {
		return fmt.Errorf("订阅模块未启用")
	}
	return c.feed.AuthorizeFeature()
}

// FeedHost 解析订阅地址的域名，供授权弹窗展示用（不改任何状态）。
func (c *Controller) FeedHost(rawURL string) (string, error) {
	return pluginfeed.FeedHost(rawURL)
}

// HasBuiltinSubscription 报告官方源是否已在订阅列表里。
func (c *Controller) HasBuiltinSubscription() bool {
	for _, sub := range c.Subscriptions() {
		if pluginfeed.IsBuiltinFeed(sub.URL) {
			return true
		}
	}
	return false
}

// AddSubscription 添加一条订阅并授权其域名（三级授权里的第 ② 级）。
//
// 调用方必须已经拿到用户对「信任该域名的插件下载」的确认 —— 这里不做询问。
func (c *Controller) AddSubscription(rawURL string) (pluginfeed.Subscription, error) {
	if c.feed == nil {
		return pluginfeed.Subscription{}, fmt.Errorf("订阅模块未启用")
	}
	return c.feed.AddSubscription(rawURL)
}

// RemoveSubscription 删除一条订阅（已装的插件不会被卸载）。
func (c *Controller) RemoveSubscription(rawURL string) error {
	if c.feed == nil {
		return fmt.Errorf("订阅模块未启用")
	}
	return c.feed.RemoveSubscription(rawURL)
}

// SetSubscriptionEnabled 启停一条订阅。
func (c *Controller) SetSubscriptionEnabled(rawURL string, enabled bool) error {
	if c.feed == nil {
		return fmt.Errorf("订阅模块未启用")
	}
	return c.feed.SetSubscriptionEnabled(rawURL, enabled)
}

// HostAuthorized 报告某个下载域名是否已被授权（三级授权里的第 ③ 级）。
func (c *Controller) HostAuthorized(host string) bool {
	return c.feed != nil && c.feed.HostAuthorized(host)
}

// AuthorizeHost 授权某个下载域名，记住它属于哪条订阅。
func (c *Controller) AuthorizeHost(host, forSubscription string) error {
	if c.feed == nil {
		return fmt.Errorf("订阅模块未启用")
	}
	return c.feed.AuthorizeHost(host, forSubscription)
}

// ── 订阅内容与安装 ────────────────────────────────────────────

// FeedEntries 拉取并展开一条订阅，同时标注本机已装的版本。
func (c *Controller) FeedEntries(ctx context.Context, rawURL string) ([]pluginfeed.Entry, error) {
	feed, err := pluginfeed.Fetch(ctx, c.feedClient(), rawURL)
	if err != nil {
		return nil, err
	}
	if err := feed.Validate(c.hostVersion(), pluginfeed.Platform()); err != nil {
		return nil, err
	}
	entries, err := pluginfeed.Plan(feed, rawURL)
	if err != nil {
		return nil, err
	}

	// 本机已装的版本来自插件目录里的描述文件（订阅安装时会写入 version / sha256）。
	if manifests, err := pluginhost.Discover(c.pluginDir()); err == nil {
		installed := make(map[string]pluginhost.Manifest, len(manifests))
		for _, man := range manifests {
			installed[man.ID] = man
		}
		for i := range entries {
			if man, ok := installed[entries[i].Plugin.ID]; ok {
				entries[i].Installed = man.Version
				entries[i].InstalledSHA256 = man.SHA256
			}
		}
	}
	return entries, nil
}

// InstallPlugin 安装或更新一个插件。
//
// 这个方法是「多步流程只在一个地方」的典型：下载并校验（pluginfeed.Install 内部）→
// 覆盖前先停掉正在运行的旧进程 → 覆盖 → 把校验过的摘要记进信任 → 重建插件来源。
// 缺任何一步都会留下上不了台面的状态，此前它们散在界面的按键处理与 tea.Cmd 闭包里，
// 于是「装完变未信任」「更新报 Access is denied」都真实发生过。
//
// 跨域下载的授权由调用方在用户确认后写进订阅仓库（这里只查表，不弹窗）；progress 可
// 为空。
func (c *Controller) InstallPlugin(
	ctx context.Context,
	feedURL string,
	e pluginfeed.Entry,
	progress func(done, total int64),
) (*pluginfeed.Installed, error) {
	if c.feed == nil {
		return nil, fmt.Errorf("订阅模块未启用")
	}

	res, installErr := pluginfeed.Install(ctx, c.feedClient(), pluginfeed.InstallRequest{
		FeedURL:   feedURL,
		Entry:     e,
		PluginDir: c.pluginDir(),
		CacheDir:  c.cacheDir(),
		Authorize: func(host string) (bool, error) {
			return c.feed.HostAuthorized(host), nil
		},
		BeforeWrite: func() error {
			return c.stopForUpdate(e.Plugin.ID)
		},
		Progress: progress,
	})

	// 装成功就把信任一并记下来（依据：安装已过三级授权，包的 sha256 也被强制校验过，
	// 那个摘要就是用户授权过的内容）。关掉 AutoLoadTrusted 则留回逐个确认。
	var trustErr error
	if installErr == nil && res != nil && res.ID != "" && res.SHA256 != "" && c.autoLoadTrusted() {
		if c.afs != nil {
			c.afs.SetSourceTrust(res.ID, res.SHA256)
			if err := c.afs.Save(); err != nil {
				trustErr = fmt.Errorf("记录插件信任失败: %w", err)
			}
		}
	}

	// 无论成败都重建一次来源：成功要把新信任接上（插件随即起来）；失败前可能已经把
	// 旧进程停掉了（停进程与写盘之间出错），不重建就把它留在「已停止」。
	if c.host != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reloadTimeout)
		defer cancel()
		if err := c.host.Reconfigure(ctx, c.manifestSources()); err != nil && installErr == nil && trustErr == nil {
			return res, fmt.Errorf("重载插件来源: %w", err)
		}
	}

	if installErr != nil {
		return nil, installErr
	}
	return res, trustErr
}

// stopForUpdate 停掉插件进程并等它放开可执行文件；没装过或没跑起来时什么都不做。
func (c *Controller) stopForUpdate(id string) error {
	if c.host == nil {
		return nil
	}
	return c.host.StopForUpdate(id)
}

// autoLoadTrusted 报告「订阅装完是否直接记信任」。
func (c *Controller) autoLoadTrusted() bool {
	return c.set != nil && c.set.Plugins.AutoLoadTrusted
}

// hostVersion 返回用于校验 min_host_version 的宿主版本。
//
// 开发版本（dev）不参与比较，否则官方源的 min_host_version 会把本地构建挡在门外。
func (c *Controller) hostVersion() string {
	v := strings.TrimSpace(c.version)
	if v == "" || v == "dev" {
		return ""
	}
	return v
}

func (c *Controller) pluginDir() string {
	if c.set == nil {
		return ""
	}
	return c.set.Plugins.Dir
}

func (c *Controller) cacheDir() string {
	if c.set == nil {
		return ""
	}
	return c.set.Storage.CacheDir
}

// feedClient 是订阅拉取与插件下载共用的 HTTP 客户端。
//
// 下载可能几十 MB，所以超时给得很宽松；订阅拉取自己在调用处用短超时。
// 代理走用户配置：只认环境变量会让「软件更新正常、订阅全部失败」变得难以排查。
func (c *Controller) feedClient() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	client := &http.Client{Timeout: downloadTimeout}
	proxy := ""
	if c.set != nil {
		proxy = strings.TrimSpace(c.set.Network.Proxy)
	}
	if proxy == "" {
		return client
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return client
	}
	clone := tr.Clone()
	if u, err := url.Parse(proxy); err == nil {
		clone.Proxy = http.ProxyURL(u)
		client.Transport = clone
	}
	return client
}
