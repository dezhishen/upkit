package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/settings"
)

// 订阅详情的异步消息。
type (
	// feedLoadedMsg 是一次订阅拉取的结果。
	feedLoadedMsg struct {
		url     string
		entries []pluginfeed.Entry
		err     error
	}
	// installDoneMsg 是一次安装的结果。
	installDoneMsg struct {
		entry  pluginfeed.Entry
		result *pluginfeed.Installed
		err    error
	}
	// installProgressMsg 是下载进度。
	installProgressMsg struct{ done, total int64 }
)

// progressDone 是"本次安装结束"的哨兵值。
const progressDone = int64(-1)

// installProgress 是一次下载进度快照。
type installProgress struct{ done, total int64 }

// feedClient 是订阅拉取与插件下载共用的 HTTP 客户端。
//
// 下载可能几十 MB，所以超时给得很宽松；订阅拉取单独用短超时（见 loadFeedCmd）。
// 代理走用户配置：只认环境变量会让「软件更新正常、订阅全部失败」变得难以排查。
func (m Model) feedClient() *http.Client {
	c := &http.Client{Timeout: 30 * time.Minute}
	if proxy := proxyFromSettings(m.set); proxy != "" {
		tr, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return c
		}
		clone := tr.Clone()
		if u, err := url.Parse(proxy); err == nil {
			clone.Proxy = http.ProxyURL(u)
			c.Transport = clone
		}
	}
	return c
}

// proxyFromSettings 取出用户配置的代理地址。
func proxyFromSettings(s *settings.Settings) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.Network.Proxy)
}

// hostVersionForFeed 返回用于校验 min_host_version 的宿主版本。
//
// 开发版本（dev）不参与比较，否则官方源的 min_host_version 会把本地构建挡在门外。
func (m Model) hostVersionForFeed() string {
	v := strings.TrimSpace(m.opts.Version)
	if v == "" || v == "dev" {
		return ""
	}
	return v
}

// loadFeedCmd 拉取订阅、校验、展开成当前平台的条目，并标注本机已装版本。
func (m Model) loadFeedCmd(rawURL string) tea.Cmd {
	pluginDir := m.set.Plugins.Dir
	hostVersion := m.hostVersionForFeed()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		feed, err := pluginfeed.Fetch(ctx, m.feedClient(), rawURL)
		if err != nil {
			return feedLoadedMsg{url: rawURL, err: err}
		}
		if err := feed.Validate(hostVersion, pluginfeed.Platform()); err != nil {
			return feedLoadedMsg{url: rawURL, err: err}
		}
		entries, err := pluginfeed.Plan(feed, rawURL)
		if err != nil {
			return feedLoadedMsg{url: rawURL, err: err}
		}
		// 本机已装的版本来自插件目录里的描述文件（订阅安装时会写入 version/sha256）。
		if manifests, err := pluginhost.Discover(pluginDir); err == nil {
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
		return feedLoadedMsg{url: rawURL, entries: entries}
	}
}

// installEntryCmd 下载并安装一个条目；进度通过 installCh 回传。
func (m Model) installEntryCmd(rawURL string, e pluginfeed.Entry) tea.Cmd {
	ch := m.installCh
	pluginDir := m.set.Plugins.Dir
	cacheDir := m.set.Storage.CacheDir
	store := m.feed
	// 不能叫 host：Authorize 的参数同名，会遮蔽掉这个捕获的宿主。
	pluginHost := m.host

	return func() tea.Msg {
		if ch != nil {
			defer func() { ch <- installProgress{done: progressDone} }()
		}
		res, err := pluginfeed.Install(context.Background(), m.feedClient(), pluginfeed.InstallRequest{
			FeedURL:   rawURL,
			Entry:     e,
			PluginDir: pluginDir,
			CacheDir:  cacheDir,
			// 跨域授权已经在界面上处理过了，这里只做纯查表（不能在这里弹窗）。
			Authorize: func(host string) (bool, error) {
				if store == nil {
					return false, nil
				}
				return store.HostAuthorized(host), nil
			},
			// 盖掉旧文件之前先停掉旧进程：Windows 上正在运行的映像不能被替换，
			// 否则更新会以「Access is denied」失败。
			BeforeWrite: func() error {
				if pluginHost == nil {
					return nil
				}
				return pluginHost.StopForUpdate(e.Plugin.ID)
			},
			Progress: func(done, total int64) {
				if ch == nil {
					return
				}
				select {
				case ch <- installProgress{done: done, total: total}:
				default: // 界面来不及消费时丢帧，不要拖慢下载
				}
			},
		})
		return installDoneMsg{entry: e, result: res, err: err}
	}
}

// waitInstallProgress 持续接收下载进度，直到收到结束哨兵。
func (m Model) waitInstallProgress() tea.Cmd {
	ch := m.installCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return installProgressMsg{done: p.done, total: p.total}
	}
}

