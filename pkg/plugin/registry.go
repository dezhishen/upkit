package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// registry 是插件进程内的 Source 实现。
//
// 它把「一批软件 + 各自的构造器」翻译成宿主可调用的方法：实例按需构造并缓存，
// 构造失败只影响单个软件（不会让整个插件不可用）。
type registry struct {
	info Info

	mo    sync.Mutex // 保护 order / apps
	order []string
	apps  map[string]*appState

	co  sync.RWMutex // 保护 cfg
	cfg Config

	eo     sync.Mutex // 保护 events
	events map[string][]Event

	ko      sync.Mutex // 保护 cancels
	cancels map[string]context.CancelFunc
}

type appState struct {
	reg  Registration
	inst App
	err  error
	done bool
}

// newRegistry 校验并固化注册表。校验失败表示插件自身写错了，应当尽早暴露。
func newRegistry(info Info, regs []Registration) (*registry, error) {
	if strings.TrimSpace(info.ID) == "" {
		return nil, fmt.Errorf("%w: 插件缺少 id", ErrBadConfig)
	}
	if !ValidID(info.ID) {
		return nil, fmt.Errorf("%w: 插件 id %q 不合法（要求 ^[a-z0-9][a-z0-9._-]{0,63}$，且不得为 Windows 保留名）", ErrBadConfig, info.ID)
	}
	if info.APIVersion == "" {
		info.APIVersion = APIVersion
	}
	if len(regs) == 0 {
		return nil, fmt.Errorf("%w: 插件 %s 没有注册任何软件", ErrBadConfig, info.ID)
	}

	r := &registry{
		info:    info,
		apps:    make(map[string]*appState, len(regs)),
		events:  map[string][]Event{},
		cancels: map[string]context.CancelFunc{},
	}
	for i, reg := range regs {
		id := strings.TrimSpace(reg.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: 第 %d 个注册项缺少 id", ErrBadConfig, i+1)
		}
		if !ValidID(id) {
			return nil, fmt.Errorf("%w: 软件 id %q 不合法（要求 ^[a-z0-9][a-z0-9._-]{0,63}$，且不得为 Windows 保留名）", ErrBadConfig, id)
		}
		if _, dup := r.apps[id]; dup {
			return nil, fmt.Errorf("%w: 软件 id %q 重复注册", ErrBadConfig, id)
		}
		cp := reg
		cp.ID = id
		if cp.Name == "" {
			cp.Name = id
		}
		cp.Tags = append([]string(nil), reg.Tags...)
		cp.Provides = append([]string(nil), reg.Provides...)
		cp.Defaults = reg.Defaults.clone()
		r.apps[id] = &appState{reg: cp}
		r.order = append(r.order, id)
	}
	return r, nil
}

// ── Source 实现 ─────────────────────────────────────────────

func (r *registry) Info(_ context.Context) (Info, error) {
	info := r.info
	if len(info.Capabilities) == 0 {
		info.Capabilities = []string{CapabilityCatalog}
	}
	return info, nil
}

func (r *registry) List(_ context.Context) ([]Software, error) {
	r.mo.Lock()
	defer r.mo.Unlock()
	out := make([]Software, 0, len(r.order))
	for _, id := range r.order {
		sw := r.apps[id].reg.Software
		sw.Tags = append([]string(nil), sw.Tags...)
		sw.Provides = append([]string(nil), sw.Provides...)
		sw.Defaults = sw.Defaults.clone()
		if sw.Target != nil {
			t := *sw.Target
			t.Entrypoints = append([]string(nil), t.Entrypoints...)
			t.Processes = append([]string(nil), t.Processes...)
			t.Preserve = append([]string(nil), t.Preserve...)
			sw.Target = &t
		}
		out = append(out, sw)
	}
	return out, nil
}

func (r *registry) Versions(ctx context.Context, req SourceVersionsRequest) ([]Release, error) {
	app, err := r.instance(req.AppID, req.Runtime)
	if err != nil {
		return nil, err
	}
	rels, err := app.Versions(ctx, VersionsRequest{
		Limit:  req.Limit,
		Config: r.mergedConfig(req.Runtime.Config),
	})
	if err != nil {
		return nil, err
	}
	return rels, nil
}

