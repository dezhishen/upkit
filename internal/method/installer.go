package method

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/util"
)

// 已注册的安装器类安装方式。
const (
	KindExeInstaller = "exe-installer"
	KindMSIExec      = "msiexec"
)

// ExeInstaller 通过静默参数调用安装器（NSIS / Inno / Squirrel / 各类 setup.exe）。
//
// 选项：args（静默参数，逗号分隔）、elevate、uninstall（卸载命令模板）、timeout_seconds。
//
// 注意：安装器类软件**不可回滚**（Caps.Rollbackable=false），降级只能卸载后装旧版。
type ExeInstaller struct {
	app  core.AppRef
	deps registry.Deps
}

// NewExeInstaller 构造适配器。
func NewExeInstaller(app core.AppRef, deps registry.Deps) (core.InstallMethod, error) {
	return &ExeInstaller{app: app, deps: deps}, nil
}

// Name 实现 core.InstallMethod。
func (m *ExeInstaller) Name() string { return KindExeInstaller }

// Caps 实现 core.InstallMethod。
func (m *ExeInstaller) Caps() core.Caps {
	return core.Caps{
		NeedsUnpack:    false,
		CustomPath:     truthy(m.app.MethodOpts["custom_path"]),
		Silent:         true,
		Rollbackable:   false,
		NeedsElevation: truthy(m.app.MethodOpts["elevate"]),
	}
}

// Plan 生成「执行安装器 → 复核」的步骤。
func (m *ExeInstaller) Plan(_ context.Context, req core.Request) (core.Plan, error) {
	p := req.Plan
	if p.Action == core.ActionNoOp {
		return p, nil
	}
	p.Note = "调用安装器静默安装；安装器类软件无法自动回滚，升级后不能自动降级"
	p.Steps = []core.Step{
		{Kind: core.StepRun, Desc: "执行安装器", Command: m.command(req, p.Artifact.Name, p.To), Critical: true},
		{Kind: core.StepVerify, Desc: "安装后复核", Critical: false},
	}
	return p, nil
}

// Execute 执行安装器并等待退出。
func (m *ExeInstaller) Execute(ctx context.Context, req core.Request, sink core.EventSink) (core.Result, error) {
	start := time.Now()
	if req.ArtifactPath == "" {
		return core.Result{}, errors.New("缺少安装器文件")
	}
	cmd := m.command(req, req.ArtifactPath, req.Plan.To)
	step := core.Step{Kind: core.StepRun, Desc: "执行安装器", Command: cmd, Critical: true}

	runCtx := ctx
	if secs := atoiSafe(m.app.MethodOpts["timeout_seconds"]); secs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(secs)*time.Second)
		defer cancel()
	}

	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "安装",
		Msg: "执行：" + util.Truncate(joinCommand(cmd), 120)})
	if err := runStep(runCtx, step, req.WorkDir, sink); err != nil {
		return core.Result{}, err
	}

	// 安装器经常「静默失败但退出码为 0」，因此必须复核。
	if err := verifyEntrypoints(m.app); err != nil {
		if len(m.app.Entrypoints) == 0 {
			sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelWarn,
				Msg: "未声明 entrypoints，无法复核安装结果，请人工确认"})
		} else {
			return core.Result{}, fmt.Errorf("%w：%s 未找到 %s",
				core.ErrVerifyFailed, m.app.InstallPath, strings.Join(m.app.Entrypoints, "/"))
		}
	}

	return core.Result{
		Action:      req.Plan.Action,
		From:        req.Plan.From,
		To:          req.Plan.To,
		InstallPath: m.app.InstallPath,
		Elapsed:     time.Since(start),
	}, nil
}

// command 组装安装命令；artifact 为占位文件名或真实路径。
func (m *ExeInstaller) command(req core.Request, artifact, ver string) []string {
	args := splitList(m.app.MethodOpts["args"])
	if len(args) == 0 {
		args = []string{"/S"}
	}
	cmd := []string{artifact}
	cmd = append(cmd, parseArgs(args, m.app, req, ver)...)
	return cmd
}

// Uninstall 调用声明的卸载命令；未声明时返回不支持。
func (m *ExeInstaller) Uninstall(ctx context.Context, req core.Request, _ core.UninstallOptions) error {
	line := m.app.MethodOpts["uninstall"]
	if line == "" {
		return fmt.Errorf("%w: %s 未声明 method.uninstall", core.ErrUnsupported, m.app.ID)
	}
	fields := splitList(line)
	if len(fields) == 0 {
		return fmt.Errorf("%w: 卸载命令为空", core.ErrUnsupported)
	}
	cmd := parseArgs(fields, m.app, req, req.Plan.From)
	return runStep(ctx, core.Step{Kind: core.StepRun, Desc: "卸载", Command: cmd, Critical: true}, req.WorkDir, core.NopSink{})
}

// Rollback 安装器类不支持回滚。
func (m *ExeInstaller) Rollback(context.Context, core.Request, string) error {
	return fmt.Errorf("%w: %s 使用安装器，无法自动回滚（可卸载后安装旧版本）", core.ErrUnsupported, KindExeInstaller)
}

