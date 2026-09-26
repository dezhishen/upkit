package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/download"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/guard"
	"github.com/dezhishen/upkit/internal/process"
	"github.com/dezhishen/upkit/internal/util"
	"github.com/dezhishen/upkit/internal/version"
)

// minFreeSlack 是磁盘空间预检额外要求的余量（压缩包 + 解压 + 备份）。
const minFreeSlack = 512 << 20

// JobResult 是批量执行中单个软件的结果。
type JobResult struct {
	AppID  string
	Name   string
	Result *core.Result
	Err    error
}

// Plan 生成单个软件的更新计划（不联网、不写盘）。
func (e *Engine) Plan(ctx context.Context, id string) (*core.Plan, error) {
	a, err := e.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Action == core.ActionNoOp {
		return &core.Plan{App: a.Ref, Action: core.ActionNoOp, From: a.Status.Version, To: a.Release.Version,
			Note: a.Note}, nil
	}
	m, err := e.reg.Method(a.Ref, e.deps())
	if err != nil {
		return nil, err
	}
	// 自包含方式（如插件接管安装）自带取包逻辑，产物列表可以为空。
	selfContained := m.Caps().SelfContained
	if !selfContained && len(a.Release.Artifacts) == 0 {
		return nil, fmt.Errorf("%w: %s 的版本 %s 没有可用产物", core.ErrNotFound, a.Ref.ID, a.Release.Version)
	}
	base := core.Plan{
		App:     a.Ref,
		Action:  a.Action,
		From:    a.Status.Version,
		To:      a.Release.Version,
		Release: a.Release,
		Backup:  e.settings.Storage.BackupKeep > 0 && a.Status.Installed,
	}
	if !selfContained {
		base.Artifact = a.Release.Artifacts[0]
		base.Size = a.Release.Artifacts[0].Size
	}
	p, err := m.Plan(ctx, e.request(a.Ref, base))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Apply 执行单个软件的安装 / 更新。
func (e *Engine) Apply(ctx context.Context, id string) (*core.Result, error) {
	start := time.Now()
	a, err := e.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Action == core.ActionNoOp {
		return &core.Result{Action: core.ActionNoOp, From: a.Status.Version, To: a.Release.Version,
			AlreadyInPlace: true, Elapsed: time.Since(start)}, nil
	}

	pp, err := e.Plan(ctx, id)
	if err != nil {
		return nil, err
	}
	plan := *pp
	method, err := e.reg.Method(a.Ref, e.deps())
	if err != nil {
		return nil, err
	}

	e.emit(a.Ref.ID, core.Event{Kind: core.EventStarted, Level: core.LevelInfo,
		Msg: fmt.Sprintf("开始 %s：%s", actionVerb(plan.Action), a.Ref.DisplayName())})
	if e.opts.DryRun {
		return &core.Result{Action: plan.Action, From: plan.From, To: plan.To,
			AlreadyInPlace: false, Elapsed: time.Since(start)}, nil
	}

	// 1. 准备本次运行的工作目录（自包含方式也用它放中间产物）
	workDir, cleanup, err := e.workDir(a.Ref)
	if err != nil {
		return nil, e.fail(a.Ref.ID, err)
	}
	defer cleanup()

	caps := method.Caps()
	req := e.request(a.Ref, plan)
	if caps.SelfContained {
		req.WorkDir = workDir
	}

	var dlRes downloadResult
	if caps.SelfContained {
		// 自包含：取包、校验、解压、落地全在适配器内部完成，宿主只交参数。
		e.emit(a.Ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo,
			Msg: fmt.Sprintf("%s 由适配器自行获取并安装", a.Ref.DisplayName())})
	} else {
		// 2. 预检磁盘空间
		if err := e.checkSpace(a.Ref, plan); err != nil {
			return nil, e.fail(a.Ref.ID, err)
		}

		// 3. 取产物（命中缓存则跳过下载）
		dlRes, err = e.fetchArtifact(ctx, a.Ref, plan, workDir)
		if err != nil {
			return nil, e.fail(a.Ref.ID, err)
		}
		plan.ReusedCache = dlRes.Reused
		plan.Artifact.Name = filepath.Base(dlRes.Path)
		req.Plan = plan

		switch {
		case dlRes.Reused:
			e.emit(a.Ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo,
				Msg: fmt.Sprintf("复用已下载的安装包 %s（%s），跳过下载", filepath.Base(dlRes.Path), util.HumanBytes(dlRes.Size))})
		default:
			e.emit(a.Ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo,
				Msg: fmt.Sprintf("下载完成 %s（%s）", filepath.Base(dlRes.Path), util.HumanBytes(dlRes.Size))})
		}
		req.ArtifactPath = dlRes.Path
		req.Digest = dlRes.SHA256

		// 4. 解包
		if caps.NeedsUnpack {
			src, err := e.unpack(ctx, a.Ref, dlRes.Path, workDir)
			if err != nil {
				return nil, e.fail(a.Ref.ID, err)
			}
			req.SourceRoot = src
		} else {
			req.SourceRoot = dlRes.Path
		}
	}

	// 5. 结束占用进程
	if err := e.ensureNoBlockers(ctx, a.Ref); err != nil {
		return nil, e.fail(a.Ref.ID, err)
	}

	// 6. 确认
	if e.settings.Behavior.ConfirmBeforeApply && e.prompter != nil {
		ok, err := e.prompter.Confirm(ctx, fmt.Sprintf("确认%s %s？", actionVerb(plan.Action), a.Ref.DisplayName()),
			planSummary(plan))
		if err != nil {
			return nil, e.fail(a.Ref.ID, err)
		}
		if !ok {
			return nil, e.fail(a.Ref.ID, core.ErrUserAborted)
		}
	}

	// 7. 执行
	res, err := method.Execute(ctx, req, e.sink)
	if err != nil {
		e.audit(map[string]any{"action": "apply", "app": a.Ref.ID, "result": "failed", "error": err.Error()})
		return nil, e.fail(a.Ref.ID, err)
	}
	res.Downloaded = dlRes.Size
	res.SHA256 = dlRes.SHA256
	res.ReusedCache = dlRes.Reused
	res.Elapsed = time.Since(start)

	// 8. 记录状态 + 审计 + 收尾
	if err := e.recordState(a.Ref, plan, dlRes.SHA256); err != nil {
		e.log.Warn("写入状态文件失败", "app", a.Ref.ID, "error", err.Error())
	}
	e.audit(map[string]any{
		"action": string(plan.Action), "app": a.Ref.ID, "name": a.Ref.DisplayName(),
		"from": plan.From, "to": plan.To, "result": "success",
		"backup": res.BackupPath, "sha256": dlRes.SHA256, "reused_cache": dlRes.Reused,
	})
	e.pruneCache()
	e.launchIfNeeded(a.Ref)
	e.emit(a.Ref.ID, core.Event{Kind: core.EventFinished, Level: core.LevelInfo,
		Msg: fmt.Sprintf("%s 完成（%s）", a.Ref.DisplayName(), res.Elapsed)})
	return &res, nil
}

