package pluginhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
)

// State 是来源的运行时状态。
type State string

// 来源状态取值。
const (
	StateOK        State = "ok"        // 已启动，可用
	StateDisabled  State = "disabled"  // 配置里停用
	StateMissing   State = "missing"   // 找不到可执行文件或描述
	StateUntrusted State = "untrusted" // 未信任（或缺哈希记录），不启动
	StateError     State = "error"     // 启动或握手失败
)

// SourceStatus 是一个来源对外呈现的状态，供界面展示与排障。
type SourceStatus struct {
	ID      string
	Name    string
	Kind    string
	Mode    string
	Version string
	Apps    int
	State   State
	Detail  string
	Exec    string
	SHA256  string
}

// Config 是宿主配置。
type Config struct {
	// Dir 是插件目录（存放 <id>[.exe] 与 <id>.plugin.yaml）。
	Dir string
	// Entries 是清单里声明的来源。
	Entries []apps.SourceSpec
	// DataRoot / LogRoot 是插件私有目录的根；每个插件得到 <Root>/<来源ID>。
	DataRoot string
	LogRoot  string
	// Log 是宿主日志；为 nil 时静默。
	Log core.Logger
	// Stderr 接收插件进程的 stdout/stderr；为 nil 时丢弃。
	Stderr io.Writer
}

// Manager 负责插件来源的发现、信任、启动与调用。
type Manager struct {
	cfg Config

	mu    sync.RWMutex
	items []*item
	byID  map[string]*item
}

type item struct {
	entry apps.SourceSpec
	man   Manifest
	exec  string
	sha   string
	mode  string

	state  State
	detail string

	client *upkitplugin.Client
	apps   []upkitplugin.Software
	// info 是插件握手时自报的信息，是能力判定的唯一依据（清单里的 mode 仅供离线展示）。
	info upkitplugin.Info
}

// NewManager 合并「清单里声明的来源」与「插件目录里自动发现到的描述」。
//
// 清单优先：同 ID 时以清单为准（用户可覆盖 exec / 信任 / 超时 / 软件开关）；
// 仅被自动发现的插件也会出现在列表里，但在写入信任哈希之前不会启动。
func NewManager(cfg Config) (*Manager, error) {
	m := &Manager{cfg: cfg, byID: map[string]*item{}}

	seen := map[string]int{}
	for _, e := range cfg.Entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			return nil, fmt.Errorf("来源缺少 id")
		}
		if !upkitplugin.ValidID(id) {
			return nil, fmt.Errorf("来源 id %q 不合法（要求 ^[a-z0-9][a-z0-9._-]{0,63}$）", id)
		}
		if kind := strings.TrimSpace(e.Kind); kind != "" && kind != apps.KindPlugin {
			return nil, fmt.Errorf("来源 %s 的 kind %q 不支持（目前只支持 %s）", id, kind, apps.KindPlugin)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("来源 id %q 重复", id)
		}
		seen[id] = 1
		it := &item{entry: e, mode: normalizeMode(e.Mode)}
		m.items = append(m.items, it)
		m.byID[id] = it
	}

	found, err := Discover(cfg.Dir)
	if err != nil {
		return nil, err
	}
	for _, man := range found {
		if it, ok := m.byID[man.ID]; ok {
			it.man = man
			if it.mode == "" {
				it.mode = normalizeMode(man.Mode)
			}
			continue
		}
		it := &item{
			entry: apps.SourceSpec{ID: man.ID, Kind: apps.KindPlugin},
			man:   man,
			mode:  normalizeMode(man.Mode),
		}
		m.items = append(m.items, it)
		m.byID[man.ID] = it
	}

	sort.SliceStable(m.items, func(i, j int) bool { return m.items[i].entry.ID < m.items[j].entry.ID })
	return m, nil
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case upkitplugin.ModeFull:
		return upkitplugin.ModeFull
	case "", upkitplugin.ModeCatalog:
		return upkitplugin.ModeCatalog
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// SourceIDs 返回全部已声明的来源 ID（稳定的字典序）。
func (m *Manager) SourceIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.items))
	for _, it := range m.items {
		out = append(out, it.entry.ID)
	}
	return out
}

// Load 依次启动所有可用的来源。
//
// 单个来源失败绝不影响其它来源，也不影响内置能力：失败原因记录在状态里供界面展示。
func (m *Manager) Load(ctx context.Context) {
	m.mu.Lock()
	items := append([]*item(nil), m.items...)
	m.mu.Unlock()

	for _, it := range items {
		m.start(ctx, it)
	}
}