// Backups 安装器类没有文件树备份。
func (m *ExeInstaller) Backups(context.Context, core.Request) ([]core.Backup, error) { return nil, nil }

// MSIExec 通过 msiexec 安装 / 卸载 MSI 包。
//
// 选项：args（默认 /qn /norestart）、uninstall_args、timeout_seconds。
type MSIExec struct {
	app  core.AppRef
	deps registry.Deps
}

// NewMSIExec 构造适配器。
func NewMSIExec(app core.AppRef, deps registry.Deps) (core.InstallMethod, error) {
	return &MSIExec{app: app, deps: deps}, nil
}

// Name 实现 core.InstallMethod。
func (m *MSIExec) Name() string { return KindMSIExec }

// Caps 实现 core.InstallMethod。
func (m *MSIExec) Caps() core.Caps {
	return core.Caps{NeedsUnpack: false, CustomPath: true, Silent: true, Rollbackable: false}
}

// Plan 生成 msiexec 安装步骤。
func (m *MSIExec) Plan(_ context.Context, req core.Request) (core.Plan, error) {
	p := req.Plan
	if p.Action == core.ActionNoOp {
		return p, nil
	}
	p.Note = "调用 msiexec 安装 MSI；不支持自动回滚"
	p.Steps = []core.Step{
		{Kind: core.StepRun, Desc: "msiexec 安装", Command: m.installCommand(req, p.Artifact.Name, p.To), Critical: true},
		{Kind: core.StepVerify, Desc: "安装后复核", Critical: false},
	}
	return p, nil
}

// Execute 执行 msiexec。
func (m *MSIExec) Execute(ctx context.Context, req core.Request, sink core.EventSink) (core.Result, error) {
	start := time.Now()
	if req.ArtifactPath == "" {
		return core.Result{}, errors.New("缺少 MSI 文件")
	}
	cmd := m.installCommand(req, req.ArtifactPath, req.Plan.To)
	runCtx := ctx
	if secs := atoiSafe(m.app.MethodOpts["timeout_seconds"]); secs > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(secs)*time.Second)
		defer cancel()
	}
	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "安装", Msg: "执行：" + joinCommand(cmd)})
	if err := runStep(runCtx, core.Step{Kind: core.StepRun, Desc: "msiexec", Command: cmd, Critical: true}, req.WorkDir, sink); err != nil {
		return core.Result{}, err
	}
	if err := verifyEntrypoints(m.app); err != nil && len(m.app.Entrypoints) > 0 {
		return core.Result{}, fmt.Errorf("%w：msiexec 返回成功但 %s 未找到 %s",
			core.ErrVerifyFailed, m.app.InstallPath, strings.Join(m.app.Entrypoints, "/"))
	}
	return core.Result{Action: req.Plan.Action, From: req.Plan.From, To: req.Plan.To,
		InstallPath: m.app.InstallPath, Elapsed: time.Since(start)}, nil
}

func (m *MSIExec) installCommand(req core.Request, artifact, ver string) []string {
	args := splitList(m.app.MethodOpts["args"])
	if len(args) == 0 {
		args = []string{"/qn", "/norestart"}
	}
	cmd := []string{"msiexec", "/i", artifact}
	return append(cmd, parseArgs(args, m.app, req, ver)...)
}

// Uninstall 调用 msiexec /x。
func (m *MSIExec) Uninstall(ctx context.Context, req core.Request, _ core.UninstallOptions) error {
	if strings.TrimSpace(req.ArtifactPath) == "" {
		return fmt.Errorf("%w: %s 的 MSI 文件路径为空，无法卸载（请先重新下载）", core.ErrNotFound, m.app.ID)
	}
	args := splitList(m.app.MethodOpts["uninstall_args"])
	if len(args) == 0 {
		args = []string{"/qn", "/norestart"}
	}
	cmd := []string{"msiexec", "/x", req.ArtifactPath}
	cmd = append(cmd, parseArgs(args, m.app, req, "")...)
	return runStep(ctx, core.Step{Kind: core.StepRun, Desc: "msiexec 卸载", Command: cmd, Critical: true}, req.WorkDir, core.NopSink{})
}

// Rollback MSI 不支持自动回滚。
func (m *MSIExec) Rollback(context.Context, core.Request, string) error {
	return fmt.Errorf("%w: %s 无法自动回滚", core.ErrUnsupported, KindMSIExec)
}

// Backups MSI 没有文件树备份。
func (m *MSIExec) Backups(context.Context, core.Request) ([]core.Backup, error) { return nil, nil }

func joinCommand(cmd []string) string {
	var b strings.Builder
	for i, c := range cmd {
		if i > 0 {
			b.WriteByte(' ')
		}
		if containsSpace(c) {
			b.WriteByte('"')
			b.WriteString(c)
			b.WriteByte('"')
			continue
		}
		b.WriteString(c)
	}
	return b.String()
}

func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' {
			return true
		}
	}
	return false
}