// ApplyMany 批量执行（并发受设置限制）。
func (e *Engine) ApplyMany(ctx context.Context, ids []string, concurrency int) []JobResult {
	if concurrency <= 0 {
		concurrency = e.settings.Engine.ApplyConcurrency
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	results := make([]JobResult, len(ids))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			a := e.Find(id)
			name := id
			if a != nil {
				name = a.Ref.DisplayName()
			}
			res, err := e.Apply(ctx, id)
			results[i] = JobResult{AppID: id, Name: name, Result: res, Err: err}
		}(i, id)
	}
	wg.Wait()
	return results
}

// Rollback 回滚到指定备份。
func (e *Engine) Rollback(ctx context.Context, id, backupPath string) error {
	if backupPath == "" {
		if backups := e.backupsOf(ctx, id); len(backups) > 0 {
			backupPath = backups[len(backups)-1].Path
		} else {
			return fmt.Errorf("%w: %s 没有可用的备份", core.ErrNotFound, id)
		}
	}
	a, err := e.ensure(ctx, id)
	if err != nil {
		return err
	}
	m, err := e.reg.Method(a.Ref, e.deps())
	if err != nil {
		return err
	}
	if !m.Caps().Rollbackable {
		return fmt.Errorf("%w: %s 使用的安装方式不支持回滚", core.ErrUnsupported, a.Ref.ID)
	}
	req := e.request(a.Ref, core.Plan{App: a.Ref, From: a.Status.Version})
	if err := m.Rollback(ctx, req, backupPath); err != nil {
		return err
	}
	e.audit(map[string]any{"action": "rollback", "app": a.Ref.ID, "backup": backupPath, "result": "success"})
	e.emit(a.Ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo, Msg: "已回滚到 " + backupPath})
	return nil
}

