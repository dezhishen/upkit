package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
)

// 方法轴与探测器轴的插件适配器名（见 internal/method/plugin 与 internal/detect/plugin）。
//
// 定义在宿主里是因为「插件声明了 full 能力就用这两个轴」这条规则属于宿主，
// 适配器包反过来引用这里，保证同一个契约只有一份定义。
const (
	// KindPlugin 既是 full 模式的安装方式名，也是它的探测器名。
	KindPlugin = "plugin"

	// pluginMethodPrefix 是方法轴适配器的展示名前缀，形如 plugin:<来源ID>。
	pluginMethodPrefix = KindPlugin + ":"
)

// Method 返回插件为该软件提供的安装方法（full 模式）。
//
// ok 为 false 表示该来源的清单项存在但没有接管这个软件的安装 —— 此时调用方应
// 报错而不是回退到内置四轴：清单里写了插件来源的软件却没有可用的安装器，静默
// 回退只会让用户以为装上了，实际装的是别的东西。
func (m *Manager) Method(ref core.AppRef) (core.InstallMethod, bool) {
	it, appID, ok := m.takeover(ref)
	if !ok {
		return nil, false
	}
	return &appMethod{mgr: m, it: it, appRef: ref, appID: appID}, true
}

// Status 返回插件报告的本机安装状态（full 模式）。
func (m *Manager) Status(ctx context.Context, ref core.AppRef) (core.Status, bool, error) {
	it, appID, ok := m.takeover(ref)
	if !ok {
		return core.Status{}, false, nil
	}
	src, err := m.source(it)
	if err != nil {
		return core.Status{}, true, err
	}
	st, err := src.Status(ctx, upkitplugin.SourceAppRequest{
		AppID:       appID,
		InstallPath: ref.InstallPath,
		Runtime:     m.runtime(it),
	})
	if err != nil {
		if errors.Is(err, upkitplugin.ErrNotSupported) {
			// 插件接管了安装却没实现 Status：当作「没装」，让检查流程继续走。
			// 探测结果为空不等于错误 —— 报错会让整个软件在界面上消失。
			return core.Status{}, false, nil
		}
		return core.Status{}, true, err
	}
	return core.Status{
		Installed: st.Installed,
		Version:   st.Version,
		Path:      st.Path,
		Source:    KindPlugin,
	}, true, nil
}

// takeover 判断插件是否接管了该软件的安装，是则返回来源与插件内软件 ID。
//
// 两条途径等价：插件级声明 CapabilityFull（全部软件都接管），或单个软件在
// Defaults.Method 里写 MethodPlugin（只接管它自己）。
func (m *Manager) takeover(ref core.AppRef) (*item, string, bool) {
	pref, err := apps.ParsePluginRef(ref)
	if err != nil {
		return nil, "", false
	}
	it, err := m.lookup(pref.SourceID)
	if err != nil {
		return nil, "", false
	}
	if !m.hasCapability(it, upkitplugin.CapabilityFull) && !m.softwareTakesOver(it, pref.AppID) {
		return nil, "", false
	}
	return it, pref.AppID, true
}

// softwareTakesOver 报告某个软件是否自己声明了接管安装（Defaults.Method = plugin）。
func (m *Manager) softwareTakesOver(it *item, appID string) bool {
	m.mu.RLock()
	apps := it.apps
	m.mu.RUnlock()
	for _, a := range apps {
		if a.ID == appID {
			return a.Defaults.Method == upkitplugin.MethodPlugin
		}
	}
	return false
}

