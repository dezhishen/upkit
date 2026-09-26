package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/manifest"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/util"
)

// tabID 是面板编号。
type tabID int

const (
	tabOverview tabID = iota
	tabDetail
	tabJobs
	tabLogs
	tabSettings
	tabSources
	tabCount
)

var tabTitles = []string{"概览", "详情", "任务", "日志", "设置", "来源"}

// Options 构造 TUI。
type Options struct {
	Engine   *engine.Engine
	Settings *settings.Settings
	Apps     *apps.File
	Logger   *logging.Manager
	Sink     *sink
	Version  string
	NoColor  bool
	ASCII    bool
	Borders  string // unicode | square | ascii
	// ConfigPath 是 settings.yaml 的实际路径（界面上展示）。
	ConfigPath string
	// Host 是插件宿主（可为 nil：插件子系统未启用）。
	Host *pluginhost.Manager
	// Feed 是订阅与授权记录（可为 nil：订阅不可用）。
	Feed *pluginfeed.Store
}

// jobItem 是任务面板的一项。
type jobItem struct {
	AppID   string
	Name    string
	State   string
	Phase   string
	Done    int64
	Total   int64
	Speed   float64
	Err     error
	Start   time.Time
	Elapsed time.Duration
}

// logEntry 是一条界面日志。
type logEntry struct {
	At    time.Time
	Level string
	App   string
	Msg   string
}

// confirmBox 是确认弹窗。OnYes 拿到模型指针，便于在确认后改动界面状态。
type confirmBox struct {
	Title   string
	Message string
	OnYes   func(*Model) tea.Cmd
}

// promptBox 是文本输入弹窗。
type promptBox struct {
	Title string
	Label string
	Buf   string
	Apply func(*Model, string) tea.Cmd
}

// Model 是 bubbletea 模型。
type Model struct {
	opts  Options
	theme Theme
	eng   *engine.Engine
	set   *settings.Settings
	afs   *apps.File
	log   *logging.Manager
	sink  *sink

	width, height int
	tab           tabID

	apps    []*engine.App
	cursor  int
	offset  int
	detailY int
	plan    *core.Plan

	jobs      []*jobItem
	jobCursor int
	jobOffset int

	logs      []logEntry
	logLevel  string
	logFilter string
	logView   viewport.Model
	logReady  bool
	logFollow bool

	setCursor int
	setDirty  bool

	// 来源面板：插件配置的编辑入口就在插件条目上。
	host      *pluginhost.Manager
	feed      *pluginfeed.Store
	srcCursor int
	cfgFor    string // 非空表示正在编辑该来源的配置
	cfgCursor int

	// 订阅详情：列出订阅里的插件，并安装/更新。
	feedFor     string // 非空表示正在查看该订阅
	feedEntries []pluginfeed.Entry
	feedCursor  int
	feedLoaded  bool
	feedBusy    bool
	feedErr     error
	feedProg    installProgress
	installCh   chan installProgress

	busy    bool
	spinner int
	status  string
	statusT time.Time
	fatal   error

	confirm *confirmBox
	prompt  *promptBox
	help    bool
}

// New 构造模型。
func New(opts Options) Model {
	m := Model{
		opts:      opts,
		host:      opts.Host,
		feed:      opts.Feed,
		theme:     NewTheme(opts.ASCII, opts.NoColor, opts.Borders),
		eng:       opts.Engine,
		set:       opts.Settings,
		afs:       opts.Apps,
		log:       opts.Logger,
		sink:      opts.Sink,
		logLevel:  "info",
		logFollow: true,
		status:    "按 ? 查看快捷键，c 检查更新",
		installCh: make(chan installProgress, 64),
	}
	if m.log != nil {
		for _, r := range m.log.Ring().Snapshot() {
			m.logs = append(m.logs, logEntry{At: r.At, Level: r.Level, App: r.App, Msg: r.Msg})
		}
	}
	return m
}

// ── 消息 ──────────────────────────────────────────────────────

type appsMsg struct {
	apps []*engine.App
	err  error
}

type appliedMsg struct {
	results []engine.JobResult
}

type planMsg struct {
	plan *core.Plan
	err  error
}

type noticeMsg struct {
	text string
	err  error
}

type statusExpiredMsg struct{}

// ── 生命周期 ──────────────────────────────────────────────────

// Init 启动时先做一次全量检查。
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.sink.waitEvent(), m.checkCmd(nil))
}