func (m *Manager) start(ctx context.Context, it *item) {
	id := it.entry.ID

	if !it.entry.EnabledValue() || !it.man.EnabledValue() {
		m.setState(it, StateDisabled, "已在配置中停用")
		return
	}

	it.exec = ResolveExec(m.cfg.Dir, id, firstNonEmpty(it.entry.Exec, it.man.Exec))
	if st, err := os.Stat(it.exec); err != nil || st.IsDir() {
		m.setState(it, StateMissing, fmt.Sprintf("找不到插件可执行文件 %s", it.exec))
		return
	}

	sha, err := HashFile(it.exec)
	if err != nil {
		m.setState(it, StateError, fmt.Sprintf("计算插件哈希失败: %v", err))
		return
	}
	it.sha = sha

	if !Trusted(it.entry.Trust, sha) {
		detail := fmt.Sprintf("未信任，已跳过启动。其 sha256 为 %s；确认来源可信后把它写入清单的 sources[%s].trust 即可启用", sha, id)
		m.setState(it, StateUntrusted, detail)
		return
	}

	timeout := time.Duration(it.entry.TimeoutSeconds) * time.Second
	if timeout <= 0 && it.man.TimeoutSeconds > 0 {
		timeout = time.Duration(it.man.TimeoutSeconds) * time.Second
	}

	client, err := upkitplugin.NewClient(upkitplugin.ClientConfig{
		Exec:    it.exec,
		Dir:     filepath.Dir(it.exec),
		Timeout: timeout,
		Stderr:  m.cfg.Stderr,
	})
	if err != nil {
		m.setState(it, StateError, err.Error())
		return
	}

	list, err := client.Source().List(ctx)
	if err != nil {
		client.Kill()
		m.setState(it, StateError, fmt.Sprintf("读取插件软件列表失败: %v", err))
		return
	}

	m.mu.Lock()
	it.client = client
	it.apps = list
	info := client.Info()
	it.info = info
	if it.man.Name == "" {
		it.man.Name = info.Name
	}
	it.man.Description = firstNonEmpty(it.man.Description, info.Description)
	it.state = StateOK
	it.detail = ""
	m.mu.Unlock()

	m.logf(core.LevelInfo, "插件来源已加载", "source", id, "version", info.Version, "apps", len(list), "mode", it.mode)
}

// Close 停止全部插件进程。
func (m *Manager) Close() {
	m.mu.Lock()
	items := append([]*item(nil), m.items...)
	m.mu.Unlock()
	for _, it := range items {
		if it.client != nil {
			it.client.Kill()
			it.client = nil
		}
	}
}

// Sources 返回全部来源的状态快照。
func (m *Manager) Sources() []SourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SourceStatus, 0, len(m.items))
	for _, it := range m.items {
		st := SourceStatus{
			ID:     it.entry.ID,
			Name:   firstNonEmpty(it.entry.Name, it.man.Name),
			Kind:   apps.KindPlugin,
			Mode:   it.mode,
			Apps:   len(it.apps),
			State:  it.state,
			Detail: it.detail,
			Exec:   it.exec,
			SHA256: it.sha,
		}
		if it.client != nil {
			info := it.client.Info()
			st.Version = info.Version
			if st.Name == "" {
				st.Name = info.Name
			}
		}
		if st.Name == "" {
			st.Name = st.ID
		}
		out = append(out, st)
	}
	return out
}

// Software 返回某个来源里插件上报的全部软件（未做启用过滤）。
func (m *Manager) Software(sourceID string) ([]upkitplugin.Software, error) {
	it, err := m.lookup(sourceID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]upkitplugin.Software, len(it.apps))
	copy(out, it.apps)
	return out, nil
}

// SoftwareEnabled 报告源内某个软件是否启用（清单里的显式开关优先）。
func (m *Manager) SoftwareEnabled(sourceID, appID string) bool {
	it, err := m.lookup(sourceID)
	if err != nil {
		return false
	}
	return it.entry.AppEnabled(appID)
}

// AppSpecs 把某个来源里启用的软件翻译成清单条目（限定 ID 为 <来源>/<软件>）。
func (m *Manager) AppSpecs(sourceID string) ([]apps.AppSpec, error) {
	sw, err := m.Software(sourceID)
	if err != nil {
		return nil, err
	}
	it, err := m.lookup(sourceID)
	if err != nil {
		return nil, err
	}
	full := m.hasCapability(it, upkitplugin.CapabilityFull)
	out := make([]apps.AppSpec, 0, len(sw))
	for _, s := range sw {
		if !m.SoftwareEnabled(sourceID, s.ID) {
			continue
		}
		out = append(out, ToAppSpec(s, sourceID, full))
	}
	return out, nil
}