// Uninstall 卸载（KeepUserData 为真时保留用户数据）。
func (e *Engine) Uninstall(ctx context.Context, id string, keepUserData bool) error {
	a, err := e.ensure(ctx, id)
	if err != nil {
		return err
	}
	m, err := e.reg.Method(a.Ref, e.deps())
	if err != nil {
		return err
	}
	req := e.request(a.Ref, core.Plan{App: a.Ref, Action: core.ActionUninstall, From: a.Status.Version})
	if err := m.Uninstall(ctx, req, core.UninstallOptions{KeepUserData: keepUserData}); err != nil {
		return err
	}
	e.audit(map[string]any{"action": "uninstall", "app": a.Ref.ID,
		"keep_user_data": keepUserData, "result": "success"})
	e.emit(a.Ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo, Msg: "已卸载 " + a.Ref.DisplayName()})
	return nil
}

// Backups 返回某软件的备份列表。
func (e *Engine) Backups(ctx context.Context, id string) ([]core.Backup, error) {
	a, err := e.ensure(ctx, id)
	if err != nil {
		return nil, err
	}
	return e.backups(ctx, a.Ref)
}

func (e *Engine) backupsOf(ctx context.Context, id string) []core.Backup {
	a := e.Find(id)
	if a == nil {
		return nil
	}
	b, err := e.backups(ctx, a.Ref)
	if err != nil {
		return nil
	}
	return b
}

// ensure 取出条目并保证已完成一次检查。
func (e *Engine) ensure(ctx context.Context, id string) (*App, error) {
	if len(e.Apps()) == 0 {
		if _, err := e.List(ctx); err != nil {
			return nil, err
		}
	}
	a := e.Find(id)
	if a == nil {
		return nil, fmt.Errorf("%w: 未找到软件 %q", core.ErrNotFound, id)
	}
	if a.Shadowed {
		return nil, fmt.Errorf("%w: %s %s", core.ErrConflict, a.Ref.ID, a.Note)
	}
	if a.CheckErr != nil {
		return nil, a.CheckErr
	}
	if a.Release.Version == "" {
		if _, err := e.CheckOne(ctx, id); err != nil {
			return nil, err
		}
		a = e.Find(id)
		if a == nil || a.CheckErr != nil {
			if a != nil && a.CheckErr != nil {
				return nil, a.CheckErr
			}
			return nil, fmt.Errorf("%w: 未找到软件 %q", core.ErrNotFound, id)
		}
	}
	return a, nil
}

// checkSpace 预检磁盘空间（临时目录所在卷）。
func (e *Engine) checkSpace(ref core.AppRef, plan core.Plan) error {
	if plan.Size <= 0 {
		return nil
	}
	need := uint64(plan.Size)*2 + minFreeSlack
	if e.settings.Storage.MinFreeSpaceMB > 0 {
		if min := uint64(e.settings.Storage.MinFreeSpaceMB) << 20; need < min {
			need = min
		}
	}
	free, err := fsutil.DiskFree(e.settings.Storage.TempDir)
	if err != nil || free == 0 {
		return nil
	}
	if free < need {
		return fmt.Errorf("磁盘空间不足：%s 需要约 %s，当前可用 %s",
			e.settings.Storage.TempDir, util.HumanBytes(int64(need)), util.HumanBytes(int64(free)))
	}
	return nil
}

// downloadResult 是产物获取结果。
type downloadResult struct {
	Path   string
	Size   int64
	SHA256 string
	Reused bool
}

