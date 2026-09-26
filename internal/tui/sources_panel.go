package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
)

// sourceRow 是「来源」面板的一行：一个插件来源，或一条订阅。
type sourceRow struct {
	isSubscription bool

	spec    apps.SourceSpec
	name    string
	state   pluginhost.State
	detail  string
	version string
	apps    int

	sub pluginfeed.Subscription
}

// configRow 是插件配置子视图里的一行。
type configRow struct {
	Key      string
	Label    string
	Value    string
	Default  string
	Help     string
	Required bool
	Secret   bool
}

// sourceRows 汇总来源列表：插件来源在前，订阅在后。
func (m Model) sourceRows() []sourceRow {
	rows := make([]sourceRow, 0, len(m.afs.Sources)+4)

	live := map[string]pluginhost.SourceStatus{}
	if m.host != nil {
		for _, st := range m.host.Sources() {
			live[st.ID] = st
		}
	}
	for _, spec := range m.afs.Sources {
		row := sourceRow{spec: spec, name: spec.Name}
		switch {
		case !spec.EnabledValue():
			row.state, row.detail = pluginhost.StateDisabled, "已在清单中停用"
		default:
			if st, ok := live[spec.ID]; ok {
				row.state, row.detail = st.State, st.Detail
				row.version, row.apps = st.Version, st.Apps
				// 清单里的名字优先，否则用插件自报的名字。
				row.name = firstNonEmptyStr(spec.Name, st.Name)
			} else {
				row.state, row.detail = pluginhost.StateDisabled, "宿主未加载"
			}
		}
		rows = append(rows, row)
	}

	if m.feed != nil {
		for _, sub := range m.feed.Subscriptions() {
			rows = append(rows, sourceRow{isSubscription: true, sub: sub})
		}
	}
	return rows
}

// configSource 返回正在编辑配置的那个来源。
func (m Model) configSource() (apps.SourceSpec, bool) {
	for _, spec := range m.afs.Sources {
		if spec.ID == m.cfgFor {
			return spec, true
		}
	}
	return apps.SourceSpec{}, false
}

// configRows 返回插件声明的配置项及其当前值。
func (m Model) configRows(spec apps.SourceSpec) []configRow {
	if m.host == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema, err := m.host.ConfigSchema(ctx, spec.ID)
	if err != nil {
		return nil
	}
	out := make([]configRow, 0, len(schema.Fields))
	for _, f := range schema.Fields {
		row := configRow{
			Key:      f.Key,
			Label:    firstNonEmptyStr(f.Label, f.Key),
			Default:  f.Default,
			Help:     f.Help,
			Required: f.Required,
			Secret:   f.Secret,
		}
		if v, ok := spec.Config[f.Key]; ok {
			row.Value = v
		} else {
			row.Value = f.Default
		}
		out = append(out, row)
	}
	return out
}

// ── 渲染 ──────────────────────────────────────────────────────

func (m Model) viewSources(w, height int) string {
	if m.feedFor != "" {
		return m.viewFeedDetail(w, height)
	}
	if m.cfgFor != "" {
		return m.viewPluginConfig(w, height)
	}

	rows := m.sourceRows()
	if len(rows) == 0 {
		return m.theme.Panel("来源",
			"还没有任何插件来源。\n\n"+
				"· 加订阅：按 a 填订阅地址，upkit 会自动下载并安装插件（需逐级授权）\n"+
				"· 手工安装：把插件可执行文件与 <id>.plugin.yaml 放进 plugin/ 目录\n"+
				"· 订阅支持 .json / .yaml / .yml 三种格式",
			w, height, true)
	}

	var b strings.Builder
	for i, r := range rows {
		if r.isSubscription && (i == 0 || !rows[i-1].isSubscription) {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(m.theme.Primary().Render("订阅"))
			b.WriteString("\n")
		}
		if !r.isSubscription && i == 0 {
			b.WriteString(m.theme.Primary().Render("插件来源"))
			b.WriteString("\n")
		}
		b.WriteString(m.sourceLine(i, r))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.theme.Dim().Render("enter/c 进入（订阅看可装/可更新，插件改配置）   o 官方源   a 添加订阅   d 删除   space 启用/停用   r 重载"))
	return m.theme.Panel("来源", strings.TrimRight(b.String(), "\n"), w, height, true)
}