// startInstall 在界面上处理授权后开始安装。
//
// 跨域下载必须先拿到用户确认：这一步必须在 UI 线程完成，不能塞进下载的 goroutine。
func (m Model) startInstall(e pluginfeed.Entry) (tea.Model, tea.Cmd) {
	if m.feedBusy {
		m.setStatus("已有安装任务在进行")
		return m, nil
	}
	if m.feed == nil {
		m.setStatusErr(fmt.Errorf("订阅模块未启用"))
		return m, nil
	}
	switch e.Action() {
	case pluginfeed.ActionCurrent:
		if e.InstalledSHA256 != "" && e.InstalledSHA256 != pluginfeed.NormalizeSHA256(e.Package.SHA256) {
			// 版本相同但摘要不同：上游重发过，允许覆盖安装。
			break
		}
		m.setStatus(fmt.Sprintf("%s 已是最新（%s）", e.Plugin.Name, e.Plugin.Version))
		return m, nil
	case pluginfeed.ActionDowngrade:
		m.setStatus(fmt.Sprintf("%s：订阅版本 %s 比本机 %s 更旧，已拒绝降级",
			e.Plugin.Name, e.Plugin.Version, e.Installed))
		return m, nil
	}

	url := m.feedFor
	if e.Location.NeedsAuthorization() && !m.feed.HostAuthorized(e.Location.Host) {
		host := e.Location.Host
		entry := e
		m.confirm = &confirmBox{
			Title: "授权下载域名",
			Message: fmt.Sprintf("「%s」的安装包放在 %s，与订阅不同源。\n\n"+
				"信任该域名并继续安装？", entry.Plugin.Name, host),
			OnYes: func(mm *Model) tea.Cmd {
				if err := mm.feed.AuthorizeHost(host, url); err != nil {
					mm.setStatusErr(err)
					return nil
				}
				mm.feedBusy = true
				mm.feedProg = installProgress{}
				return tea.Batch(mm.installEntryCmd(url, entry), mm.waitInstallProgress())
			},
		}
		return m, nil
	}

	m.feedBusy = true
	m.feedProg = installProgress{}
	return m, tea.Batch(m.installEntryCmd(url, e), m.waitInstallProgress())
}

// ── 渲染 ──────────────────────────────────────────────────────

func (m Model) viewFeedDetail(w, height int) string {
	var b strings.Builder
	b.WriteString(m.theme.Dim().Render("订阅："))
	b.WriteString(m.theme.Primary().Render(m.feedFor))
	b.WriteString("\n\n")

	switch {
	case m.feedErr != nil:
		b.WriteString(m.theme.Err().Render("拉取失败：" + m.feedErr.Error()))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Dim().Render("r 重试   esc 返回"))
		return m.theme.Frame("订阅详情", b.String(), w, height, true)
	case !m.feedLoaded:
		b.WriteString(m.spinnerText() + " 正在拉取订阅…")
		return m.theme.Frame("订阅详情", b.String(), w, height, true)
	case len(m.feedEntries) == 0:
		b.WriteString("该订阅没有适配当前平台的插件。")
		b.WriteString("\n\n")
		b.WriteString(m.theme.Dim().Render("r 重新拉取   esc 返回"))
		return m.theme.Frame("订阅详情", b.String(), w, height, true)
	}

	for i, e := range m.feedEntries {
		b.WriteString(m.feedLine(i, e))
		b.WriteString("\n")
	}

	if m.feedBusy {
		b.WriteString("\n")
		if m.feedProg.total > 0 {
			fmt.Fprintf(&b, "%s %d%%  %s / %s",
				m.theme.Bar(m.feedProg.done, m.feedProg.total, 20),
				m.feedProg.done*100/m.feedProg.total,
				humanBytes(m.feedProg.done), humanBytes(m.feedProg.total))
		} else {
			b.WriteString(m.spinnerText() + " 处理中…")
		}
	}

	b.WriteString("\n")
	b.WriteString(m.theme.Dim().Render("i/enter 安装或更新   r 重新拉取   esc 返回"))
	return m.theme.Frame("订阅详情", strings.TrimRight(b.String(), "\n"), w, height, true)
}

func (m Model) feedLine(i int, e pluginfeed.Entry) string {
	cur := m.theme.Cursor(i == m.feedCursor)
	name := e.Plugin.Name
	if name == "" {
		name = e.Plugin.ID
	}
	version := e.Plugin.Version

	switch e.Action() {
	case pluginfeed.ActionInstall:
		return fmt.Sprintf("%s%s  %s  %s", cur, m.theme.Primary().Render(name),
			m.theme.Dim().Render("v"+version), m.theme.OK().Render("可安装"))
	case pluginfeed.ActionUpdate:
		return fmt.Sprintf("%s%s  %s → %s  %s", cur, m.theme.Primary().Render(name),
			m.theme.Dim().Render(e.Installed), m.theme.OK().Render(version), m.theme.OK().Render("可更新"))
	case pluginfeed.ActionDowngrade:
		return fmt.Sprintf("%s%s  %s → %s  %s", cur, m.theme.Primary().Render(name),
			m.theme.Dim().Render(e.Installed), m.theme.Warn().Render(version), m.theme.Warn().Render("降级（将拒绝）"))
	default:
		return fmt.Sprintf("%s%s  %s  %s", cur, m.theme.Primary().Render(name),
			m.theme.Dim().Render("v"+version), m.theme.Dim().Render("已是最新"))
	}
}

// ── 按键 ──────────────────────────────────────────────────────

func (m Model) updateFeedDetail(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q", "h", "left", "backspace":
		m.feedFor = ""
		m.feedEntries = nil
		m.feedLoaded = false
		m.feedErr = nil
		return m, nil
	case "j", "down":
		m.feedCursor++
	case "k", "up":
		m.feedCursor--
	case "g", "home":
		m.feedCursor = 0
	case "G", "end":
		m.feedCursor = len(m.feedEntries) - 1
	case "r":
		if m.feedBusy {
			m.setStatus("安装进行中，稍后再刷新")
			return m, nil
		}
		m.feedLoaded = false
		m.feedErr = nil
		return m, m.loadFeedCmd(m.feedFor)
	case "i", "enter", "l":
		if m.feedCursor >= 0 && m.feedCursor < len(m.feedEntries) {
			return m.startInstall(m.feedEntries[m.feedCursor])
		}
	}
	if m.feedCursor < 0 {
		m.feedCursor = 0
	}
	if m.feedCursor >= len(m.feedEntries) {
		m.feedCursor = len(m.feedEntries) - 1
	}
	return m, nil
}

// humanBytes 把字节数格式化成易读形式。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
