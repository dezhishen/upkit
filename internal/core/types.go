// Package core 定义领域模型与接口。
//
// 硬约束：本包只依赖标准库，不 import 任何其它内部包；所有适配器都必须面向这里
// 的接口编程，engine 也只认识这些接口，不认识 GitHub、zip 或 msiexec。
package core

import (
	"time"
)

// AppRef 是一个软件在本机的实例描述：身份 + 本机参数 + 各适配器选项。
//
// Options 里的键值来自 apps.yaml，已展开变量、已做基础校验。
type AppRef struct {
	ID   string // 稳定标识（单机内唯一）
	Name string

	Source     string // 适配器名：github-release
	SourceOpts map[string]string
	Unpack     string // zip | raw | tar.gz
	UnpackOpts map[string]string
	Method     string // portable-inplace | portable-sxs | exe-installer | msiexec | script
	MethodOpts map[string]string
	Detect     []string // 探测链，按顺序尝试

	InstallPath string
	Entrypoints []string
	Processes   []string
	Preserve    []string

	// Disabled 表示清单里被停用：不参与检查和更新，但**仍要出现在列表里** ——
	// 界面把它显示成灰色的「（停用）」，用户按空格就能再启用。
	// 用反义字段是为了让零值等于「启用」：新增构造点漏填不会让软件凭空停用。
	Disabled bool

	Pin           string // 固定版本，非空则忽略上游最新
	TrackRevision bool
	Tags          []string
}

// DisplayName 返回用于界面展示的名字。
func (a AppRef) DisplayName() string {
	if a.Name != "" {
		return a.Name
	}
	if a.ID != "" {
		return a.ID
	}
	return "unknown"
}

// Release 是一条可安装的版本。
type Release struct {
	Version     string
	Tag         string
	Channel     string
	PublishedAt time.Time
	Notes       string
	Artifacts   []Artifact
}

// Artifact 是发布物中的一个文件。
type Artifact struct {
	Name   string
	URL    string
	Size   int64
	Digest string // "sha256:..."，可为空
}

// Status 是本机已安装状态。
type Status struct {
	Installed   bool
	Version     string
	Path        string
	Source      string // 版本来源：state-file / pe-resource / dir-name / cli-version
	InstalledAt time.Time
	Size        int64
	Blockers    []Blocker
}

// Blocker 表示占用安装目录的进程。
type Blocker struct {
	PID  int
	Name string
	Path string
}

// Action 描述本次要做的事。
type Action string

const (
	ActionInstall   Action = "install"
	ActionUpdate    Action = "update"
	ActionReinstall Action = "reinstall"
	ActionUninstall Action = "uninstall"
	ActionNoOp      Action = "noop"
)

// Plan 是一次操作的完整计划（可展示、可 dry-run）。
type Plan struct {
	App         AppRef
	Action      Action
	From, To    string
	Release     Release
	Artifact    Artifact
	Steps       []Step
	ReusedCache bool
	Backup      bool
	Note        string
	Size        int64
}

// StepKind 是计划步骤的类型。
type StepKind string

const (
	StepDownload StepKind = "download"
	StepExtract  StepKind = "extract"
	StepCopy     StepKind = "copy"
	StepRemove   StepKind = "remove"
	StepRun      StepKind = "run"
	StepVerify   StepKind = "verify"
	StepSwitch   StepKind = "switch"
)

// Step 是计划中的一步。
type Step struct {
	Kind     StepKind
	Desc     string
	Command  []string // Run 步骤：dry-run 与日志会原样打印
	Critical bool
}

// Result 是一次操作的结果。
type Result struct {
	Action         Action
	From, To       string
	ReusedCache    bool
	Downloaded     int64
	SHA256         string
	BackupPath     string
	InstallPath    string
	Elapsed        time.Duration
	AlreadyInPlace bool
}

// Backup 描述一份备份。
type Backup struct {
	Path      string
	Version   string
	CreatedAt time.Time
	Size      int64
}

// Caps 声明安装方式的能力。
type Caps struct {
	NeedsUnpack    bool
	CustomPath     bool
	Silent         bool
	Rollbackable   bool
	PreUninstall   bool
	NeedsElevation bool

	// SelfContained 表示该方式自带取包与落地（如插件全权接管安装）。
	//
	// 为 true 时宿主不再做磁盘预检、不下载产物、不解包，只把工作目录、参数与
	// 事件通道交给适配器；Result 里的 Downloaded / SHA256 / ReusedCache 一律为空。
	SelfContained bool
}

// UninstallOptions 控制卸载行为。
type UninstallOptions struct {
	KeepUserData bool
	DryRun       bool
}

// Request 是交给适配器的一次执行请求。
type Request struct {
	App        AppRef
	Plan       Plan
	WorkDir    string // 本次运行的临时目录
	CacheDir   string
	BackupDir  string
	KeepBackup bool
	MaxBackups int
	Verify     bool
	Proxy      string
	Token      string
	Timeout    time.Duration

	// 以下字段由 engine 在执行前补充：
	ArtifactPath string // 已下载（或命中缓存）的产物路径
	SourceRoot   string // 解包后的安装源根目录（raw 适配器下即产物文件）
	Digest       string // 产物的 SHA256（用于从缓存复用时复核）
}

// DownloadResult 是下载结果。
type DownloadResult struct {
	Path   string
	Size   int64
	SHA256 string
}