func (r *registry) Status(ctx context.Context, req SourceAppRequest) (Status, error) {
	m, err := r.method(req.AppID, req.Runtime)
	if err != nil {
		return Status{}, err
	}
	return m.Status(ctx, StatusRequest{
		InstallPath: req.InstallPath,
		DataDir:     req.Runtime.DataDir,
		Config:      r.mergedConfig(req.Runtime.Config),
	})
}

func (r *registry) Plan(ctx context.Context, req SourcePlanRequest) (PlanResult, error) {
	m, err := r.method(req.AppID, req.Runtime)
	if err != nil {
		return PlanResult{}, err
	}
	return m.Plan(ctx, r.planRequest(req))
}

func (r *registry) Apply(ctx context.Context, req SourcePlanRequest) (Result, error) {
	m, err := r.method(req.AppID, req.Runtime)
	if err != nil {
		return Result{}, err
	}
	jobID := req.JobID
	if jobID != "" {
		runCtx, cancel := context.WithCancel(ctx)
		r.setCancel(jobID, cancel)
		defer func() {
			cancel()
			r.clearCancel(jobID)
		}()
		ctx = runCtx
	}
	if jobID != "" {
		r.clearEvents(jobID)
	}
	send := func(e Event) {
		if jobID != "" {
			r.pushEvent(jobID, e)
		}
	}
	res, err := m.Apply(ctx, r.planRequest(req), send)

	// 收尾事件由 SDK 补，而不是要求插件作者记得发：宿主靠它判断任务真的结束了，
	// 少发一条就会让界面上的进度条永远停在 99%。插件自己也可以发，最后一条为准。
	if jobID != "" {
		if err != nil {
			r.pushEvent(jobID, Event{Kind: EventFailed, Level: LogLevelError, Msg: err.Error()})
		} else {
			r.pushEvent(jobID, Event{Kind: EventFinished, Level: LogLevelInfo, Msg: finishMessage(res)})
		}
	}
	return res, err
}

// finishMessage 拼一句人类可读的收尾说明。
func finishMessage(res Result) string {
	if res.To == "" {
		return "完成"
	}
	return "完成 " + res.To
}

func (r *registry) Rollback(ctx context.Context, req SourceRollbackRequest) error {
	m, err := r.method(req.AppID, req.Runtime)
	if err != nil {
		return err
	}
	return m.Rollback(ctx, RollbackRequest{
		BackupPath:  req.BackupPath,
		InstallPath: req.InstallPath,
		Config:      r.mergedConfig(req.Runtime.Config),
	})
}

func (r *registry) Uninstall(ctx context.Context, req SourceUninstallRequest) error {
	m, err := r.method(req.AppID, req.Runtime)
	if err != nil {
		return err
	}
	return m.Uninstall(ctx, UninstallRequest{
		KeepUserData: req.KeepUserData,
		InstallPath:  req.InstallPath,
		DataDir:      req.Runtime.DataDir,
		Config:       r.mergedConfig(req.Runtime.Config),
	})
}

func (r *registry) PollEvents(_ context.Context, jobID string) ([]Event, error) {
	if jobID == "" {
		return nil, nil
	}
	return r.drainEvents(jobID), nil
}

func (r *registry) ConfigSchema(_ context.Context) (ConfigSchema, error) {
	r.mo.Lock()
	defer r.mo.Unlock()
	for _, id := range r.order {
		if s := r.apps[id].reg.Schema; s != nil {
			return *s, nil
		}
	}
	return ConfigSchema{Title: r.info.Name}, nil
}

func (r *registry) ValidateConfig(ctx context.Context, cfg Config) (ValidationResult, error) {
	c, ok := r.configurable()
	if !ok {
		return ValidationResult{OK: true}, nil
	}
	return c.ValidateConfig(ctx, cfg)
}

func (r *registry) Configure(ctx context.Context, cfg Config) error {
	if c, ok := r.configurable(); ok {
		if err := c.Configure(ctx, cfg); err != nil {
			return err
		}
	}
	r.co.Lock()
	r.cfg = cfg.clone()
	r.co.Unlock()
	r.resetInstances()
	return nil
}

func (r *registry) Ping(_ context.Context) error { return nil }

func (r *registry) Cancel(_ context.Context, jobID string) error {
	r.ko.Lock()
	cancel, ok := r.cancels[jobID]
	r.ko.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	return nil
}