// hasCapability 报告插件是否声明了某项能力。
func (m *Manager) hasCapability(it *item, capability string) bool {
	m.mu.RLock()
	info := it.info
	m.mu.RUnlock()
	for _, c := range info.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// source 取出可调用的插件对象。
func (m *Manager) source(it *item) (upkitplugin.Source, error) {
	client, err := m.client(it)
	if err != nil {
		return nil, err
	}
	return client.Source(), nil
}

// ── core.InstallMethod 的插件实现 ─────────────────────────────

// appMethod 把插件的一套 full 模式方法适配成宿主的安装方式。
//
// 它不自带任何下载与落地逻辑：取包、解压、替换全部由插件完成，宿主只提供
// 工作目录、参数与事件通道（core.Caps.SelfContained）。
type appMethod struct {
	mgr    *Manager
	it     *item
	appRef core.AppRef
	appID  string
}

var _ core.InstallMethod = (*appMethod)(nil)

// Name 返回适配器名，形如 plugin:corp-index。
func (a *appMethod) Name() string { return pluginMethodPrefix + a.it.entry.ID }

// Caps 声明能力：自包含、可指定安装路径、静默、可回滚。
func (a *appMethod) Caps() core.Caps {
	return core.Caps{
		SelfContained: true,
		CustomPath:    true,
		Silent:        true,
		Rollbackable:  true,
	}
}

// Plan 让插件给出一份计划，并翻译成宿主领域类型。
func (a *appMethod) Plan(ctx context.Context, req core.Request) (core.Plan, error) {
	src, err := a.mgr.source(a.it)
	if err != nil {
		return core.Plan{}, err
	}
	res, err := src.Plan(ctx, upkitplugin.SourcePlanRequest{
		SourceAppRequest: a.appRequest(),
		Plan:             toPluginPlanRequest(req),
	})
	if err != nil {
		return core.Plan{}, err
	}
	return toCorePlan(req, res), nil
}

// Execute 让插件执行安装，期间并发拉取进度事件。
//
// 事件是「插件缓冲 + 宿主轮询」而不是插件反向回调宿主：这样插件的 Apply 无论
// 跑多久都不会因为上报而阻塞，宿主也不必向插件暴露回调地址。
func (a *appMethod) Execute(ctx context.Context, req core.Request, sink core.EventSink) (core.Result, error) {
	src, err := a.mgr.source(a.it)
	if err != nil {
		return core.Result{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobID := newJobID(a.it.entry.ID, a.appID)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.pumpEvents(src, jobID, req.App.ID, sink, runCtx.Done(), stop)
	}()

	res, err := src.Apply(runCtx, upkitplugin.SourcePlanRequest{
		SourceAppRequest: a.appRequest(),
		JobID:            jobID,
		Plan:             toPluginPlanRequest(req),
	})

	close(stop)
	wg.Wait() // 收尾拉取完成后才返回，保证事件不丢

	if err != nil {
		// 宿主侧取消（超时、退出）时，插件进程并不会自动停下 —— 它可能正在
		// 替换文件。必须显式通知它中止，否则会出现「宿主已放弃、插件仍在写盘」。
		if ctx.Err() != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), cancelTimeout)
			defer stopCancel()
			_ = src.Cancel(stopCtx, jobID)
		}
		return core.Result{}, err
	}
	return toCoreResult(req, res), nil
}

// Uninstall 交给插件卸载。
func (a *appMethod) Uninstall(ctx context.Context, req core.Request, opts core.UninstallOptions) error {
	if opts.DryRun {
		return nil
	}
	src, err := a.mgr.source(a.it)
	if err != nil {
		return err
	}
	return src.Uninstall(ctx, upkitplugin.SourceUninstallRequest{
		SourceAppRequest: a.appRequest(),
		KeepUserData:     opts.KeepUserData,
	})
}

// Rollback 交给插件回滚到指定备份。
func (a *appMethod) Rollback(ctx context.Context, req core.Request, backupPath string) error {
	src, err := a.mgr.source(a.it)
	if err != nil {
		return err
	}
	return src.Rollback(ctx, upkitplugin.SourceRollbackRequest{
		SourceAppRequest: a.appRequest(),
		BackupPath:       backupPath,
	})
}

// Backups 返回空列表：SDK 目前没有「枚举备份」的能力。
//
// 插件自己管理的备份无法在宿主侧发现，界面只会显示「没有可回滚的备份」；
// 这比伪造一份假列表诚实。
func (a *appMethod) Backups(context.Context, core.Request) ([]core.Backup, error) {
	return nil, nil
}

// appRequest 组装每次调用都要带上的软件级上下文。
func (a *appMethod) appRequest() upkitplugin.SourceAppRequest {
	return upkitplugin.SourceAppRequest{
		AppID:       a.appID,
		InstallPath: a.appRef.InstallPath,
		Runtime:     a.mgr.runtime(a.it),
	}
}

// ── 事件桥接 ──────────────────────────────────────────────────

// pumpEvents 周期性拉取插件事件，直到任务结束或调用方取消。
func (a *appMethod) pumpEvents(
	src upkitplugin.Source, jobID, appID string, sink core.EventSink,
	cancelled <-chan struct{}, stop <-chan struct{},
) {
	if sink == nil {
		return
	}
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// 收尾：把插件缓冲里剩下的事件取干净，否则最后一条失败原因会丢。
			a.drain(src, jobID, appID, sink)
			return
		case <-cancelled:
			// 调用方已取消：再拉一次意义不大，插件侧也在被 Cancel。
			return
		case <-ticker.C:
			a.drain(src, jobID, appID, sink)
		}
	}
}

