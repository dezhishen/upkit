// Package engine 编排「检查 → 计划 → 下载 → 解包 → 备份 → 替换 → 记录」全流程。
//
// 它只依赖 core 的接口与 registry 的适配器，不认识 GitHub、zip 或 msiexec。
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/download"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/util"
	"github.com/dezhishen/upkit/internal/version"
)

// Options 构造 Engine。
type Options struct {
	Settings *settings.Settings
	Apps     *apps.File
	Registry *registry.Registry
	Log      core.Logger
	Sink     core.EventSink
	Prompter core.Prompter

	// Audit 接收审计条目（键值对）；由 main 接到 logging.Manager.Audit。
	Audit func(entry map[string]any)

	// Plugins 是插件宿主；为 nil 时插件来源不可用，内置能力不受影响。
	Plugins registry.PluginHost

	Force  bool // 强制重装
	DryRun bool // 只出计划，不做任何写操作
}

// App 是「清单条目 + 运行时状态」的汇总视图。
type App struct {
	Ref      core.AppRef
	Status   core.Status
	Release  core.Release
	CheckErr error

	Action   core.Action
	Note     string
	Shadowed bool
	Conflict *Conflict

	Backups []core.Backup
}

// QualifiedID 返回展示用的限定标识（当前不带插件前缀）。
func (a *App) QualifiedID() string { return a.Ref.ID }

// Engine 是编排器。
type Engine struct {
	opts     Options
	settings *settings.Settings
	apps     *apps.File
	reg      *registry.Registry
	dl       *download.Client
	log      core.Logger
	sink     core.EventSink
	prompter core.Prompter
	plugins  registry.PluginHost

	mu   sync.RWMutex
	list []*App
}

// New 构造 Engine。
func New(opts Options) (*Engine, error) {
	if opts.Settings == nil {
		return nil, fmt.Errorf("缺少设置")
	}
	if opts.Apps == nil {
		return nil, fmt.Errorf("缺少软件清单")
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("缺少适配器注册表")
	}
	if opts.Log == nil {
		opts.Log = nopLogger{}
	}
	if opts.Sink == nil {
		opts.Sink = core.NopSink{}
	}

	var timeout time.Duration
	if opts.Settings.Network.TimeoutSeconds > 0 {
		timeout = time.Duration(opts.Settings.Network.TimeoutSeconds) * time.Second
	}
	dl, err := download.New(timeout, opts.Settings.Network.Proxy)
	if err != nil {
		return nil, err
	}
	dl.SetUserAgent("upkit/2")

	return &Engine{
		opts:     opts,
		settings: opts.Settings,
		apps:     opts.Apps,
		reg:      opts.Registry,
		dl:       dl,
		log:      opts.Log,
		sink:     opts.Sink,
		prompter: opts.Prompter,
		plugins:  opts.Plugins,
	}, nil
}

// deps 返回注入给适配器的依赖。
func (e *Engine) deps() registry.Deps {
	return registry.Deps{
		HTTP:     e.dl.HTTP(),
		Download: e.dl,
		Log:      e.log,
		Clock:    time.Now,
		Plugins:  e.plugins,
	}
}

// List 构建软件列表并做冲突归一化（不联网）。
func (e *Engine) List(ctx context.Context) ([]*App, error) {
	refs, err := e.apps.Build()
	if err != nil {
		return nil, err
	}
	token := settings.ResolveToken(e.settings.Network.GitHubToken)

	out := make([]*App, 0, len(refs))
	for _, ref := range refs {
		if ref.SourceOpts == nil {
			ref.SourceOpts = map[string]string{}
		}
		if token != "" {
			ref.SourceOpts["token"] = token
		}
		if _, err := e.reg.Method(ref, e.deps()); err != nil {
			return nil, err
		}
		if _, err := e.reg.Source(ref, e.deps()); err != nil {
			return nil, err
		}
		if _, err := e.reg.Unpacker(ref, e.deps()); err != nil {
			return nil, err
		}
		out = append(out, &App{Ref: ref})
	}
	normalizeConflicts(out, e.apps)
	e.mu.Lock()
	e.list = out
	e.mu.Unlock()
	return out, nil
}

// Apps 返回最近一次 List 的结果（未构建时为 nil）。
func (e *Engine) Apps() []*App {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.list
}

// Find 按 id 查找条目。
func (e *Engine) Find(id string) *App {
	for _, a := range e.Apps() {
		if a.Ref.ID == id {
			return a
		}
	}
	return nil
}

// Check 刷新全部条目的本地状态与上游版本。
func (e *Engine) Check(ctx context.Context) ([]*App, error) {
	list, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	conc := e.settings.Engine.ApplyConcurrency
	if conc <= 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, a := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(a *App) {
			defer wg.Done()
			defer func() { <-sem }()
			e.checkOne(ctx, a)
		}(a)
	}
	wg.Wait()
	return list, nil
}

// CheckOne 刷新单个条目。
func (e *Engine) CheckOne(ctx context.Context, id string) (*App, error) {
	if len(e.Apps()) == 0 {
		if _, err := e.List(ctx); err != nil {
			return nil, err
		}
	}
	a := e.Find(id)
	if a == nil {
		return nil, fmt.Errorf("%w: 未找到软件 %q", core.ErrNotFound, id)
	}
	e.checkOne(ctx, a)
	return a, nil
}