// ── 内部工具 ─────────────────────────────────────────────────

func (r *registry) planRequest(req SourcePlanRequest) PlanRequest {
	p := req.Plan
	if p.InstallPath == "" {
		p.InstallPath = req.InstallPath
	}
	if p.DataDir == "" {
		p.DataDir = req.Runtime.DataDir
	}
	p.Config = r.mergedConfig(req.Runtime.Config)
	return p
}

// instance 按需构造软件实例并缓存结果。
func (r *registry) instance(appID string, rt RuntimeConfig) (App, error) {
	r.mo.Lock()
	defer r.mo.Unlock()

	st, ok := r.apps[appID]
	if !ok {
		return nil, fmt.Errorf("%w: 未知软件 %q", ErrNotFound, appID)
	}
	if st.done {
		return st.inst, st.err
	}
	st.done = true

	if st.reg.New == nil {
		st.err = fmt.Errorf("%w: 软件 %q 未提供构造器", ErrBadConfig, appID)
		return nil, st.err
	}
	cfg := AppConfig{
		Source:  r.info,
		App:     st.reg.Software,
		Config:  r.mergedConfig(rt.Config),
		DataDir: rt.DataDir,
		LogDir:  rt.LogDir,
		Log:     newStderrLogger(),
	}
	inst, err := st.reg.New(cfg)
	if err != nil {
		st.err = fmt.Errorf("软件 %q 的构造器失败: %w", appID, err)
		return nil, st.err
	}
	if inst == nil {
		st.err = fmt.Errorf("软件 %q 的构造器返回了 nil", appID)
		return nil, st.err
	}
	st.inst = inst
	return inst, nil
}

// method 取软件的 full 模式实现；未实现时返回 ErrNotSupported，宿主据此回退。
func (r *registry) method(appID string, rt RuntimeConfig) (Method, error) {
	inst, err := r.instance(appID, rt)
	if err != nil {
		return nil, err
	}
	m, ok := inst.(Method)
	if !ok {
		return nil, fmt.Errorf("%w: 软件 %q 未实现 full 模式，请使用宿主内置能力", ErrNotSupported, appID)
	}
	return m, nil
}

// configurable 返回第一个实现 Configurable 的实例（插件级配置由它统一处理）。
func (r *registry) configurable() (Configurable, bool) {
	r.mo.Lock()
	ids := append([]string(nil), r.order...)
	r.mo.Unlock()
	for _, id := range ids {
		inst, err := r.instance(id, RuntimeConfig{})
		if err != nil {
			continue
		}
		if c, ok := inst.(Configurable); ok {
			return c, true
		}
	}
	return nil, false
}

func (r *registry) resetInstances() {
	r.mo.Lock()
	defer r.mo.Unlock()
	for id, st := range r.apps {
		st.done = false
		st.inst = nil
		st.err = nil
		r.apps[id] = st
	}
}

// mergedConfig 把插件级配置与软件级配置合并（软件级优先）。
func (r *registry) mergedConfig(appCfg Config) Config {
	r.co.RLock()
	out := r.cfg.clone()
	r.co.RUnlock()
	for _, k := range appCfg.Keys() {
		out.Set(k, appCfg.values[k])
	}
	return out
}

func (r *registry) setCancel(jobID string, cancel context.CancelFunc) {
	r.ko.Lock()
	r.cancels[jobID] = cancel
	r.ko.Unlock()
}

func (r *registry) clearCancel(jobID string) {
	r.ko.Lock()
	delete(r.cancels, jobID)
	r.ko.Unlock()
}

func (r *registry) pushEvent(jobID string, e Event) {
	r.eo.Lock()
	r.events[jobID] = append(r.events[jobID], e)
	r.eo.Unlock()
}

func (r *registry) drainEvents(jobID string) []Event {
	r.eo.Lock()
	defer r.eo.Unlock()
	evs := r.events[jobID]
	delete(r.events, jobID)
	return evs
}

func (r *registry) clearEvents(jobID string) {
	r.eo.Lock()
	delete(r.events, jobID)
	r.eo.Unlock()
}

func cloneStrings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// 供内部使用：Config 的零值可以直接读取，无需初始化。
