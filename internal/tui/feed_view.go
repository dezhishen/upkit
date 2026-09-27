package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/pluginfeed"
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

// feedFetchTimeout 是订阅拉取（清单本身很小）的超时。
const feedFetchTimeout = 60 * time.Second

// loadFeedCmd 拉取订阅、校验、展开成当前平台的条目，并标注本机已装版本。
func (m Model) loadFeedCmd(rawURL string) tea.Cmd {
	ctrl := m.ctrl
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), feedFetchTimeout)
		defer cancel()
		entries, err := ctrl.FeedEntries(ctx, rawURL)
		return feedLoadedMsg{url: rawURL, entries: entries, err: err}
	}
}

// installEntryCmd 下载并安装一个条目；进度通过 installCh 回传。
//
// 授权查表、覆盖前停掉旧进程、装完记信任、重建插件来源这一串都在控制层里；这里只负责
// 把下载进度接到界面的通道上。
func (m Model) installEntryCmd(rawURL string, e pluginfeed.Entry) tea.Cmd {
	ch := m.installCh
	ctrl := m.ctrl
	return func() tea.Msg {
		if ch != nil {
			defer func() { ch <- installProgress{done: progressDone} }()
		}
		res, err := ctrl.InstallPlugin(context.Background(), rawURL, e, func(done, total int64) {
			if ch == nil {
				return
			}
			select {
			case ch <- installProgress{done: done, total: total}:
			default: // 界面来不及消费时丢帧，不要拖慢下载
			}
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
	if m.ctrl == nil || !m.ctrl.FeedAvailable() {
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
	if e.Location.NeedsAuthorization() && !m.ctrl.HostAuthorized(e.Location.Host) {
		host := e.Location.Host
		entry := e
		m.confirm = &confirmBox{
			Title: "授权下载域名",
			Message: fmt.Sprintf("「%s」的安装包放在 %s，与订阅不同源。\n\n"+
				"信任该域名并继续安装？", entry.Plugin.Name, host),
			OnYes: func(mm *Model) tea.Cmd {
				if err := mm.ctrl.AuthorizeHost(host, url); err != nil {
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
	if sub, ok := m.subscription(m.feedFor); ok && !sub.EnabledValue() {
		// 停用的订阅依旧可以打开看（里面的插件还能装，只是不参与拉取），
		// 但得说清楚——否则从这里看不到外面那行的状态。
		b.WriteString("  " + m.theme.Dim().Render("（已停用：不参与拉取）"))
	}
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
	case "esc":
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
	m.clampFeedCursor()
	return m, nil
}

// clampFeedCursor 把订阅详情的选中行收回界内。
func (m *Model) clampFeedCursor() {
	if m.feedCursor >= len(m.feedEntries) {
		m.feedCursor = len(m.feedEntries) - 1
	}
	if m.feedCursor < 0 {
		m.feedCursor = 0
	}
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