// Update 处理消息。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeLogView()
		return m, nil

	case eventMsg:
		m.handleEvent(core.Event(msg))
		return m, m.sink.waitEvent()

	case feedLoadedMsg:
		if msg.url != m.feedFor {
			return m, nil // 已经切走，丢弃
		}
		m.feedLoaded = true
		m.feedErr = msg.err
		m.feedEntries = msg.entries
		m.feedCursor = 0
		if msg.err != nil {
			m.setStatusErr(msg.err)
			return m, nil
		}
		pending := 0
		for _, e := range msg.entries {
			if a := e.Action(); a == pluginfeed.ActionInstall || a == pluginfeed.ActionUpdate {
				pending++
			}
		}
		m.setStatus(fmt.Sprintf("订阅内有 %d 个插件，%d 个可安装或更新", len(msg.entries), pending))
		return m, nil

	case installProgressMsg:
		if msg.done == progressDone {
			m.feedProg = installProgress{}
			return m, nil
		}
		m.feedProg = installProgress{done: msg.done, total: msg.total}
		return m, m.waitInstallProgress()

	case installDoneMsg:
		m.feedBusy = false
		m.feedProg = installProgress{}
		name := msg.entry.Plugin.Name
		if name == "" {
			name = msg.entry.Plugin.ID
		}
		if msg.err != nil {
			m.setStatusErr(msg.err)
			m.appendLog(logEntry{At: time.Now(), Level: "error", App: msg.entry.Plugin.ID, Msg: msg.err.Error()})
			return m, nil
		}
		m.appendLog(logEntry{At: time.Now(), Level: "info", App: msg.entry.Plugin.ID,
			Msg: "已从订阅安装 v" + msg.entry.Plugin.Version})
		// 新插件立刻生效；随后刷新订阅以反映最新状态。
		if m.host != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			m.host.Load(ctx)
			cancel()
		}
		m.setStatus(fmt.Sprintf("已安装 %s v%s", name, msg.entry.Plugin.Version))
		m.feedLoaded = false
		return m, m.loadFeedCmd(m.feedFor)

	case appsMsg:
		m.busy = false
		if msg.err != nil {
			m.setStatusErr(msg.err)
			return m, nil
		}
		m.apps = msg.apps
		m.clampCursor()
		pending := 0
		for _, a := range m.apps {
			if a.Action == core.ActionInstall || a.Action == core.ActionUpdate {
				pending++
			}
		}
		m.setStatus(fmt.Sprintf("共 %d 个软件，%d 个可更新", len(m.apps), pending))
		return m, nil

	case appliedMsg:
		m.busy = false
		ok, failed := 0, 0
		for _, r := range msg.results {
			if r.Err != nil {
				failed++
				m.appendLog(logEntry{At: time.Now(), Level: "error", App: r.AppID, Msg: r.Err.Error()})
			} else {
				ok++
			}
		}
		if failed == 0 {
			m.setStatus(fmt.Sprintf("完成 %d 个任务", ok))
		} else {
			m.setStatusErr(fmt.Errorf("完成 %d 个，失败 %d 个（见任务/日志面板）", ok, failed))
		}
		return m, m.checkCmd(nil)

	case planMsg:
		m.busy = false
		if msg.err != nil {
			m.setStatusErr(msg.err)
			return m, nil
		}
		m.plan = msg.plan
		m.detailY = 0
		m.tab = tabDetail
		return m, nil

	case noticeMsg:
		m.busy = false
		if msg.err != nil {
			m.setStatusErr(msg.err)
			return m, nil
		}
		m.setStatus(msg.text)
		return m, m.checkCmd(nil)

	case statusExpiredMsg:
		if time.Since(m.statusT) > 6*time.Second {
			m.status, m.fatal = "", nil
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// ── 事件与日志 ────────────────────────────────────────────────

func (m *Model) handleEvent(e core.Event) {
	if e.AppID != "" {
		j := m.jobFor(e.AppID)
		switch e.Kind {
		case core.EventStarted:
			j.State = "进行中"
			j.Phase = e.Phase
			j.Start = time.Now()
			m.tab = tabJobs
		case core.EventPhase:
			j.State = "进行中"
			j.Phase = e.Phase
		case core.EventProgress:
			j.Done, j.Total, j.Speed = e.Done, e.Total, e.Speed
		case core.EventBlocked:
			j.State = "等待"
			j.Phase = "进程占用"
		case core.EventFinished:
			j.State = "完成"
			j.Phase = ""
			j.Elapsed = time.Since(j.Start)
		case core.EventFailed:
			j.State = "失败"
			j.Err = e.Err
			m.setStatusErr(e.Err)
		}
	}
	if e.Msg != "" {
		lvl := string(e.Level)
		if lvl == "" {
			lvl = "info"
		}
		m.appendLog(logEntry{At: time.Now(), Level: lvl, App: e.AppID, Msg: e.Msg})
	}
}

func (m *Model) jobFor(id string) *jobItem {
	for _, j := range m.jobs {
		if j.AppID == id && (j.State == "进行中" || j.State == "等待" || j.State == "排队") {
			return j
		}
	}
	name := id
	if a := m.eng.Find(id); a != nil {
		name = a.Ref.DisplayName()
	}
	j := &jobItem{AppID: id, Name: name, State: "排队", Start: time.Now()}
	m.jobs = append(m.jobs, j)
	return j
}

func (m *Model) appendLog(e logEntry) {
	m.logs = append(m.logs, e)
	if len(m.logs) > 2000 {
		m.logs = m.logs[len(m.logs)-2000:]
	}
	if m.logReady && m.logFollow {
		m.refreshLogView()
	}
}

func (m *Model) refreshLogView() {
	lines := make([]string, 0, len(m.logs))
	for _, l := range m.logs {
		if !m.logVisible(l) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %-5s %-10s %s",
			l.At.Format("15:04:05"), strings.ToUpper(l.Level), util.Truncate(l.App, 10), l.Msg))
	}
	m.logView.SetContent(strings.Join(lines, "\n"))
	if m.logFollow {
		m.logView.GotoBottom()
	}
}

