package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/util"
)

// settingKind 是设置项类型。
type settingKind int

const (
	kindBool settingKind = iota
	kindInt
	kindEnum
	kindText
	kindInfo
)

// settingField 描述一个可编辑项。
type settingField struct {
	Label string
	Kind  settingKind
	Value func() string
	Delta func(int)
	Cycle func(int) // 布尔切换 / 枚举循环
	Text  func() *promptBox
	Opts  []string
}

// maskSecret 只表明「是否已设置」，不回显内容。
func maskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return "********"
}

// splitList 把逗号/分号/换行分隔的输入切成字段。
func splitList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fields 返回全部可编辑项。
func (m Model) fields() []settingField {
	s := m.set
	return []settingField{
		{Label: "网络代理", Kind: kindText, Value: func() string { return orDash(s.Network.Proxy) },
			Text: func() *promptBox {
				return &promptBox{Title: "网络代理", Label: "http:// 或 socks5://（留空表示不使用）", Buf: s.Network.Proxy,
					Apply: func(mm *Model, v string) tea.Cmd {
						mm.set.Network.Proxy = strings.TrimSpace(v)
						mm.setDirty = true
						return nil
					}}
			}},
		{Label: "请求超时（秒）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Network.TimeoutSeconds) },
			Delta: func(d int) {
				s.Network.TimeoutSeconds = clampInt(s.Network.TimeoutSeconds+d*10, 10, 600)
				m.setDirty = true
			}},
		{Label: "失败重试次数", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Network.Retries) },
			Delta: func(d int) { s.Network.Retries = clampInt(s.Network.Retries+d, 0, 10); m.setDirty = true }},
		{Label: "下载限速（KB/s，0=不限）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Network.RateLimitKBps) },
			Delta: func(d int) {
				s.Network.RateLimitKBps = clampInt(s.Network.RateLimitKBps+d*256, 0, 256*1024)
				m.setDirty = true
			}},
		{Label: "GitHub 令牌", Kind: kindText, Value: func() string { return maskSecret(s.Network.GitHubToken) },
			Text: func() *promptBox {
				return &promptBox{Title: "GitHub 令牌", Label: "留空表示不使用；也可写 env:VAR 引用环境变量", Buf: s.Network.GitHubToken,
					Apply: func(mm *Model, v string) tea.Cmd {
						mm.set.Network.GitHubToken = strings.TrimSpace(v)
						mm.setDirty = true
						return nil
					}}
			}},
		{Label: "下载并发", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Engine.DownloadConcurrency) },
			Delta: func(d int) {
				s.Engine.DownloadConcurrency = clampInt(s.Engine.DownloadConcurrency+d, 1, 8)
				m.setDirty = true
			}},
		{Label: "安装并发", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Engine.ApplyConcurrency) },
			Delta: func(d int) {
				s.Engine.ApplyConcurrency = clampInt(s.Engine.ApplyConcurrency+d, 1, 16)
				m.setDirty = true
			}},
		{Label: "缓存保留份数", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Storage.CacheKeep) },
			Delta: func(d int) { s.Storage.CacheKeep = clampInt(s.Storage.CacheKeep+d, 0, 50); m.setDirty = true }},
		{Label: "备份保留份数", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Storage.BackupKeep) },
			Delta: func(d int) { s.Storage.BackupKeep = clampInt(s.Storage.BackupKeep+d, 0, 20); m.setDirty = true }},
		{Label: "存储总配额（MB，0=不限）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Storage.BudgetMB) },
			Delta: func(d int) { s.Storage.BudgetMB = clampInt(s.Storage.BudgetMB+d*1024, 0, 1024*1024); m.setDirty = true }},
		{Label: "最低磁盘余量（MB）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Storage.MinFreeSpaceMB) },
			Delta: func(d int) {
				s.Storage.MinFreeSpaceMB = clampInt(s.Storage.MinFreeSpaceMB+d*512, 0, 512*1024)
				m.setDirty = true
			}},
		{Label: "执行前确认", Kind: kindBool, Value: func() string { return yesNo(s.Behavior.ConfirmBeforeApply) },
			Cycle: func(int) { s.Behavior.ConfirmBeforeApply = !s.Behavior.ConfirmBeforeApply; m.setDirty = true }},
		{Label: "等待关闭占用进程", Kind: kindBool, Value: func() string { return yesNo(s.Behavior.WaitForClose) },
			Cycle: func(int) { s.Behavior.WaitForClose = !s.Behavior.WaitForClose; m.setDirty = true }},
		{Label: "更新后自动启动", Kind: kindBool, Value: func() string { return yesNo(s.Behavior.LaunchAfterUpdate) },
			Cycle: func(int) { s.Behavior.LaunchAfterUpdate = !s.Behavior.LaunchAfterUpdate; m.setDirty = true }},
		{Label: "结束占用进程方式", Kind: kindEnum, Opts: []string{"graceful", "force"},
			Value: func() string { return s.Behavior.StopStrategy },
			Cycle: func(d int) {
				s.Behavior.StopStrategy = cycle(s.Behavior.StopStrategy, []string{"graceful", "force"}, d)
				m.setDirty = true
			}},
		{Label: "日志级别", Kind: kindEnum, Opts: []string{"debug", "info", "warn", "error"},
			Value: func() string { return s.Logs.Level },
			Cycle: func(d int) {
				s.Logs.Level = cycle(s.Logs.Level, []string{"debug", "info", "warn", "error"}, d)
				m.setDirty = true
			}},
		{Label: "审计日志", Kind: kindBool, Value: func() string { return yesNo(s.Logs.Audit) },
			Cycle: func(int) { s.Logs.Audit = !s.Logs.Audit; m.setDirty = true }},
		{Label: "日志保留份数", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Logs.MaxFiles) },
			Delta: func(d int) { s.Logs.MaxFiles = clampInt(s.Logs.MaxFiles+d, 1, 200); m.setDirty = true }},
		{Label: "日志保留天数", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Logs.MaxAgeDays) },
			Delta: func(d int) { s.Logs.MaxAgeDays = clampInt(s.Logs.MaxAgeDays+d, 1, 3650); m.setDirty = true }},
		{Label: "压缩旧日志", Kind: kindBool, Value: func() string { return yesNo(s.Logs.Compress) },
			Cycle: func(int) { s.Logs.Compress = !s.Logs.Compress; m.setDirty = true }},
		{Label: "单个日志上限（MB）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Logs.MaxSizeMB) },
			Delta: func(d int) { s.Logs.MaxSizeMB = clampInt(s.Logs.MaxSizeMB+d*4, 1, 512); m.setDirty = true }},
		{Label: "日志总配额（MB，0=不限）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.Logs.MaxTotalMB) },
			Delta: func(d int) {
				s.Logs.MaxTotalMB = clampInt(s.Logs.MaxTotalMB+d*64, 0, 64*1024)
				m.setDirty = true
			}},
		{Label: "日志脱敏", Kind: kindBool, Value: func() string { return yesNo(s.Logs.Redact) },
			Cycle: func(int) { s.Logs.Redact = !s.Logs.Redact; m.setDirty = true }},
		{Label: "自动加载已授权插件", Kind: kindBool, Value: func() string { return yesNo(s.Plugins.AutoLoadTrusted) },
			Cycle: func(int) { s.Plugins.AutoLoadTrusted = !s.Plugins.AutoLoadTrusted; m.setDirty = true }},
		{Label: "要求插件签名", Kind: kindBool, Value: func() string { return yesNo(s.Plugins.RequireSignature) },
			Cycle: func(int) { s.Plugins.RequireSignature = !s.Plugins.RequireSignature; m.setDirty = true }},
		{Label: "插件域名白名单", Kind: kindText, Value: func() string { return orDash(strings.Join(s.Plugins.Allowlist, ", ")) },
			Text: func() *promptBox {
				return &promptBox{Title: "插件域名白名单", Label: "逗号分隔；留空表示不限制（跨域下载仍需逐次授权）", Buf: strings.Join(s.Plugins.Allowlist, ", "),
					Apply: func(mm *Model, v string) tea.Cmd {
						mm.set.Plugins.Allowlist = splitList(v)
						mm.setDirty = true
						return nil
					}}
			}},
		{Label: "界面主题", Kind: kindEnum, Opts: []string{"auto", "dark", "light"},
			Value: func() string { return s.UI.Theme },
			Cycle: func(d int) { s.UI.Theme = cycle(s.UI.Theme, []string{"auto", "dark", "light"}, d); m.setDirty = true }},
		{Label: "边框样式", Kind: kindEnum, Opts: []string{"unicode", "square", "ascii"},
			Value: func() string { return s.UI.Borders },
			Cycle: func(d int) {
				s.UI.Borders = cycle(s.UI.Borders, []string{"unicode", "square", "ascii"}, d)
				m.setDirty = true
			}},
		{Label: "刷新间隔（毫秒）", Kind: kindInt, Value: func() string { return fmt.Sprint(s.UI.RefreshMS) },
			Delta: func(d int) { s.UI.RefreshMS = clampInt(s.UI.RefreshMS+d*100, 100, 5000); m.setDirty = true }},
	}
}