// fetchArtifact 取产物：优先复用缓存，否则下载并校验。
func (e *Engine) fetchArtifact(ctx context.Context, ref core.AppRef, plan core.Plan, _ string) (downloadResult, error) {
	art := plan.Artifact
	if art.URL == "" {
		return downloadResult{}, fmt.Errorf("%w: %s 缺少下载地址", core.ErrNotFound, ref.ID)
	}

	expected := ""
	if v, ok := e.verifier(ref); ok {
		sum, err := v.ExpectedDigest(ctx, ref, art)
		if err != nil {
			e.log.Warn("获取上游摘要失败", "app", ref.ID, "error", err.Error())
		}
		expected = sum
	}

	if res, ok := e.lookupCache(ref, art, expected); ok {
		return res, nil
	}

	dest := filepath.Join(e.settings.Storage.CacheDir, filepath.Base(art.Name))
	e.emit(ref.ID, core.Event{Kind: core.EventPhase, Phase: "下载", Level: core.LevelInfo,
		Msg: "下载 " + art.Name})
	dr, err := e.dl.Download(ctx, art.URL, dest, nil, func(done, total int64, speed float64, _ time.Duration) {
		e.emit(ref.ID, core.Event{Kind: core.EventProgress, Phase: "下载",
			Done: done, Total: total, Speed: speed})
	})
	if err != nil {
		return downloadResult{}, err
	}

	if expected != "" {
		e.emit(ref.ID, core.Event{Kind: core.EventPhase, Phase: "校验", Level: core.LevelInfo, Msg: "校验 SHA256"})
		if err := download.VerifySHA256(dr.Path, expected); err != nil {
			_ = fsutil.RemoveAll(dr.Path)
			return downloadResult{}, fmt.Errorf("%w: %v", core.ErrChecksum, err)
		}
	}
	return downloadResult{Path: dr.Path, Size: dr.Size, SHA256: dr.SHA256}, nil
}

// lookupCache 检查缓存里是否有可复用的产物。
func (e *Engine) lookupCache(ref core.AppRef, art core.Artifact, expected string) (downloadResult, bool) {
	path := filepath.Join(e.settings.Storage.CacheDir, filepath.Base(art.Name))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return downloadResult{}, false
	}
	if art.Size > 0 && info.Size() != art.Size {
		e.log.Warn("缓存大小不符，重新下载", "app", ref.ID,
			"local", util.HumanBytes(info.Size()), "upstream", util.HumanBytes(art.Size))
		_ = fsutil.RemoveAll(path)
		return downloadResult{}, false
	}
	sum := expected
	if sum == "" {
		if s, err := download.SHA256File(path); err == nil {
			sum = s
		}
	} else if err := download.VerifySHA256(path, sum); err != nil {
		e.log.Warn("缓存校验失败，重新下载", "app", ref.ID, "error", err.Error())
		_ = fsutil.RemoveAll(path)
		return downloadResult{}, false
	}
	return downloadResult{Path: path, Size: info.Size(), SHA256: sum, Reused: true}, true
}

// unpack 解包产物并返回安装源根目录。
func (e *Engine) unpack(ctx context.Context, ref core.AppRef, archivePath, workDir string) (string, error) {
	up, err := e.reg.Unpacker(ref, e.deps())
	if err != nil {
		return "", err
	}
	dest := filepath.Join(workDir, "extract")
	res, err := up.Unpack(ctx, core.UnpackRequest{
		ArchivePath: archivePath,
		DestDir:     dest,
		Opts:        ref.UnpackOpts,
	}, e.sink)
	if err != nil {
		return "", err
	}
	e.emit(ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelDebug,
		Msg: fmt.Sprintf("解压完成：%d 个文件，%s", res.Files, util.HumanBytes(res.Bytes))})
	return res.Root, nil
}

// ensureNoBlockers 结束占用安装目录的进程。
func (e *Engine) ensureNoBlockers(ctx context.Context, ref core.AppRef) error {
	g, err := guard.New(ref, e.deps())
	if err != nil {
		return err
	}
	blockers, err := g.Blockers(ctx, ref)
	if err != nil {
		e.log.Warn("枚举进程失败，跳过预检", "app", ref.ID, "error", err.Error())
		return nil
	}
	if len(blockers) == 0 {
		return nil
	}

	names := guard.Format(blockers)
	e.emit(ref.ID, core.Event{Kind: core.EventBlocked, Level: core.LevelWarn,
		Msg: fmt.Sprintf("检测到 %d 个进程正在使用 %s：%s", len(blockers), ref.InstallPath, names)})

	if e.settings.Behavior.WaitForClose && e.prompter != nil {
		ok, err := e.prompter.Confirm(ctx,
			fmt.Sprintf("%s 正在运行", ref.DisplayName()),
			"替换文件前需要关闭以下进程：\n\n"+names+"\n\n是否由 upkit 结束它们？")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 需要先关闭 %s", core.ErrBlocked, names)
		}
	}

	killed, err := g.StopBlockers(ctx, ref, 30, func(format string, a ...any) {
		e.log.Debug(fmt.Sprintf(format, a...), "app", ref.ID)
	})
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrBlocked, err)
	}
	e.emit(ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo,
		Msg: fmt.Sprintf("已结束 %d 个进程", killed)})
	return nil
}

