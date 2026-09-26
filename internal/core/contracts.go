package core

import (
	"context"
	"time"
)

// ── 适配器接口（四条轴）────────────────────────────────────────

// SourceResolver 回答「从哪拿、有哪些版本、下载什么」。
type SourceResolver interface {
	Name() string
	// Latest 返回最新版本；app.Pin 非空时应返回被固定的版本。
	Latest(ctx context.Context, app AppRef) (Release, error)
	// Versions 返回最近若干可选版本，按新 → 旧排序。
	Versions(ctx context.Context, app AppRef, limit int) ([]Release, error)
}

// Unpacker 回答「下载物怎么变成文件树」。
type Unpacker interface {
	Name() string
	// Root 返回可供安装使用的根目录（可能等于解压目录，也可能更下一层）。
	Unpack(ctx context.Context, req UnpackRequest, sink EventSink) (UnpackResult, error)
}

// UnpackRequest 是解包请求。
type UnpackRequest struct {
	ArchivePath string
	DestDir     string
	Opts        map[string]string
}

// UnpackResult 是解包结果。
type UnpackResult struct {
	Root  string // 安装源根目录
	Files int
	Bytes int64
}

// Detector 回答「本机装的是哪个版本、装在哪」。
type Detector interface {
	Name() string
	// Detect 未安装时返回 Installed=false 且 err=nil。
	Detect(ctx context.Context, app AppRef) (Status, error)
}

// InstallMethod 回答「怎么装到这台机器上」。
type InstallMethod interface {
	Name() string
	Caps() Caps
	// Plan 是纯计算，产出可展示的步骤清单。
	Plan(ctx context.Context, req Request) (Plan, error)
	// Execute 幂等执行；失败时必须保证安装目录仍可用（自带回滚）。
	Execute(ctx context.Context, req Request, sink EventSink) (Result, error)
	Uninstall(ctx context.Context, req Request, opts UninstallOptions) error
	Rollback(ctx context.Context, req Request, backupPath string) error
	// Backups 列出该软件的备份（按时间升序）。
	Backups(ctx context.Context, req Request) ([]Backup, error)
}

// ── 可选能力 ──────────────────────────────────────────────────

// RuntimeGuard 负责替换前结束占用进程。
type RuntimeGuard interface {
	Blockers(ctx context.Context, app AppRef) ([]Blocker, error)
	StopBlockers(ctx context.Context, app AppRef, timeoutSeconds int, log func(string, ...any)) (int, error)
}

// Verifier 提供额外的校验值（如上游 .sha256 附件）。
type Verifier interface {
	ExpectedDigest(ctx context.Context, app AppRef, art Artifact) (string, error)
}

// Launcher 负责安装后启动。
type Launcher interface {
	Launch(ctx context.Context, app AppRef) error
}

// ── 前端解耦 ──────────────────────────────────────────────────

// EventKind 是事件类型。
type EventKind string

const (
	EventStarted  EventKind = "started"
	EventPhase    EventKind = "phase"
	EventProgress EventKind = "progress"
	EventLog      EventKind = "log"
	EventBlocked  EventKind = "blocked"
	EventFinished EventKind = "finished"
	EventFailed   EventKind = "failed"
)

// LogLevel 是日志级别。
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// Event 是运行时事件（TUI、日志、审计共用同一份语义）。
type Event struct {
	AppID string
	RunID string
	Kind  EventKind
	Phase string // 检查 / 下载 / 校验 / 解压 / 备份 / 替换
	Done  int64
	Total int64
	Speed float64
	Level LogLevel
	Msg   string
	Err   error
	At    time.Time
}

// EventSink 接收事件。
type EventSink interface {
	Emit(Event)
}

// SinkFunc 让普通函数充当 EventSink。
type SinkFunc func(Event)

// Emit 实现 EventSink。
func (f SinkFunc) Emit(e Event) {
	if f != nil {
		f(e)
	}
}

// NopSink 丢弃所有事件。
type NopSink struct{}

// Emit 实现 EventSink。
func (NopSink) Emit(Event) {}

// Logger 是 core 需要的最小日志接口（实现见 logging 包）。
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	With(kv ...any) Logger
}

// Prompter 负责所有交互；只有 TUI 实现它（单元测试用 fake）。
type Prompter interface {
	Confirm(ctx context.Context, title, message string) (bool, error)
}