// paths 返回只读的路径信息（便携布局：全部在 upkit 同级目录下）。
func (m Model) paths() [][2]string {
	s := m.set
	cfg := m.opts.ConfigPath
	if cfg == "" {
		cfg = s.Path
	}
	root := s.RootDir()
	return [][2]string{
		{"根目录", root},
		{"设置文件", cfg},
		{"软件清单", s.AppsPath()},
		{"清单导出", s.ManifestPath()},
		{"日志目录", s.Logs.Dir},
		{"插件目录", s.Plugins.Dir},
		{"数据目录", s.Storage.DataDir},
		{"缓存目录", s.Storage.CacheDir},
		{"备份目录", s.Storage.BackupDir},
		{"临时目录", s.Storage.TempDir},
	}
}

func (m Model) updateSettings(key string) (tea.Model, tea.Cmd) {
	fields := m.fields()
	switch key {
	case "j", "down":
		m.setCursor++
	case "k", "up":
		m.setCursor--
	case "g", "home":
		m.setCursor = 0
	case "G", "end":
		m.setCursor = len(fields) - 1
	case "left", "h":
		if m.applyFieldDelta(fields, -1) {
			m.setDirty = true
		}
	case "right", "l":
		if m.applyFieldDelta(fields, 1) {
			m.setDirty = true
		}
	case " ", "enter":
		if m.setCursor >= 0 && m.setCursor < len(fields) {
			f := fields[m.setCursor]
			switch f.Kind {
			case kindBool, kindEnum:
				if f.Cycle != nil {
					f.Cycle(1)
					m.setDirty = true
				}
			case kindText:
				if f.Text != nil {
					m.prompt = f.Text()
				}
			}
		}
	case "s":
		if !m.setDirty {
			m.setStatus("没有需要保存的修改")
			return m, nil
		}
		if err := m.set.Save(); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		m.setDirty = false
		m.setStatus("已保存到 " + m.set.Path + "（部分项重启后生效）")
		return m, nil
	case "R":
		m.confirm = &confirmBox{Title: "恢复默认设置", Message: "将把设置恢复为内置默认值（不会删除软件清单）。",
			OnYes: func(mm *Model) tea.Cmd {
				def := settings.Default()
				def.Path = mm.set.Path
				if err := def.Normalize(); err == nil {
					*mm.set = *def
					mm.setDirty = true
					mm.setStatus("已恢复默认值，按 s 保存")
				}
				return nil
			}}
		return m, nil
	}
	if m.setCursor < 0 {
		m.setCursor = 0
	}
	if m.setCursor >= len(fields) {
		m.setCursor = len(fields) - 1
	}
	return m, nil
}

// applyFieldDelta 按方向调整当前项，返回是否有改动。
func (m Model) applyFieldDelta(fields []settingField, d int) bool {
	if m.setCursor < 0 || m.setCursor >= len(fields) {
		return false
	}
	f := fields[m.setCursor]
	switch f.Kind {
	case kindInt:
		if f.Delta != nil {
			f.Delta(d)
			return true
		}
	case kindEnum, kindBool:
		if f.Cycle != nil {
			f.Cycle(d)
			return true
		}
	}
	return false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func cycle(cur string, opts []string, d int) string {
	idx := 0
	for i, o := range opts {
		if strings.EqualFold(o, cur) {
			idx = i
			break
		}
	}
	idx = (idx + d + len(opts)) % len(opts)
	return opts[idx]
}

func yesNo(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func humanSize(n int64) string {
	if n <= 0 {
		return "—"
	}
	return util.HumanBytes(n)
}

var _ = util.Truncate