func (m Model) sourceLine(i int, r sourceRow) string {
	cur := m.theme.Cursor(i == m.srcCursor)
	if r.isSubscription {
		state, style := "已记录", m.theme.Dim()
		if r.sub.LastError != "" {
			state, style = "上次拉取失败", m.theme.Warn()
		}
		return fmt.Sprintf("%s%s %s  %s", cur, m.theme.Dim().Render("*"),
			m.theme.Primary().Render(r.sub.URL), style.Render(state))
	}

	name := r.name
	if name == "" {
		name = r.spec.ID
	}
	detail := m.stateText(r)
	return fmt.Sprintf("%s%s %s  %s  %s", cur, m.sourceMarker(r.state),
		m.theme.Primary().Render(name), m.theme.Dim().Render(orDash(r.version)), detail)
}

func (m Model) sourceMarker(st pluginhost.State) string {
	switch st {
	case pluginhost.StateOK:
		return m.theme.OK().Render("●")
	case pluginhost.StateError:
		return m.theme.Err().Render("!")
	case pluginhost.StateUntrusted:
		return m.theme.Warn().Render("?")
	case pluginhost.StateMissing:
		return m.theme.Warn().Render("-")
	default:
		return m.theme.Dim().Render("○")
	}
}

func (m Model) stateText(r sourceRow) string {
	switch r.state {
	case pluginhost.StateOK:
		return m.theme.OK().Render(fmt.Sprintf("正常 · %d 个软件", r.apps))
	case pluginhost.StateUntrusted:
		return m.theme.Warn().Render("未信任 · 需在清单里写入 sha256")
	case pluginhost.StateMissing:
		return m.theme.Warn().Render("缺少文件")
	case pluginhost.StateError:
		return m.theme.Err().Render("错误：" + firstNonEmptyStr(r.detail, "启动失败"))
	default:
		return m.theme.Dim().Render(firstNonEmptyStr(r.detail, "未启用"))
	}
}

