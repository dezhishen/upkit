package plugin

import "context"

// RuntimeConfig 是宿主在调用软件级方法时提供的运行时上下文。
type RuntimeConfig struct {
	// Config 是该软件的配置（清单选项 + 用户在界面填写的值）。
	Config Config
	// DataDir / LogDir 是该软件的私有目录，宿主已保证存在且可写。
	DataDir string
	LogDir  string
}

// SourceVersionsRequest 是版本查询请求（Source 级）。
type SourceVersionsRequest struct {
	AppID   string
	Limit   int
	Runtime RuntimeConfig
}

// SourceAppRequest 是软件级请求的公共部分。
type SourceAppRequest struct {
	AppID string
	// InstallPath 是该软件在本机的安装目录（清单配置展开后的值）。
	//
	// full 模式的插件靠它探测状态与落地；宿主不去猜插件把文件放哪了。
	InstallPath string
	Runtime     RuntimeConfig
}

// SourcePlanRequest 是计划/执行请求。
type SourcePlanRequest struct {
	SourceAppRequest
	// jobID 由宿主分配，用于取消与事件拉取（full 模式）。
	// 必须导出：它在 JSON 载荷里跨进程传输。
	JobID string
	Plan  PlanRequest
}

// SourceRollbackRequest 是回滚请求。
type SourceRollbackRequest struct {
	SourceAppRequest
	BackupPath string
}

// SourceUninstallRequest 是卸载请求。
type SourceUninstallRequest struct {
	SourceAppRequest
	KeepUserData bool
}

// Source 是宿主看到的插件对象。
//
// full 模式的方法（Status/Plan/Apply/Rollback/Uninstall）在插件未实现时返回
// ErrNotSupported，宿主据此回退到内置四轴能力；不需要用 errors.Is 之外的方式判断。
type Source interface {
	// Info 返回插件自身信息，同时充当握手探测。
	Info(ctx context.Context) (Info, error)
	// List 返回该插件管理的全部软件。
	List(ctx context.Context) ([]Software, error)
	// Versions 返回某个软件的可选版本（新 → 旧）。
	Versions(ctx context.Context, req SourceVersionsRequest) ([]Release, error)

	// ── full 模式（catalog 插件返回 ErrNotSupported）──

	Status(ctx context.Context, req SourceAppRequest) (Status, error)
	Plan(ctx context.Context, req SourcePlanRequest) (PlanResult, error)
	// Apply 同步执行安装；执行期间的事件由宿主并发调用 PollEvents 拉取。
	Apply(ctx context.Context, req SourcePlanRequest) (Result, error)
	Rollback(ctx context.Context, req SourceRollbackRequest) error
	Uninstall(ctx context.Context, req SourceUninstallRequest) error
	// PollEvents 拉取自上次调用以来产生的事件（full 模式）。
	PollEvents(ctx context.Context, jobID string) ([]Event, error)

	// ── 可选配置能力 ──

	ConfigSchema(ctx context.Context) (ConfigSchema, error)
	ValidateConfig(ctx context.Context, cfg Config) (ValidationResult, error)
	Configure(ctx context.Context, cfg Config) error

	// ── 控制 ──

	// Ping 用于健康检查。
	Ping(ctx context.Context) error
	// Cancel 取消某个长任务。
	Cancel(ctx context.Context, jobID string) error
}
