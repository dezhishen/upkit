package plugin

// 本文件集中 SDK 里所有会「跨进程出现」的字符串常量。
//
// 它们是对外契约 —— 会出现在清单、描述文件、插件日志、事件流与配置表单里，
// 改动等于不兼容变更。因此一律在这里定义，实现代码与测试里都不再出现裸字面量。

// ── 日志（logfmt）──────────────────────────────────────────────
//
// 插件的 Logger 以 key=value 写 stderr，宿主的 pluginLogWriter 按这两个键还原
// 级别与正文，从而把插件日志并入 upkit 的日志流（轮转、脱敏、run 关联一并继承）。
const (
	LogKeyLevel   = "level"
	LogKeyMessage = "msg"
)

// 日志级别取值，插件与宿主共用同一套。
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// ── 事件 ──────────────────────────────────────────────────────

// Event.Kind 的取值（full 模式的行为与可观测性全靠它）。
const (
	EventStarted  = "started"
	EventPhase    = "phase"
	EventProgress = "progress"
	EventLog      = "log"
	EventBlocked  = "blocked"
	EventFinished = "finished"
	EventFailed   = "failed"
)

// ── 动作 ──────────────────────────────────────────────────────

// PlanResult.Action 与 Result.Action 的取值。
const (
	ActionInstall   = "install"
	ActionUpdate    = "update"
	ActionReinstall = "reinstall"
	ActionUninstall = "uninstall"
	ActionNoop      = "noop"
)

// ── 计划步骤 ──────────────────────────────────────────────────

// Step.Kind 的取值。Run 步骤会带完整命令行，宿主在 dry-run 与日志里原样打印，
// 这是安装器类软件能被信任的前提。
const (
	StepDownload = "download"
	StepExtract  = "extract"
	StepCopy     = "copy"
	StepRemove   = "remove"
	StepRun      = "run"
	StepVerify   = "verify"
	StepSwitch   = "switch"
)

// ── 配置字段类型 ───────────────────────────────────────────────

// ConfigField.Type 的取值，决定宿主生成的表单控件。
const (
	FieldString   = "string"
	FieldInt      = "int"
	FieldBool     = "bool"
	FieldEnum     = "enum"
	FieldPath     = "path"
	FieldDuration = "duration"
	FieldSecret   = "secret" // 掩码显示、日志脱敏、导出只留引用
)

// ── 模式 ──────────────────────────────────────────────────────
//
// 同一个取值有两个场景：Info.Capabilities 里声明插件能力，以及描述文件/清单里的
// mode 字段。两者必须一致，所以只定义一次。
const (
	// ModeCatalog：插件只提供来源信息（软件、版本、下载地址），
	// 下载 / 解包 / 落地 / 探测全部复用宿主内置的四条轴。
	ModeCatalog = "catalog"
	// ModeFull：插件自己接管 Plan / Apply / 回滚。
	ModeFull = "full"
)

// MethodPlugin 是「由插件自己安装」的安装方式名。
//
// 在 Software.Defaults.Method 里声明它，就表示该软件的取包、解压与落地都在插件
// 进程内完成（宿主不再下载、不解包）。前提是该软件实现了 Method 接口。
//
// 想一次性声明插件里的全部软件，用插件级的 Info.Capabilities = []string{CapabilityFull}；
// 两者等价，后者只是省去逐个软件声明。
const MethodPlugin = "plugin"

// Software.Defaults 现在是结构化类型（见 defaults.go）：插件作者直接填字段，
// 宿主按字段翻译成四条轴的配置，因此不再需要「键名」常量。