func (m Model) logVisible(l logEntry) bool {
	if m.logFilter != "" && !strings.Contains(strings.ToLower(l.Msg+" "+l.App), strings.ToLower(m.logFilter)) {
		return false
	}
	rank := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
	return rank[l.Level] >= rank[m.logLevel]
}

func (m *Model) resizeLogView() {
	w, h := m.width-6, m.height-8
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	if !m.logReady {
		m.logView = viewport.New(w, h)
		m.logReady = true
	} else {
		m.logView.Width, m.logView.Height = w, h
	}
	m.refreshLogView()
}

// ── 状态栏 ────────────────────────────────────────────────────

func (m *Model) setStatus(text string) {
	m.status, m.fatal, m.statusT = text, nil, time.Now()
}

func (m *Model) setStatusErr(err error) {
	if err == nil {
		return
	}
	m.fatal, m.statusT = err, time.Now()
}

// ── 命令 ──────────────────────────────────────────────────────

func (m Model) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

func (m Model) checkCmd(ids []string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if len(ids) == 0 {
			list, err := eng.Check(ctx)
			return appsMsg{apps: list, err: err}
		}
		for _, id := range ids {
			if _, err := eng.CheckOne(ctx, id); err != nil {
				return appsMsg{err: err}
			}
		}
		return appsMsg{apps: eng.Apps()}
	}
}

func (m Model) applyCmd(ids []string) tea.Cmd {
	if len(ids) == 0 {
		return nil
	}
	eng := m.eng
	conc := m.set.Engine.ApplyConcurrency
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()
		return appliedMsg{results: eng.ApplyMany(ctx, ids, conc)}
	}
}

func (m Model) planCmd(id string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p, err := eng.Plan(ctx, id)
		return planMsg{plan: p, err: err}
	}
}

func (m Model) uninstallCmd(id string, keep bool) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := eng.Uninstall(ctx, id, keep); err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{text: "已卸载 " + id}
	}
}

func (m Model) rollbackCmd(id string) tea.Cmd {
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := eng.Rollback(ctx, id, ""); err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{text: "已回滚 " + id}
	}
}

func (m Model) exportCmd() tea.Cmd {
	eng, afs, set := m.eng, m.afs, m.set
	return func() tea.Msg {
		installed := map[string]string{}
		for _, a := range eng.Apps() {
			if a.Status.Version != "" {
				installed[a.Ref.ID] = a.Status.Version
			}
		}
		path, err := manifest.Export("", set, afs, installed)
		if err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{text: "清单已导出到 " + path}
	}
}

func (m Model) importCmd(path string) tea.Cmd {
	eng, afs := m.eng, m.afs
	return func() tea.Msg {
		f, err := manifest.Load(path)
		if err != nil {
			return noticeMsg{err: err}
		}
		added, updated := f.MergeApps(afs)
		if err := afs.Save(); err != nil {
			return noticeMsg{err: err}
		}
		if _, err := eng.List(context.Background()); err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{text: fmt.Sprintf("已导入：新增 %d 个，更新 %d 个", len(added), len(updated))}
	}
}