// drain 拉取一批事件并转发。
func (a *appMethod) drain(src upkitplugin.Source, jobID, appID string, sink core.EventSink) {
	ctx, cancel := context.WithTimeout(context.Background(), eventPollTimeout)
	defer cancel()

	events, err := src.PollEvents(ctx, jobID)
	if err != nil || len(events) == 0 {
		return // 拉取失败不致命：下一个周期会重试
	}
	for _, e := range events {
		sink.Emit(toCoreEvent(appID, jobID, e))
	}
}

// newJobID 生成一次任务的标识，用于取消与事件拉取。
func newJobID(sourceID, appID string) string {
	return fmt.Sprintf("%s%s%s@%d", sourceID, apps.QualifiedIDSeparator, appID, time.Now().UnixNano())
}

// ── 领域类型翻译 ──────────────────────────────────────────────

func toPluginPlanRequest(req core.Request) upkitplugin.PlanRequest {
	p := req.Plan
	return upkitplugin.PlanRequest{
		From:        p.From,
		To:          p.To,
		Release:     toPluginRelease(p.Release),
		Artifact:    toPluginArtifact(p.Artifact),
		InstallPath: req.App.InstallPath,
		WorkDir:     req.WorkDir,
		CacheDir:    req.CacheDir,
	}
}

func toPluginRelease(r core.Release) upkitplugin.Release {
	arts := make([]upkitplugin.Artifact, 0, len(r.Artifacts))
	for _, a := range r.Artifacts {
		arts = append(arts, toPluginArtifact(a))
	}
	return upkitplugin.Release{
		Version:     r.Version,
		Tag:         r.Tag,
		Channel:     r.Channel,
		PublishedAt: r.PublishedAt,
		Notes:       r.Notes,
		Artifacts:   arts,
	}
}

func toPluginArtifact(a core.Artifact) upkitplugin.Artifact {
	return upkitplugin.Artifact{Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest}
}

// toCorePlan 把插件的计划翻译成宿主计划。
//
// 插件没填的字段一律回落到宿主算出来的值：宿主侧已经比对过本机与上游版本，
// 那是 Action / From / To 的可信来源。
func toCorePlan(req core.Request, r upkitplugin.PlanResult) core.Plan {
	p := core.Plan{
		App:      req.App,
		Action:   core.Action(r.Action),
		From:     r.From,
		To:       r.To,
		Release:  ToCoreRelease(r.Release),
		Artifact: toCoreArtifact(r.Artifact),
		Steps:    toCoreSteps(r.Steps),
		Note:     r.Note,
		Size:     r.Artifact.Size,
	}
	if p.Action == "" {
		p.Action = req.Plan.Action
	}
	if p.From == "" {
		p.From = req.Plan.From
	}
	if p.To == "" {
		p.To = firstNonEmpty(r.To, p.Release.Version, req.Plan.To)
	}
	if p.To != "" && p.Release.Version == "" {
		p.Release.Version = p.To
	}
	return p
}

// toCoreResult 把插件的执行结果翻译成宿主结果。
func toCoreResult(req core.Request, r upkitplugin.Result) core.Result {
	res := core.Result{
		Action:      core.Action(r.Action),
		From:        r.From,
		To:          r.To,
		InstallPath: r.InstallPath,
		BackupPath:  r.BackupPath,
		Elapsed:     time.Duration(r.ElapsedMS) * time.Millisecond,
	}
	if res.Action == "" {
		res.Action = req.Plan.Action
	}
	if res.From == "" {
		res.From = req.Plan.From
	}
	if res.To == "" {
		res.To = req.Plan.To
	}
	if res.InstallPath == "" {
		res.InstallPath = req.App.InstallPath
	}
	return res
}

func toCoreArtifact(a upkitplugin.Artifact) core.Artifact {
	return core.Artifact{Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest}
}

func toCoreSteps(steps []upkitplugin.Step) []core.Step {
	out := make([]core.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, core.Step{
			Kind:     core.StepKind(s.Kind),
			Desc:     s.Desc,
			Command:  append([]string(nil), s.Command...),
			Critical: true, // 插件给出的步骤都是它自己的关键路径
		})
	}
	return out
}

// toCoreEvent 把插件事件翻译成宿主事件。
func toCoreEvent(appID, runID string, e upkitplugin.Event) core.Event {
	ev := core.Event{
		AppID: appID,
		RunID: runID,
		Kind:  core.EventKind(e.Kind),
		Phase: e.Phase,
		Done:  e.Done,
		Total: e.Total,
		Msg:   e.Msg,
		At:    time.Now(),
	}
	if ev.Kind == "" {
		ev.Kind = core.EventLog
	}
	if e.Level == "" {
		ev.Level = core.LevelInfo
	} else {
		ev.Level = core.LogLevel(e.Level)
	}
	return ev
}
