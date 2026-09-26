package plugin

import "time"

// Info 描述插件自身。宿主用它做兼容性判定、界面展示与信任记录。
type Info struct {
	// ID 是插件在本机的唯一标识，同时决定插件私有目录名
	// （<插件根目录>/<ID>/）。要求 ^[a-z0-9][a-z0-9._-]{0,63}$。
	ID string
	// Name 是展示名，如「企业软件源」。
	Name string
	// Version 是插件自身的版本号（语义版本）。
	Version string
	// APIVersion 是插件编译时使用的 SDK 协议版本，留空时由 Serve 自动填 APIVersion。
	APIVersion string
	// Vendor / Homepage / Description 用于界面展示。
	Vendor      string
	Homepage    string
	Description string
	// Capabilities 声明插件额外能力：CapabilityCatalog / CapabilityFull /
	// CapabilityConfigurable，留空时按 CapabilityCatalog 处理。
	Capabilities []string
}

// CapabilityConfigurable 表示插件实现了 Configurable 接口，宿主会展示配置表单。
const CapabilityConfigurable = "configurable"

// CapabilityFull 表示插件实现 Method 接口，自己接管安装（full 模式）。
const CapabilityFull = ModeFull

// CapabilityCatalog 表示插件只提供来源信息，安装由宿主内置四轴完成。
const CapabilityCatalog = ModeCatalog

// Software 描述插件管理的单个软件。它是纯数据，宿主据此生成清单条目。
type Software struct {
	ID          string   // 插件内唯一，宿主的限定 ID 为 <插件ID>/<软件ID>
	Name        string   // 展示名
	Description string   // 一句话说明
	Homepage    string   // 主页
	Tags        []string // 分组标签
	// Provides 是软身份（别名、上游 owner/repo 等），用于跨来源去重与冲突提示。
	Provides []string
	// Target 是对安装目标的建议（仅提示，非权威）。
	Target *TargetHint
	// Defaults 是该软件建议的默认值，用户在清单里写的同 ID 条目可以覆盖它。
	Defaults Defaults
}

// TargetHint 让宿主能在安装前发现「不同来源往同一位置写」的情况。
type TargetHint struct {
	PathTemplate string   // 如 "${ROOT}/CorpVPN"；支持 ${ROOT} ${LOCALAPPDATA} ${ARCH}
	Entrypoints  []string // 如 ["vpn.exe"]
	Processes    []string // 需要先关闭的进程名
	Preserve     []string // 升级时要保留的相对路径
}

// Release 是一条可安装的版本。
type Release struct {
	Version     string
	Tag         string
	Channel     string // stable / beta / ...
	PublishedAt time.Time
	Notes       string
	Artifacts   []Artifact
}

// Artifact 是发布物中的一个可下载文件。宿主按 Artifacts 里第一个匹配的条目下载。
type Artifact struct {
	Name   string // 文件名，如 setup-1.4.2.exe
	URL    string // 下载地址
	Size   int64  // 字节数，0 表示未知
	Digest string // "sha256:..."，可空
}

// Status 是本机已安装状态（full 模式使用）。
type Status struct {
	Installed bool
	Version   string
	Path      string
}

// Step 是安装计划中的一步（full 模式使用）。
type Step struct {
	Kind    string   // StepDownload / StepExtract / StepCopy / StepRemove / StepRun / StepVerify / StepSwitch
	Desc    string   // 人类可读说明
	Command []string // Kind=StepRun 时，dry-run 与日志会原样打印这条命令
}

// PlanResult 是插件给出的安装计划（full 模式使用）。
type PlanResult struct {
	Action   string // ActionInstall / ActionUpdate / ActionReinstall / ActionUninstall / ActionNoop
	From, To string
	Release  Release
	Artifact Artifact
	Steps    []Step
	Note     string
}

// Result 是一次执行的结果（full 模式使用）。
type Result struct {
	Action      string
	From, To    string
	InstallPath string
	BackupPath  string
	ElapsedMS   int64
}

// Event 是插件上报的进度/日志事件（full 模式使用）。
type Event struct {
	Kind  string // EventStarted / EventPhase / EventProgress / EventLog / EventBlocked / EventFinished / EventFailed
	Phase string // 检查 / 下载 / 校验 / 解压 / 备份 / 替换
	Done  int64
	Total int64
	Level string // LogLevelDebug / LogLevelInfo / LogLevelWarn / LogLevelError
	Msg   string
}

// ConfigSchema 由插件声明自己的配置项，宿主据此生成表单。
type ConfigSchema struct {
	Title  string
	Fields []ConfigField
}

// ConfigField 是一个配置项。
type ConfigField struct {
	Key      string
	Label    string
	Type     string // FieldString / FieldInt / FieldBool / FieldEnum / FieldPath / FieldDuration / FieldSecret
	Default  string
	Required bool
	Enum     []string
	Help     string
	Pattern  string
	Secret   bool
}

// ValidationResult 是配置校验结果。
type ValidationResult struct {
	OK       bool
	Messages []string
}