// ── 快捷键 ────────────────────────────────────────────────────

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// 弹窗优先
	if m.prompt != nil {
		return m.handlePromptKey(key)
	}
	if m.confirm != nil {
		switch key {
		case "y", "Y", "enter":
			action := m.confirm.OnYes
			m.confirm = nil
			var cmd tea.Cmd
			if action != nil {
				cmd = action(&m)
			}
			return m, cmd
		case "n", "N", "esc", "q":
			m.confirm = nil
			m.setStatus("已取消")
			return m, nil
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}

	// 全局键
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		m.tab = tabID(int(key[0] - '1'))
		return m, nil
	case "tab":
		m.tab = (m.tab + 1) % tabCount
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + tabCount - 1) % tabCount
		return m, nil
	case "q":
		if m.busy {
			m.confirm = &confirmBox{Title: "仍在执行任务", Message: "有任务正在运行，确定退出？",
				OnYes: func(*Model) tea.Cmd { return tea.Quit }}
			return m, nil
		}
		return m, tea.Quit
	}

	switch m.tab {
	case tabOverview:
		return m.updateOverview(key)
	case tabDetail:
		return m.updateDetail(key)
	case tabJobs:
		return m.updateJobs(key)
	case tabLogs:
		return m.updateLogs(key, msg)
	case tabSettings:
		return m.updateSettings(key)
	case tabSources:
		return m.updateSources(key)
	}
	return m, nil
}

func (m Model) updateOverview(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		m.cursor++
		m.clampCursor()
	case "k", "up":
		m.cursor--
		m.clampCursor()
	case "g", "home":
		m.cursor, m.offset = 0, 0
	case "G", "end":
		m.cursor = len(m.apps) - 1
		m.clampCursor()
	case "c":
		if a := m.current(); a != nil {
			m.busy = true
			m.setStatus("正在检查 " + a.Ref.DisplayName())
			return m, m.checkCmd([]string{a.Ref.ID})
		}
	case "C":
		m.busy = true
		m.setStatus("正在检查全部软件…")
		return m, m.checkCmd(nil)
	case "u":
		if a := m.current(); a != nil {
			return m.applyOne(a)
		}
	case "U":
		ids := m.pendingIDs()
		if len(ids) == 0 {
			m.setStatus("没有需要更新的软件")
			return m, nil
		}
		m.confirm = &confirmBox{
			Title:   "批量更新",
			Message: fmt.Sprintf("将依次更新 %d 个软件，是否继续？", len(ids)),
			OnYes:   func(mm *Model) tea.Cmd { mm.busy = true; return mm.applyCmd(ids) },
		}
		return m, nil
	case "p", "enter":
		if a := m.current(); a != nil {
			m.busy = true
			return m, m.planCmd(a.Ref.ID)
		}
	case " ", "space":
		a := m.current()
		if a == nil {
			return m, nil
		}
		if err := m.afs.SetEnabled(a.Ref.ID, !appEnabled(a, m.afs)); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		if err := m.afs.Save(); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		m.setStatus("已更新启用状态")
		return m, m.checkCmd(nil)
	case "x":
		if a := m.current(); a != nil {
			id, name := a.Ref.ID, a.Ref.DisplayName()
			m.confirm = &confirmBox{
				Title:   "卸载 " + name,
				Message: "将删除安装目录（保留用户数据）。确定继续？",
				OnYes:   func(mm *Model) tea.Cmd { mm.busy = true; return mm.uninstallCmd(id, true) },
			}
		}
	case "r":
		if a := m.current(); a != nil {
			id, name := a.Ref.ID, a.Ref.DisplayName()
			m.confirm = &confirmBox{
				Title:   "回滚 " + name,
				Message: "将恢复到最近一次备份，确定继续？",
				OnYes:   func(mm *Model) tea.Cmd { mm.busy = true; return mm.rollbackCmd(id) },
			}
		}
	case "E":
		m.busy = true
		return m, m.exportCmd()
	case "I":
		path := m.set.ManifestPath()
		m.prompt = &promptBox{
			Title: "导入清单",
			Label: "文件路径",
			Buf:   path,
			Apply: func(mm *Model, v string) tea.Cmd { mm.busy = true; return mm.importCmd(v) },
		}
	}
	return m, nil
}