// Versions 返回插件来源里某个软件的可用版本（已翻译成宿主领域类型）。
func (m *Manager) Versions(ctx context.Context, sourceID, appID string, limit int) ([]core.Release, error) {
	it, err := m.lookup(sourceID)
	if err != nil {
		return nil, err
	}
	client, err := m.client(it)
	if err != nil {
		return nil, err
	}
	rels, err := client.Source().Versions(ctx, upkitplugin.SourceVersionsRequest{
		AppID:   appID,
		Limit:   limit,
		Runtime: m.runtime(it),
	})
	if err != nil {
		return nil, err
	}
	out := make([]core.Release, 0, len(rels))
	for _, r := range rels {
		out = append(out, ToCoreRelease(r))
	}
	return out, nil
}

// Latest 返回插件来源里某个软件的最新版本。
func (m *Manager) Latest(ctx context.Context, sourceID, appID string) (core.Release, error) {
	rels, err := m.Versions(ctx, sourceID, appID, 1)
	if err != nil {
		return core.Release{}, err
	}
	if len(rels) == 0 {
		return core.Release{}, fmt.Errorf("%w: 插件来源 %s 没有为 %s 提供任何版本", core.ErrNotFound, sourceID, appID)
	}
	return rels[0], nil
}

// ConfigSchema 返回插件声明的配置项，供界面生成配置表单。
func (m *Manager) ConfigSchema(ctx context.Context, sourceID string) (upkitplugin.ConfigSchema, error) {
	it, err := m.lookup(sourceID)
	if err != nil {
		return upkitplugin.ConfigSchema{}, err
	}
	client, err := m.client(it)
	if err != nil {
		return upkitplugin.ConfigSchema{}, err
	}
	return client.Source().ConfigSchema(ctx)
}

// ValidateConfig 让插件校验一份配置（保存前的预检）。
func (m *Manager) ValidateConfig(ctx context.Context, sourceID string, cfg upkitplugin.Config) (upkitplugin.ValidationResult, error) {
	it, err := m.lookup(sourceID)
	if err != nil {
		return upkitplugin.ValidationResult{}, err
	}
	client, err := m.client(it)
	if err != nil {
		return upkitplugin.ValidationResult{}, err
	}
	return client.Source().ValidateConfig(ctx, cfg)
}

// Configure 把插件级配置热应用到插件进程。
func (m *Manager) Configure(ctx context.Context, sourceID string, cfg upkitplugin.Config) error {
	it, err := m.lookup(sourceID)
	if err != nil {
		return err
	}
	client, err := m.client(it)
	if err != nil {
		return err
	}
	if err := client.Source().Configure(ctx, cfg); err != nil {
		return err
	}
	m.mu.Lock()
	it.entry.Config = configToMap(cfg)
	m.mu.Unlock()
	return nil
}

// configToMap 把配置摊平成清单里的键值对（清单层就是键值映射）。
func configToMap(c upkitplugin.Config) map[string]string {
	if c.Len() == 0 {
		return nil
	}
	out := make(map[string]string, c.Len())
	for _, k := range c.Keys() {
		out[k] = c.Get(k)
	}
	return out
}

// ── 内部工具 ─────────────────────────────────────────────────

func (m *Manager) lookup(sourceID string) (*item, error) {
	m.mu.RLock()
	it, ok := m.byID[sourceID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: 未知来源 %q", core.ErrNotFound, sourceID)
	}
	return it, nil
}

func (m *Manager) client(it *item) (*upkitplugin.Client, error) {
	m.mu.RLock()
	c := it.client
	st := it.state
	detail := it.detail
	m.mu.RUnlock()
	if c == nil {
		if detail == "" {
			detail = string(st)
		}
		return nil, fmt.Errorf("来源 %s 当前不可用（%s）", it.entry.ID, detail)
	}
	return c, nil
}

func (m *Manager) runtime(it *item) upkitplugin.RuntimeConfig {
	return upkitplugin.RuntimeConfig{
		Config:  upkitplugin.NewConfig(cloneMap(it.entry.Config)),
		DataDir: filepath.Join(m.cfg.DataRoot, dirPlugins, it.entry.ID),
		LogDir:  filepath.Join(m.cfg.LogRoot, dirPlugins, it.entry.ID),
	}
}

func (m *Manager) setState(it *item, st State, detail string) {
	m.mu.Lock()
	it.state = st
	it.detail = detail
	m.mu.Unlock()
	if st == StateOK || st == StateDisabled {
		m.logf(core.LevelInfo, "插件来源状态", "source", it.entry.ID, "state", string(st), "detail", detail)
		return
	}
	m.logf(core.LevelWarn, "插件来源不可用", "source", it.entry.ID, "state", string(st), "detail", detail)
}

func (m *Manager) logf(level core.LogLevel, msg string, kv ...any) {
	if m.cfg.Log == nil {
		return
	}
	switch level {
	case core.LevelWarn:
		m.cfg.Log.Warn(msg, kv...)
	case core.LevelError:
		m.cfg.Log.Error(msg, kv...)
	case core.LevelDebug:
		m.cfg.Log.Debug(msg, kv...)
	default:
		m.cfg.Log.Info(msg, kv...)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
