package plugin

import "context"

// App 是「插件里的一个软件」。
//
// catalog 模式下只需要实现 Versions：宿主会用它拿到版本与下载地址，然后走内置的
// 下载 → 校验 → 解包 → 落地 → 探测四轴完成安装。
//
// 实现必须并发安全：同一个 App 实例可能被宿主的多个检查任务同时调用。
type App interface {
	// Versions 返回该软件最近若干可选版本，按新 → 旧排序。
	// limit <= 0 表示由插件自行决定（建议返回最近 20 个）。
	Versions(ctx context.Context, req VersionsRequest) ([]Release, error)
}

// VersionsRequest 是版本查询请求。
type VersionsRequest struct {
	// Limit 是宿主期望的最大条数（0 = 不限）。
	Limit int
	// Config 是该软件的配置；用 DecodeConfig 解到结构体，或用 String/Int/Bool 取值。
	Config Config
}

// Method 是 full 模式的可选能力：插件自己接管安装流程。
//
// 实现它（并在 Info.Capabilities 里声明 CapabilityFull）后，宿主不再使用内置四轴，
// 而是把你的 Plan / Apply 结果直接展示与执行。只实现其中的方法也可以，未实现的
// 方法返回 ErrNotSupported，宿主会回退到内置能力。
type Method interface {
	Status(ctx context.Context, req StatusRequest) (Status, error)
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	// Apply 执行安装；通过 send 上报事件（进度、日志、阶段）。
	// send 可能为 nil（宿主不需要进度时），实现必须容忍。
	Apply(ctx context.Context, req PlanRequest, send EventSender) (Result, error)
	Rollback(ctx context.Context, req RollbackRequest) error
	Uninstall(ctx context.Context, req UninstallRequest) error
}

// StatusRequest 是状态查询请求。
type StatusRequest struct {
	// InstallPath 是清单里为该软件配置的安装目录（已展开变量）。
	//
	// 插件应当按它探测；插件自己另存的安装位置也应当汇报在这里。
	InstallPath string
	// DataDir 是插件的私有目录，可用来存放自管的状态记录。
	DataDir string
	Config  Config
}

// PlanRequest 是计划/执行请求。
type PlanRequest struct {
	From, To    string
	Release     Release
	Artifact    Artifact
	InstallPath string
	// WorkDir / CacheDir 由宿主提供，插件可以自由使用（推荐只写 WorkDir）。
	WorkDir  string
	CacheDir string
	// DataDir 是插件的私有目录（与 StatusRequest 里的一致）。
	DataDir string
	Config  Config
}

// RollbackRequest 是回滚请求。
type RollbackRequest struct {
	BackupPath  string
	InstallPath string
	Config      Config
}

// UninstallRequest 是卸载请求。
type UninstallRequest struct {
	KeepUserData bool
	InstallPath  string
	// DataDir 是插件的私有目录，卸载时可一并清理自己的状态。
	DataDir string
	Config  Config
}

// EventSender 由宿主提供，插件用它上报事件；调用是并发安全的。
type EventSender func(Event)

// Send 在 sender 为 nil 时安全地丢弃事件。
func (s EventSender) Send(e Event) {
	if s != nil {
		s(e)
	}
}

// Configurable 是可选能力：校验并热应用自己的配置。
//
// 配置项的「声明」用静态的 ConfigSchema（见 Register 的 WithConfigSchema），
// 因为声明不应该依赖实例能否构造成功；本接口只负责声明之后的校验与热应用。
// 由任一 App 实现即可，SDK 会把第一个实现者的行为当作插件级配置处理。
type Configurable interface {
	// ValidateConfig 在用户保存前做一次校验；返回 OK=false 时界面显示 Messages。
	ValidateConfig(ctx context.Context, cfg Config) (ValidationResult, error)
	// Configure 在配置落盘后热应用（如重建缓存）。返回错误时宿主会回滚配置文件。
	Configure(ctx context.Context, cfg Config) error
}

// AppConfig 是构造器收到的依赖。
type AppConfig struct {
	// Source 是插件自身的信息。
	Source Info
	// App 是该软件的元信息（与 List 返回的一致）。
	App Software
	// Config 是该软件的配置。
	Config Config
	// DataDir / LogDir 是该软件的私有目录，宿主已保证存在且可写。
	// 插件只应在 DataDir 下写自己的缓存与状态，配置文件由宿主独占读写。
	DataDir string
	LogDir  string
	// Log 转发到宿主日志（前缀 plugin/<插件ID>），级别与脱敏由宿主统一处理。
	Log Logger
}

// AppFactory 是「单一软件的构造器」。
//
// 宿主在首次需要该软件时调用一次，并把实例缓存复用；返回错误表示这个软件当前
// 不可用（例如缺少必填配置）——只影响该软件，不影响同一个插件里的其它软件。
type AppFactory func(AppConfig) (App, error)

// Logger 是插件可用的最小日志接口。实现内部会把 kv 格式化成文本再跨进程传输，
// 因此可以放心传任意值。
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}