// viewPluginConfig 渲染某个插件的配置项（入口就在插件条目上）。
func (m Model) viewPluginConfig(w, height int) string {
	spec, ok := m.configSource()
	if !ok {
		m.cfgFor = ""
		return m.theme.Panel("插件配置", "该来源已不存在。", w, height, true)
	}
	rows := m.configRows(spec)
	if len(rows) == 0 {
		return m.theme.Panel("插件配置",
			fmt.Sprintf("%s 没有声明可配置项。\n\n插件可以用 ConfigSchema 声明配置字段，界面会据此生成表单。\n\n按 esc 返回。", spec.ID),
			w, height, true)
	}

	var b strings.Builder
	b.WriteString(m.theme.Dim().Render("插件："))
	b.WriteString(m.theme.Primary().Render(spec.ID))
	b.WriteString("\n\n")

	for i, r := range rows {
		cur := m.theme.Cursor(i == m.cfgCursor)
		val := r.Value
		if r.Secret && val != "" {
			val = "********"
		}
		if val == "" {
			val = m.theme.Dim().Render("（未设置）")
		}
		label := r.Label
		if r.Required {
			label += " *"
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s\n", cur, label, val))
		if i == m.cfgCursor && r.Help != "" {
			b.WriteString("  " + m.theme.Dim().Render(r.Help) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(m.theme.Dim().Render("enter 编辑   D 恢复默认   esc 返回"))
	return m.theme.Panel("插件配置", strings.TrimRight(b.String(), "\n"), w, height, true)
}

// ── 按键 ──────────────────────────────────────────────────────

func (m Model) updateSources(key string) (tea.Model, tea.Cmd) {
	if m.feedFor != "" {
		return m.updateFeedDetail(key)
	}
	if m.cfgFor != "" {
		return m.updatePluginConfig(key)
	}

	rows := m.sourceRows()
	switch key {
	case "j", "down":
		m.srcCursor++
	case "k", "up":
		m.srcCursor--
	case "g", "home":
		m.srcCursor = 0
	case "G", "end":
		m.srcCursor = len(rows) - 1
	case "enter", "c", "l":
		if m.srcCursor < 0 || m.srcCursor >= len(rows) {
			break
		}
		if rows[m.srcCursor].isSubscription {
			// 订阅：拉取并列出其中的插件，可直接安装/更新。
			m.feedFor = rows[m.srcCursor].sub.URL
			m.feedEntries = nil
			m.feedCursor = 0
			m.feedLoaded = false
			m.feedErr = nil
			return m, m.loadFeedCmd(m.feedFor)
		}
		m.cfgFor = rows[m.srcCursor].spec.ID
		m.cfgCursor = 0
	case "a":
		return m.addSubscription()
	case "o":
		return m.addBuiltinSubscription()
	case "d":
		return m.removeSubscription(rows)
	case " ", "space":
		return m.toggleSource(rows)
	case "r":
		return m.reloadSources()
	}
	if m.srcCursor < 0 {
		m.srcCursor = 0
	}
	if m.srcCursor >= len(rows) {
		m.srcCursor = len(rows) - 1
	}
	return m, nil
}

func (m Model) updatePluginConfig(key string) (tea.Model, tea.Cmd) {
	spec, ok := m.configSource()
	if !ok {
		m.cfgFor = ""
		return m, nil
	}
	rows := m.configRows(spec)
	switch key {
	case "esc", "q", "h", "left", "backspace":
		m.cfgFor = ""
		return m, nil
	case "j", "down":
		m.cfgCursor++
	case "k", "up":
		m.cfgCursor--
	case "g", "home":
		m.cfgCursor = 0
	case "G", "end":
		m.cfgCursor = len(rows) - 1
	case "enter", "l":
		if m.cfgCursor >= 0 && m.cfgCursor < len(rows) {
			return m.editConfigField(spec, rows[m.cfgCursor])
		}
	case "D":
		if m.cfgCursor >= 0 && m.cfgCursor < len(rows) {
			return m.resetConfigField(spec, rows[m.cfgCursor])
		}
	}
	if m.cfgCursor < 0 {
		m.cfgCursor = 0
	}
	if m.cfgCursor >= len(rows) {
		m.cfgCursor = len(rows) - 1
	}
	return m, nil
}

func (m Model) editConfigField(spec apps.SourceSpec, row configRow) (tea.Model, tea.Cmd) {
	id, key, required := spec.ID, row.Key, row.Required
	label := row.Label
	if row.Help != "" {
		label = row.Help
	}
	m.prompt = newPromptBox("编辑 "+row.Label, label, row.Value, false,
		func(mm *Model, v string) tea.Cmd {
			v = strings.TrimSpace(v)
			if required && v == "" {
				mm.setStatusErr(fmt.Errorf("%s 是必填项", label))
				return nil
			}
			return mm.writeSourceConfig(id, key, v)
		})
	return m, nil
}

// resetConfigField 清掉该项的显式值，回落到插件声明的默认值。
func (m Model) resetConfigField(spec apps.SourceSpec, row configRow) (tea.Model, tea.Cmd) {
	if err := m.applySourceConfig(spec.ID, row.Key, ""); err != nil {
		m.setStatusErr(err)
	} else {
		m.status = "已清除 " + row.Label + "，回到默认值"
	}
	return m, nil
}

// applySourceConfig 把某一项配置写回清单并热应用给插件。
//
// 写盘与热应用分开处理：写盘失败必须报错；插件拒绝只提示（清单已改，重载后仍会生效）。
func (m *Model) applySourceConfig(id, key, value string) error {
	for i := range m.afs.Sources {
		if m.afs.Sources[i].ID != id {
			continue
		}
		if m.afs.Sources[i].Config == nil {
			m.afs.Sources[i].Config = map[string]string{}
		}
		if value == "" {
			delete(m.afs.Sources[i].Config, key)
		} else {
			m.afs.Sources[i].Config[key] = value
		}
		if err := m.afs.Save(); err != nil {
			return fmt.Errorf("保存清单: %w", err)
		}
		m.setDirty = true
		if m.host != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.host.Configure(ctx, id, upkitplugin.NewConfig(m.afs.Sources[i].Config)); err != nil {
				return fmt.Errorf("插件未接受该配置: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("来源 %s 已不存在", id)
}

// writeSourceConfig 是弹窗回调用的版本：把结果转成界面提示。
func (m *Model) writeSourceConfig(id, key, value string) tea.Cmd {
	if err := m.applySourceConfig(id, key, value); err != nil {
		m.setStatusErr(err)
		return nil
	}
	m.status = "已保存 " + key
	return nil
}

func (m Model) addSubscription() (tea.Model, tea.Cmd) {
	if m.feed == nil {
		m.setStatusErr(fmt.Errorf("订阅模块未启用"))
		return m, nil
	}
	// 首次使用：先让用户确认启用订阅功能（默认关闭）。
	if !m.feed.FeatureAuthorized() {
		m.confirm = &confirmBox{
			Title: "启用插件订阅",
			Message: "订阅会从网络下载插件可执行文件并在本机运行。\n\n" +
				"启用后，每一条订阅与每一个跨域下载域名都还需要单独确认。\n\n是否启用订阅功能？",
			OnYes: func(mm *Model) tea.Cmd {
				if err := mm.feed.AuthorizeFeature(); err != nil {
					mm.setStatusErr(err)
					return nil
				}
				mm.prompt = mm.subscriptionURLPrompt("")
				return nil
			},
		}
		return m, nil
	}
	m.prompt = m.subscriptionURLPrompt("")
	return m, nil
}

// addBuiltinSubscription 一键添加内置的官方源。
//
// 只是省掉手输地址：订阅功能仍然默认关闭，域名仍然需要单独确认信任。
func (m Model) addBuiltinSubscription() (tea.Model, tea.Cmd) {
	if m.feed == nil {
		m.setStatusErr(fmt.Errorf("订阅模块未启用"))
		return m, nil
	}
	for _, sub := range m.feed.Subscriptions() {
		if pluginfeed.IsBuiltinFeed(sub.URL) {
			m.status = pluginfeed.BuiltinFeedName + " 已在订阅列表中"
			return m, nil
		}
	}
	if !m.feed.FeatureAuthorized() {
		m.confirm = &confirmBox{
			Title: "启用插件订阅",
			Message: "订阅会从网络下载插件可执行文件并在本机运行。\n\n" +
				"接下来会添加「" + pluginfeed.BuiltinFeedName + "」，仍需你确认信任其域名。\n\n是否启用订阅功能？",
			OnYes: func(mm *Model) tea.Cmd {
				if err := mm.feed.AuthorizeFeature(); err != nil {
					mm.setStatusErr(err)
					return nil
				}
				mm.prompt = mm.subscriptionURLPrompt(pluginfeed.BuiltinFeedURL)
				return nil
			},
		}
		return m, nil
	}
	m.prompt = m.subscriptionURLPrompt(pluginfeed.BuiltinFeedURL)
	return m, nil
}

// subscriptionURLPrompt 构造「输入订阅地址」的弹窗；prefill 非空时预填（官方源用）。
//
// 返回而不是直接设置 m.prompt：调用方多为值接收者，直接改字段会丢。
func (m Model) subscriptionURLPrompt(prefill string) *promptBox {
	feed := m.feed
	return newPromptBox("添加订阅", "订阅地址（.json / .yaml / .yml）", prefill, false,
		func(mm *Model, v string) tea.Cmd {
			raw := strings.TrimSpace(v)
			if raw == "" {
				return nil
			}
			host, err := pluginfeed.FeedHost(raw)
			if err != nil {
				mm.setStatusErr(err)
				return nil
			}
			// 域名授权：必须用户明确确认，且按域名记住。
			mm.confirm = &confirmBox{
				Title: "授权订阅域名",
				Message: fmt.Sprintf("将信任来自 %s 的插件下载。\n\n"+
					"该域名提供的插件会在本机运行；如果包地址指向别的域名，安装时会再单独询问。\n\n确认添加这条订阅？", host),
				OnYes: func(mmm *Model) tea.Cmd {
					sub, err := feed.AddSubscription(raw)
					if err != nil {
						mmm.setStatusErr(err)
						return nil
					}
					mmm.status = "已添加订阅 " + sub.URL
					return nil
				},
			}
			return nil
		})
}

func (m Model) removeSubscription(rows []sourceRow) (tea.Model, tea.Cmd) {
	if m.feed == nil {
		m.setStatusErr(fmt.Errorf("订阅模块未启用"))
		return m, nil
	}
	if m.srcCursor < 0 || m.srcCursor >= len(rows) || !rows[m.srcCursor].isSubscription {
		m.setStatus("这里只能删除订阅（插件来源请在清单里移除）")
		return m, nil
	}
	url := rows[m.srcCursor].sub.URL
	feed := m.feed
	m.confirm = &confirmBox{
		Title:   "删除订阅",
		Message: "确定删除这条订阅吗？\n\n" + url + "\n\n已安装的插件不会被卸载。",
		OnYes: func(mm *Model) tea.Cmd {
			if err := feed.RemoveSubscription(url); err != nil {
				mm.setStatusErr(err)
				return nil
			}
			mm.status = "已删除订阅"
			mm.srcCursor = 0
			if mm.feedFor == url { // 正在查看的就是这条，退出详情
				mm.feedFor = ""
				mm.feedEntries = nil
				mm.feedLoaded = false
				mm.feedErr = nil
			}
			return nil
		},
	}
	return m, nil
}

func (m Model) toggleSource(rows []sourceRow) (tea.Model, tea.Cmd) {
	if m.srcCursor < 0 || m.srcCursor >= len(rows) {
		return m, nil
	}
	row := rows[m.srcCursor]
	if row.isSubscription {
		if m.feed == nil {
			return m, nil
		}
		if err := m.feed.SetSubscriptionEnabled(row.sub.URL, !row.sub.EnabledValue()); err != nil {
			m.setStatusErr(err)
			return m, nil
		}
		m.status = "已切换订阅状态"
		return m, nil
	}

	// 插件来源：改清单里的 enabled 并落盘。
	for i := range m.afs.Sources {
		if m.afs.Sources[i].ID != row.spec.ID {
			continue
		}
		next := !m.afs.Sources[i].EnabledValue()
		m.afs.Sources[i].Enabled = &next
		if err := m.afs.Save(); err != nil {
			m.setStatusErr(fmt.Errorf("保存清单: %w", err))
			return m, nil
		}
		m.status = "已切换来源状态（重载后生效，按 r）"
		return m, nil
	}
	return m, nil
}

func (m Model) reloadSources() (tea.Model, tea.Cmd) {
	if m.host == nil {
		m.setStatusErr(fmt.Errorf("插件宿主未启用"))
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m.host.Load(ctx)
	m.status = "插件来源已重载"
	return m, nil
}

// firstNonEmptyStr 返回第一个非空字符串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