func (m Model) applyOne(a *engine.App) (tea.Model, tea.Cmd) {
	if a.Shadowed {
		m.setStatusErr(fmt.Errorf("已被其它条目取代，不会更新：%s", a.Note))
		return m, nil
	}
	switch a.Action {
	case core.ActionNoOp:
		m.setStatus(a.Ref.DisplayName() + " 已是最新版本")
		return m, nil
	case core.ActionInstall, core.ActionUpdate, core.ActionReinstall:
		id, name := a.Ref.ID, a.Ref.DisplayName()
		m.confirm = &confirmBox{
			Title:   "执行 " + name,
			Message: a.Note + "\n\n是否继续？",
			OnYes:   func(mm *Model) tea.Cmd { mm.busy = true; return mm.applyCmd([]string{id}) },
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateDetail(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		m.detailY++
	case "k", "up":
		if m.detailY > 0 {
			m.detailY--
		}
	case "g", "home":
		m.detailY = 0
	case "p":
		if a := m.current(); a != nil {
			m.busy = true
			return m, m.planCmd(a.Ref.ID)
		}
	case "u":
		if a := m.current(); a != nil {
			return m.applyOne(a)
		}
	case "esc":
		m.tab = tabOverview
	}
	return m, nil
}

func (m Model) updateJobs(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		m.jobCursor++
		m.clampJobCursor()
	case "k", "up":
		m.jobCursor--
		m.clampJobCursor()
	case "d":
		kept := m.jobs[:0]
		for _, j := range m.jobs {
			if j.State == "完成" || j.State == "失败" {
				continue
			}
			kept = append(kept, j)
		}
		m.jobs = kept
		m.clampJobCursor()
		m.setStatus("已清除已完成任务")
	}
	return m, nil
}

func (m Model) updateLogs(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "f":
		order := []string{"debug", "info", "warn", "error"}
		for i, l := range order {
			if l == m.logLevel {
				m.logLevel = order[(i+1)%len(order)]
				break
			}
		}
		m.refreshLogView()
		m.setStatus("日志级别过滤：" + m.logLevel)
		return m, nil
	case "F":
		m.prompt = &promptBox{Title: "日志关键字", Label: "关键字（留空取消）", Buf: m.logFilter,
			Apply: func(mm *Model, v string) tea.Cmd { mm.logFilter = v; mm.refreshLogView(); return nil }}
		return m, nil
	case "g":
		m.logFollow = false
	case "G":
		m.logFollow = true
		m.refreshLogView()
		return m, nil
	case "j", "k", "up", "down", "pgup", "pgdown", "home", "end":
		m.logFollow = false
	}
	var cmd tea.Cmd
	m.logView, cmd = m.logView.Update(msg)
	return m, cmd
}

func (m Model) handlePromptKey(key string) (tea.Model, tea.Cmd) {
	p := m.prompt
	switch key {
	case "esc":
		m.prompt = nil
		m.setStatus("已取消")
		return m, nil
	case "enter":
		action := p.Apply
		value := p.Buf
		m.prompt = nil
		var cmd tea.Cmd
		if action != nil {
			cmd = action(&m, value)
		}
		return m, cmd
	case "backspace":
		if len(p.Buf) > 0 {
			p.Buf = p.Buf[:len(p.Buf)-1]
		}
		return m, nil
	}
	if len(key) == 1 && key >= " " {
		p.Buf += key
	}
	return m, nil
}

// ── 工具 ──────────────────────────────────────────────────────

func (m Model) current() *engine.App {
	if m.cursor < 0 || m.cursor >= len(m.apps) {
		return nil
	}
	return m.apps[m.cursor]
}

func (m *Model) clampCursor() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.apps) {
		m.cursor = len(m.apps) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	visible := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if visible > 0 && m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
}

func (m *Model) clampJobCursor() {
	if m.jobCursor < 0 {
		m.jobCursor = 0
	}
	if m.jobCursor >= len(m.jobs) {
		m.jobCursor = len(m.jobs) - 1
	}
	if m.jobCursor < 0 {
		m.jobCursor = 0
	}
}

func (m Model) listHeight() int {
	h := m.height - 10
	if h < 3 {
		h = 3
	}
	return h
}

func (m Model) pendingIDs() []string {
	var ids []string
	for _, a := range m.apps {
		if a.Shadowed {
			continue
		}
		if a.Action == core.ActionInstall || a.Action == core.ActionUpdate {
			ids = append(ids, a.Ref.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m Model) spinnerText() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if m.theme.ASCII {
		frames = []string{"|", "/", "-", "\\"}
	}
	return frames[m.spinner%len(frames)]
}

// enabled 返回条目的启用状态。
func appEnabled(a *engine.App, afs *apps.File) bool {
	if a == nil || afs == nil {
		return true
	}
	return afs.Enabled(a.Ref.ID)
}

// Version 返回界面版本号。
func (m Model) Version() string {
	if m.opts.Version == "" {
		return "dev"
	}
	return m.opts.Version
}