// workDir 为本次操作创建独立临时目录。
func (e *Engine) workDir(ref core.AppRef) (string, func(), error) {
	dir, err := os.MkdirTemp(e.settings.Storage.TempDir, "upkit-"+ref.ID+"-")
	if err != nil {
		if err := util.EnsureDir(e.settings.Storage.TempDir); err != nil {
			return "", nil, fmt.Errorf("创建临时目录: %w", err)
		}
		dir, err = os.MkdirTemp(e.settings.Storage.TempDir, "upkit-"+ref.ID+"-")
		if err != nil {
			return "", nil, fmt.Errorf("创建临时目录: %w", err)
		}
	}
	return dir, func() { _ = fsutil.RemoveAll(dir) }, nil
}

// recordState 写入版本状态文件。
func (e *Engine) recordState(ref core.AppRef, plan core.Plan, sha string) error {
	return version.WriteRecord(ref.InstallPath, &version.Record{
		Version:     plan.To,
		Tag:         plan.Release.Tag,
		Asset:       plan.Artifact.Name,
		SHA256:      sha,
		Source:      plan.Release.Channel,
		InstalledAt: time.Now().UTC(),
		ToolVersion: "2",
	})
}

// pruneCache 按 cache_keep 清理最早的缓存包。
func (e *Engine) pruneCache() {
	keep := e.settings.Storage.CacheKeep
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(e.settings.Storage.CacheDir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	var items []item
	for _, en := range entries {
		if en.IsDir() || strings.HasSuffix(en.Name(), download.PartSuffix) {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(e.settings.Storage.CacheDir, en.Name()), info.ModTime(), info.Size()})
	}
	if len(items) <= keep {
		return
	}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].mod.After(items[j-1].mod); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	for _, it := range items[keep:] {
		_ = fsutil.RemoveAll(it.path)
	}
}

// launchIfNeeded 安装后启动（若启用）。
func (e *Engine) launchIfNeeded(ref core.AppRef) {
	if !e.settings.Behavior.LaunchAfterUpdate || len(ref.Entrypoints) == 0 {
		return
	}
	exe := filepath.Join(ref.InstallPath, ref.Entrypoints[0])
	if err := process.Start(exe); err != nil {
		e.log.Warn("启动失败", "app", ref.ID, "error", err.Error())
		return
	}
	e.emit(ref.ID, core.Event{Kind: core.EventLog, Level: core.LevelInfo, Msg: "已启动 " + ref.Entrypoints[0]})
}

// verifier 尝试把来源适配器当作校验值提供者。
func (e *Engine) verifier(ref core.AppRef) (core.Verifier, bool) {
	src, err := e.reg.Source(ref, e.deps())
	if err != nil {
		return nil, false
	}
	v, ok := src.(core.Verifier)
	return v, ok
}

func (e *Engine) emit(appID string, ev core.Event) {
	ev.AppID = appID
	ev.At = time.Now()
	e.sink.Emit(ev)
}

func (e *Engine) fail(appID string, err error) error {
	if errors.Is(err, core.ErrUserAborted) {
		return err
	}
	e.emit(appID, core.Event{Kind: core.EventFailed, Level: core.LevelError, Msg: err.Error(), Err: err})
	return err
}

func (e *Engine) audit(entry map[string]any) {
	if e.opts.Audit != nil {
		e.opts.Audit(entry)
	}
}

func actionVerb(a core.Action) string {
	switch a {
	case core.ActionInstall:
		return "安装"
	case core.ActionUpdate:
		return "更新"
	case core.ActionReinstall:
		return "重装"
	case core.ActionUninstall:
		return "卸载"
	default:
		return "处理"
	}
}

func planSummary(p core.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s：%s → %s\n", p.App.DisplayName(), displayVersion(p.From), p.To)
	fmt.Fprintf(&b, "安装目录：%s\n", p.App.InstallPath)
	if p.Artifact.Name != "" {
		fmt.Fprintf(&b, "安装包：%s（%s）\n", p.Artifact.Name, util.HumanBytes(p.Artifact.Size))
	}
	if p.Backup {
		b.WriteString("将先备份当前版本（失败自动回滚）\n")
	}
	if p.Note != "" {
		b.WriteString(p.Note)
		b.WriteString("\n")
	}
	if len(p.Steps) > 0 {
		b.WriteString("步骤：\n")
		for i, s := range p.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s.Desc)
			if len(s.Command) > 0 {
				fmt.Fprintf(&b, "     %s\n", strings.Join(s.Command, " "))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
