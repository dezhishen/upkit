package method

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/util"
	"github.com/dezhishen/upkit/internal/version"
)

// KindPortableInPlace 是「绿色版原目录覆盖」安装方式。
const KindPortableInPlace = "portable-inplace"

// PortableInPlace 实现绿色版安装：备份 → 清理 → 覆盖 → 复核，失败自动回滚。
type PortableInPlace struct {
	app  core.AppRef
	deps registry.Deps
}

// NewPortableInPlace 构造适配器。
func NewPortableInPlace(app core.AppRef, deps registry.Deps) (core.InstallMethod, error) {
	return &PortableInPlace{app: app, deps: deps}, nil
}

// Name 实现 core.InstallMethod。
func (m *PortableInPlace) Name() string { return KindPortableInPlace }

// Caps 实现 core.InstallMethod。
func (m *PortableInPlace) Caps() core.Caps {
	return core.Caps{NeedsUnpack: true, CustomPath: true, Silent: true, Rollbackable: true}
}

// Plan 产出可展示的步骤清单。
func (m *PortableInPlace) Plan(_ context.Context, req core.Request) (core.Plan, error) {
	p := req.Plan
	if p.Action == core.ActionNoOp {
		return p, nil
	}
	preserve := "（无）"
	if len(m.app.Preserve) > 0 {
		preserve = strings.Join(m.app.Preserve, ", ")
	}
	p.Note = "绿色版：先备份旧版本，再清理旧文件（保留保护路径），最后写入新文件"
	p.Steps = []core.Step{
		{Kind: core.StepRemove, Desc: fmt.Sprintf("清理 %s 下的旧文件（保留 %s）", m.app.InstallPath, preserve), Critical: true},
		{Kind: core.StepCopy, Desc: "复制新版本文件到 " + m.app.InstallPath, Critical: true},
		{Kind: core.StepVerify, Desc: "复核入口文件存在", Critical: true},
	}
	return p, nil
}

// Execute 实现核心安装流程。
func (m *PortableInPlace) Execute(ctx context.Context, req core.Request, sink core.EventSink) (core.Result, error) {
	start := time.Now()
	app := m.app
	if req.SourceRoot == "" {
		return core.Result{}, errors.New("缺少解包后的安装源目录")
	}
	if err := util.EnsureDir(app.InstallPath); err != nil {
		return core.Result{}, fmt.Errorf("创建安装目录: %w", err)
	}
	skip := fsutil.SkipMatcher(app.Preserve)
	logf := func(format string, a ...any) {
		sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelDebug, Msg: fmt.Sprintf(format, a...)})
	}

	var backup *core.Backup
	if req.KeepBackup && util.DirExists(app.InstallPath) {
		if empty, _ := fsutil.IsEmptyDir(app.InstallPath); !empty {
			sink.Emit(core.Event{Kind: core.EventPhase, Phase: "备份", Msg: "备份当前版本"})
			b, err := createBackup(app.InstallPath, req.BackupDir, req.Plan.From, skip, logf)
			if err != nil {
				return core.Result{}, err
			}
			backup = b
			sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelInfo,
				Msg: fmt.Sprintf("备份完成：%s（%s）", b.Path, util.HumanBytes(b.Size))})
		}
	}

	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "替换", Msg: "清理旧文件并写入新版本"})
	if err := fsutil.RemoveContents(app.InstallPath, app.Preserve); err != nil {
		return core.Result{}, m.rollback(backup, sink, fmt.Errorf("清理旧文件: %w", err))
	}
	if err := fsutil.CopyDir(req.SourceRoot, app.InstallPath, skip); err != nil {
		return core.Result{}, m.rollback(backup, sink, fmt.Errorf("写入新文件: %w", err))
	}
	if err := verifyEntrypoints(app); err != nil {
		return core.Result{}, m.rollback(backup, sink, err)
	}

	if err := ctx.Err(); err != nil {
		return core.Result{}, err
	}
	pruneBackups(req.BackupDir, req.MaxBackups, logf)

	res := core.Result{
		Action:      req.Plan.Action,
		From:        req.Plan.From,
		To:          req.Plan.To,
		InstallPath: app.InstallPath,
		Elapsed:     time.Since(start),
	}
	if backup != nil {
		res.BackupPath = backup.Path
	}
	return res, nil
}

// rollback 用备份恢复安装目录。
func (m *PortableInPlace) rollback(backup *core.Backup, sink core.EventSink, cause error) error {
	if backup == nil {
		return cause
	}
	sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelWarn, Msg: "替换失败，正在回滚：" + cause.Error()})
	if err := fsutil.RemoveContents(m.app.InstallPath, m.app.Preserve); err != nil {
		return fmt.Errorf("%w；%w（清理出错: %v），备份保留在 %s",
			cause, core.ErrRollbackFailed, err, backup.Path)
	}
	if err := fsutil.CopyDir(backup.Path, m.app.InstallPath, fsutil.SkipMatcher(m.app.Preserve)); err != nil {
		return fmt.Errorf("%w；%w（恢复出错: %v），备份保留在 %s",
			cause, core.ErrRollbackFailed, err, backup.Path)
	}
	sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelInfo,
		Msg: "已回滚到更新前状态（备份: " + backup.Path + "）"})
	return cause
}

// Uninstall 删除程序文件；KeepUserData 为真时保留保护路径。
func (m *PortableInPlace) Uninstall(_ context.Context, req core.Request, opts UninstallOptions) error {
	keep := m.app.Preserve
	if !opts.KeepUserData {
		keep = nil
	}
	if err := fsutil.RemoveContents(m.app.InstallPath, keep); err != nil {
		return err
	}
	if err := os.Remove(version.RecordPath(m.app.InstallPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除状态文件: %w", err)
	}
	return nil
}

// Rollback 恢复到指定备份。
func (m *PortableInPlace) Rollback(_ context.Context, _ core.Request, backupPath string) error {
	if strings.TrimSpace(backupPath) == "" {
		return errors.New("未指定备份路径")
	}
	if !util.DirExists(backupPath) {
		return fmt.Errorf("备份目录不存在: %s", backupPath)
	}
	if err := fsutil.RemoveContents(m.app.InstallPath, m.app.Preserve); err != nil {
		return err
	}
	return fsutil.CopyDir(backupPath, m.app.InstallPath, fsutil.SkipMatcher(m.app.Preserve))
}

// Backups 列出该软件的备份。
func (m *PortableInPlace) Backups(_ context.Context, req core.Request) ([]core.Backup, error) {
	return listBackups(req.BackupDir)
}

// UninstallOptions 重导出，便于调用方引用。
type UninstallOptions = core.UninstallOptions