// checkOne 刷新单个条目的状态。
func (e *Engine) checkOne(ctx context.Context, a *App) {
	ref := a.Ref
	a.Backups = nil
	if b, err := e.backups(ctx, ref); err == nil {
		a.Backups = b
	}

	status, err := e.detect(ctx, ref)
	if err != nil {
		e.log.Warn("读取本地状态失败", "app", ref.ID, "error", err.Error())
	}
	a.Status = status

	src, err := e.reg.Source(ref, e.deps())
	if err != nil {
		a.CheckErr = err
		return
	}
	rel, err := src.Latest(ctx, ref)
	if err != nil {
		a.CheckErr = err
		return
	}
	a.CheckErr = nil
	a.Release = rel
	a.Action, a.Note = decide(ref, status, rel, e.opts.Force)
	e.sink.Emit(core.Event{AppID: ref.ID, Kind: core.EventPhase, Phase: "检查",
		Level: core.LevelInfo, Msg: fmt.Sprintf("本地 %s → 上游 %s", displayVersion(status.Version), rel.Version)})
}

// detect 按声明的探测链读取本地状态。
func (e *Engine) detect(ctx context.Context, ref core.AppRef) (core.Status, error) {
	detectors, err := e.reg.Detectors(ref, e.deps())
	if err != nil {
		return core.Status{}, err
	}
	for _, d := range detectors {
		st, err := d.Detect(ctx, ref)
		if err != nil {
			e.log.Debug("探测器失败", "app", ref.ID, "detector", d.Name(), "error", err.Error())
			continue
		}
		if st.Installed {
			return st, nil
		}
	}
	return core.Status{}, nil
}

// backups 读取该软件的备份列表。
func (e *Engine) backups(ctx context.Context, ref core.AppRef) ([]core.Backup, error) {
	m, err := e.reg.Method(ref, e.deps())
	if err != nil {
		return nil, err
	}
	req := e.request(ref, core.Plan{App: ref})
	return m.Backups(ctx, req)
}

// request 组装适配器执行请求。
func (e *Engine) request(ref core.AppRef, plan core.Plan) core.Request {
	return core.Request{
		App:        ref,
		Plan:       plan,
		CacheDir:   e.settings.Storage.CacheDir,
		BackupDir:  e.settings.BackupDir(ref.ID),
		KeepBackup: e.settings.Storage.BackupKeep > 0,
		MaxBackups: e.settings.Storage.BackupKeep,
		Verify:     true,
		Proxy:      e.settings.Network.Proxy,
		Token:      settings.ResolveToken(e.settings.Network.GitHubToken),
		WorkDir:    e.settings.Storage.TempDir,
	}
}

// decide 判断本次要做什么。
func decide(ref core.AppRef, st core.Status, rel core.Release, force bool) (core.Action, string) {
	if force {
		return core.ActionReinstall, "已选择强制重装"
	}
	if !st.Installed || st.Version == "" {
		return core.ActionInstall, "尚未安装，将全新安装"
	}
	switch version.Compare(rel.Version, st.Version) {
	case 1:
		return core.ActionUpdate, fmt.Sprintf("%s → %s", st.Version, rel.Version)
	case 0:
		if ref.TrackRevision && ref.Pin == "" {
			local := version.MustParse(st.Version)
			remote := version.MustParse(rel.Version)
			if local.Suffix != "" && remote.CompareFull(local) > 0 {
				return core.ActionUpdate, fmt.Sprintf("%s → %s（修订号变化）", st.Version, rel.Version)
			}
		}
		return core.ActionNoOp, "已是最新版本"
	default:
		return core.ActionNoOp, fmt.Sprintf("本地版本（%s）比上游更新", st.Version)
	}
}

// normalizePath 统一路径形态，用于「同目标」冲突检测。
func normalizePath(p string) string {
	p = util.ExpandPath(p)
	p = filepath.Clean(p)
	if !strings.HasSuffix(p, string(filepath.Separator)) {
		p += string(filepath.Separator)
	}
	return strings.ToLower(p)
}

func displayVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "（未安装）"
	}
	return v
}

// nopLogger 在未注入 logger 时使用。
type nopLogger struct{}

func (nopLogger) Debug(string, ...any)    {}
func (nopLogger) Info(string, ...any)     {}
func (nopLogger) Warn(string, ...any)     {}
func (nopLogger) Error(string, ...any)    {}
func (nopLogger) With(...any) core.Logger { return nopLogger{} }

// Summary 生成条目的单行摘要，供 TUI 与日志使用。
func (a *App) Summary() string {
	cur := displayVersion(a.Status.Version)
	to := a.Release.Version
	if to == "" {
		to = "—"
	}
	state := string(a.Action)
	if a.Shadowed {
		state = "shadowed"
	}
	return fmt.Sprintf("%-24s %-10s %-16s %-16s %s", a.Ref.DisplayName(), state, cur, to, a.Note)
}
