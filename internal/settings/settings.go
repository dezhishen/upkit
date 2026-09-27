// Package settings 管理 upkit 自身的行为设置（<根目录>/config/settings.yaml）。
//
// 目录布局（默认便携模式，全部在 upkit 同级目录下）：
//
//	<根目录>/config/settings.yaml   设置
//	<根目录>/config/apps.yaml       软件清单
//	<根目录>/config/upkit-manifest.json 清单导出
//	<根目录>/log/                   日志与审计
//	<根目录>/plugin/                插件（子系统尚未实现，目录预留）
//	<根目录>/data/ cache/ backup/ temp/
//
// 根目录取值顺序：环境变量 UPKIT_HOME > upkit 可执行文件所在目录（可写时）>
// <用户配置目录>/upkit（例如 %APPDATA%\upkit、~/.config/upkit）。
// 用 --config 指向别处时，以该文件所在目录为根（若目录名为 config，则取其上一级）。
//
// 设计：单文件、单用户、无系统级配置、无强制策略、无后台服务。修改即时生效，
// 写盘采用「临时文件 + 原子替换」，写失败不会破坏已有配置。
package settings

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/util"
)

// FileName 是设置文件名。
const FileName = "settings.yaml"

// AppsFileName 是软件清单文件名。
const AppsFileName = "apps.yaml"

// ManifestFileName 是清单导出文件名。
const ManifestFileName = "upkit-manifest.json"

// EnvHome 可覆盖便携布局的根目录（便于多套配置共存与自动化测试）。
const EnvHome = "UPKIT_HOME"

// 根目录下的固定子目录名。
const (
	DirConfig = "config"
	DirLog    = "log"
	DirPlugin = "plugin"
	DirData   = "data"
	DirCache  = "cache"
	DirBackup = "backup"
	DirTemp   = "temp"
	// DirApps 是软件默认安装目录名（<根目录>/apps）。
	DirApps = "apps"
)

// Settings 是全部可配置项。
type Settings struct {
	Network  Network  `yaml:"network"`
	Storage  Storage  `yaml:"storage"`
	Plugins  Plugins  `yaml:"plugins"`
	Engine   Engine   `yaml:"engine"`
	Behavior Behavior `yaml:"behavior"`
	Logs     Logs     `yaml:"logs"`
	UI       UI       `yaml:"ui"`

	// Path 记录实际加载的文件路径（不在文件里出现）。
	Path string `yaml:"-"`
}

// Network 是网络相关设置。
type Network struct {
	Proxy          string `yaml:"proxy"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Retries        int    `yaml:"retries"`
	RateLimitKBps  int    `yaml:"rate_limit_kbps"`
	GitHubToken    string `yaml:"github_token"`
}

// Storage 是目录与容量相关设置。
type Storage struct {
	// InstallRoot 是「软件装到哪」的根目录：插件声明的 ${ROOT} 指向它，没写明安装
	// 路径的软件也装到它下面（<安装根目录>/<软件名>）。
	//
	// 留空表示跟随根目录（<根目录>/apps）。与其它目录项一样：改它不会搬动已经装好
	// 的软件 —— 那些软件的落点在清单里，检测也按那份清单。
	InstallRoot string `yaml:"install_root"`
	// ForceInstallRoot 为真时，所有由 upkit 自己落地的软件（portable-inplace）
	// 一律装到 <安装根目录>/<软件名>，**忽略插件声明的目录**。
	//
	// 存在的理由：插件可能写死 ${LOCALAPPDATA}/… 之类的路径，用户想「全部装到
	// 一个盘下的一个目录」就无从下手。默认关 —— 打开后已装在别处的软件会被看作
	// 「未安装」，需要在安装根目录下重新落地一次。
	//
	// 由安装器/插件自己决定落点的方法（exe-installer / msiexec / plugin）
	// 不受影响：那些路径只是「去哪找它」，强行改会連探测都找不到。
	ForceInstallRoot bool `yaml:"force_install_root"`

	DataDir        string `yaml:"data_dir"`
	CacheDir       string `yaml:"cache_dir"`
	TempDir        string `yaml:"temp_dir"`
	BackupDir      string `yaml:"backup_dir"`
	BackupKeep     int    `yaml:"backup_keep"`
	CacheKeep      int    `yaml:"cache_keep"`
	MinFreeSpaceMB int    `yaml:"min_free_space_mb"`
	BudgetMB       int    `yaml:"budget_mb"`
}

// Plugins 是插件相关设置。
type Plugins struct {
	Dir string `yaml:"dir"`
	// AutoLoadTrusted 为真时，订阅安装成功的插件直接记入信任并加载 —— 安装已经过用户
	// 三级授权（功能开关 → 订阅域名 → 跨域下载域名），包摘要也在下载后强制校验过。
	// 为假时插件保持「未信任」，需要用户在来源面板按 t 逐个确认。
	//
	// 手工放进 plugin/ 的插件不受这项影响：它没经过任何授权，一律需要显式信任。
	AutoLoadTrusted bool `yaml:"auto_load_trusted"`
	// RequireSignature 要求插件带签名（尚未实现，仅保留配置）。
	RequireSignature bool     `yaml:"require_signature"`
	Allowlist        []string `yaml:"allowlist"`
}

// Engine 是并发相关设置。
type Engine struct {
	DownloadConcurrency int `yaml:"download_concurrency"`
	ApplyConcurrency    int `yaml:"apply_concurrency"`
}

// Behavior 是交互行为设置。
type Behavior struct {
	ConfirmBeforeApply bool   `yaml:"confirm_before_apply"`
	WaitForClose       bool   `yaml:"wait_for_close"`
	StopStrategy       string `yaml:"stop_strategy"`
	LaunchAfterUpdate  bool   `yaml:"launch_after_update"`
}

// Logs 是日志设置。
type Logs struct {
	Level      string `yaml:"level"`
	Dir        string `yaml:"dir"`
	Audit      bool   `yaml:"audit"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxFiles   int    `yaml:"max_files"`
	MaxAgeDays int    `yaml:"max_age_days"`
	Compress   bool   `yaml:"compress"`
	MaxTotalMB int    `yaml:"max_total_mb"`
	Redact     bool   `yaml:"redact"`
}

// UI 是界面设置。
type UI struct {
	Theme     string `yaml:"theme"`
	Borders   string `yaml:"borders"`
	RefreshMS int    `yaml:"refresh_ms"`
}

// Default 返回内置默认值。
func Default() *Settings {
	return &Settings{
		Network: Network{TimeoutSeconds: 60, Retries: 3},
		Storage: Storage{BackupKeep: 3, CacheKeep: 2, MinFreeSpaceMB: 2048, BudgetMB: 20480},
		Plugins: Plugins{AutoLoadTrusted: true},
		Engine:  Engine{DownloadConcurrency: 2, ApplyConcurrency: 4},
		Behavior: Behavior{
			ConfirmBeforeApply: true,
			WaitForClose:       true,
			StopStrategy:       "graceful",
		},
		Logs: Logs{
			Level: "info", Audit: true, MaxSizeMB: 16, MaxFiles: 20,
			MaxAgeDays: 30, Compress: true, MaxTotalMB: 256, Redact: true,
		},
		UI: UI{Theme: "auto", Borders: "unicode", RefreshMS: 250},
	}
}

// BaseDir 返回便携布局的根目录：
//
//  1. 环境变量 UPKIT_HOME
//  2. upkit 可执行文件所在目录
//  3. 兜底：当前工作目录
func BaseDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		p := util.ExpandPath(v)
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// RootDir 返回实际使用的根目录：可执行文件同级目录可写时用便携布局，
// 否则回退到用户配置目录（例如安装在 Program Files 下的场景）。
func RootDir() (string, error) {
	base := BaseDir()
	if dirWritable(base) {
		return base, nil
	}
	user, err := userRoot()
	if err != nil {
		return "", err
	}
	if err := util.EnsureDir(user); err != nil {
		return "", err
	}
	return user, nil
}

// ConfigDir 返回设置目录（<根目录>/config，不存在时创建）。
func ConfigDir() (string, error) {
	root, err := RootDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, DirConfig)
	if err := util.EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// DefaultPath 返回默认的 settings.yaml 路径（<根目录>/config/settings.yaml）。
func DefaultPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// userRoot 返回用户配置目录下的 upkit 目录（回退布局）。
func userRoot() (string, error) {
	return configRoot("upkit")
}

// configRoot 返回 <用户配置目录>/<name>。
//
// 这是便携目录不可写时的回退布局。不需要按平台分支：Windows 上
// os.UserConfigDir() 就是 %APPDATA%，而 upkit 也只发行 Windows 版本。
func configRoot(name string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	return filepath.Join(base, name), nil
}

// rootFor 根据设置文件路径推算布局根目录：
//
//	/app/upkit/config/settings.yaml -> /app/upkit
//	/tmp/x/settings.yaml           -> /tmp/x
func rootFor(configPath string) string {
	dir := filepath.Dir(configPath)
	if strings.EqualFold(filepath.Base(dir), DirConfig) {
		return filepath.Dir(dir)
	}
	return dir
}

// dirWritable 报告目录是否可写（通过创建临时探针文件判断）。
func dirWritable(dir string) bool {
	if err := util.EnsureDir(dir); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".upkit-writable-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// Load 读取设置；文件不存在时返回默认值并把 Path 指向将要创建的位置。
func Load(path string) (*Settings, error) {
	s := Default()
	if strings.TrimSpace(path) == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	path = util.ExpandPath(path)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.Path = path
			// 和读到文件时一样归一化一次：目录项的默认值由根目录推导，漏掉这一步的话
			// 「首次运行」拿到的是一份目录全空的设置，每个调用方都得自己再补一次。
			if err := s.Normalize(); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, fmt.Errorf("读取设置 %s: %w", path, err)
	}

	// 直接解码到默认值上：文件里没出现的字段保持默认，出现的字段（包括显式的
	// false）按文件覆盖。这是解码器的天然语义。
	//
	// 先前的做法是「解码到空结构体 + merge 默认值」，而 merge 只能靠零值判断
	// 用户是否写过该字段 —— 对默认 true 的布尔项来说，false 既是用户意图又是
	// 零值，于是被默认值覆盖回去：执行前确认、日志脱敏这些开关永远关不掉。
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(s); err != nil && err != io.EOF {
		return nil, fmt.Errorf("解析设置 %s: %w", path, err)
	}
	s.Path = path
	if err := s.Normalize(); err != nil {
		return nil, err
	}
	return s, nil
}

// Normalize 补全派生路径并做基础校验。
func (s *Settings) Normalize() error {
	if s.Path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		s.Path = p
	}
	// 目录：留空表示跟随根目录。
	s.Storage.InstallRoot = s.ExpandDir(s.Storage.InstallRoot, DirApps)
	s.Storage.DataDir = s.ExpandDir(s.Storage.DataDir, DirData)
	s.Storage.CacheDir = s.ExpandDir(s.Storage.CacheDir, DirCache)
	s.Storage.TempDir = s.ExpandDir(s.Storage.TempDir, DirTemp)
	s.Storage.BackupDir = s.ExpandDir(s.Storage.BackupDir, DirBackup)
	s.Logs.Dir = s.ExpandDir(s.Logs.Dir, DirLog)
	s.Plugins.Dir = s.ExpandDir(s.Plugins.Dir, DirPlugin)

	if s.Network.TimeoutSeconds < 0 {
		s.Network.TimeoutSeconds = 0
	}
	if s.Network.Retries < 0 {
		s.Network.Retries = 0
	}
	if s.Storage.BackupKeep < 0 {
		s.Storage.BackupKeep = 0
	}
	if s.Storage.CacheKeep < 0 {
		s.Storage.CacheKeep = 0
	}
	if s.Engine.DownloadConcurrency <= 0 {
		s.Engine.DownloadConcurrency = 2
	}
	if s.Engine.ApplyConcurrency <= 0 {
		s.Engine.ApplyConcurrency = 4
	}
	if s.UI.RefreshMS <= 0 {
		s.UI.RefreshMS = 250
	}
	s.Logs.Level = strings.ToLower(strings.TrimSpace(s.Logs.Level))
	switch s.Logs.Level {
	case "", "info":
		s.Logs.Level = "info"
	case "error", "warn", "debug", "trace":
	default:
		return fmt.Errorf("logs.level 非法: %q（可选 error/warn/info/debug/trace）", s.Logs.Level)
	}
	switch strings.ToLower(s.Behavior.StopStrategy) {
	case "", "graceful":
		s.Behavior.StopStrategy = "graceful"
	case "force":
		s.Behavior.StopStrategy = "force"
	default:
		return fmt.Errorf("behavior.stop_strategy 非法: %q（可选 graceful/force）", s.Behavior.StopStrategy)
	}
	s.UI.Borders = strings.ToLower(strings.TrimSpace(s.UI.Borders))
	switch s.UI.Borders {
	case "", "unicode":
		s.UI.Borders = "unicode"
	case "square", "ascii":
	default:
		return fmt.Errorf("ui.borders 非法: %q（可选 unicode/square/ascii）", s.UI.Borders)
	}
	s.UI.Theme = strings.ToLower(strings.TrimSpace(s.UI.Theme))
	switch s.UI.Theme {
	case "", "auto":
		s.UI.Theme = "auto"
	case "dark", "light":
	default:
		return fmt.Errorf("ui.theme 非法: %q（可选 auto/dark/light）", s.UI.Theme)
	}
	// 代理需要协议前缀
	if p := strings.TrimSpace(s.Network.Proxy); p != "" && !strings.Contains(p, "://") {
		return fmt.Errorf("network.proxy 需要协议前缀，例如 http://127.0.0.1:7890，当前为 %q", p)
	}
	// Token 支持 env: / cmd: 引用
	if strings.TrimSpace(s.Network.GitHubToken) == "" {
		s.Network.GitHubToken = firstEnv("GITHUB_TOKEN", "GH_TOKEN")
	}
	return nil
}

// Save 原子写回设置文件。
func (s *Settings) Save() error {
	if s.Path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		s.Path = p
	}
	data, err := yaml.Marshal(s.snapshot())
	if err != nil {
		return fmt.Errorf("序列化设置: %w", err)
	}
	if err := fsutil.WriteFileAtomic(s.Path, data, 0o644); err != nil {
		return err
	}
	return nil
}

// snapshot 返回用于落盘的副本。
//
// 与当前布局推导结果相同的目录字段会被清空：这样把整个 upkit 目录搬到别处后，
// config/log/plugin/data/cache/backup/temp 会自动跟随新位置，而不是继续指向旧路径；
// 用户手工填写的自定义目录会被保留。
func (s *Settings) snapshot() *Settings {
	out := *s
	root := rootFor(s.Path)
	clearIfSame := func(v *string, def string) {
		if *v == def {
			*v = ""
		}
	}
	clearIfSame(&out.Storage.InstallRoot, filepath.Join(root, DirApps))
	clearIfSame(&out.Storage.DataDir, filepath.Join(root, DirData))
	clearIfSame(&out.Storage.CacheDir, filepath.Join(root, DirCache))
	clearIfSame(&out.Storage.TempDir, filepath.Join(root, DirTemp))
	clearIfSame(&out.Storage.BackupDir, filepath.Join(root, DirBackup))
	clearIfSame(&out.Logs.Dir, filepath.Join(root, DirLog))
	clearIfSame(&out.Plugins.Dir, filepath.Join(root, DirPlugin))
	return &out
}

// Bootstrap 首次运行时的初始化：设置文件不存在则写入一份默认配置。
func (s *Settings) Bootstrap() error {
	if util.FileExists(s.Path) {
		return nil
	}
	return s.Save()
}

// ConfigDir 返回设置/清单所在目录（<根目录>/config）。
func (s *Settings) ConfigDir() string {
	return filepath.Dir(s.Path)
}

// RootDir 返回便携布局的根目录（config 的上一级）。
func (s *Settings) RootDir() string {
	return rootFor(s.Path)
}

// ExpandDir 把目录设置解析成实际路径：留空表示跟随根目录（<根目录>/<name>）。
//
// 设置面板里「留空 = 跟随根目录」显示的就是这个解析结果，落盘时 snapshot 又会把
// 等于推导默认值的字段清空 —— 两处必须用同一个函数，否则界面显示的位置与实际写进
// 文件的内容会对不上：界面显示默认路径、文件里却存着死路径，整目录搬走后就指向旧位置。
func (s *Settings) ExpandDir(v, name string) string {
	return pickDir(v, filepath.Join(s.RootDir(), name))
}

// AppsPath 返回 apps.yaml 的路径。
func (s *Settings) AppsPath() string {
	return filepath.Join(s.ConfigDir(), AppsFileName)
}

// ManifestPath 返回清单导出/导入的默认路径。
func (s *Settings) ManifestPath() string {
	return filepath.Join(s.ConfigDir(), ManifestFileName)
}

// BackupRoot 返回备份根目录。
func (s *Settings) BackupRoot() string {
	return s.Storage.BackupDir
}

// InstallRootDir 返回软件的安装根目录（已展开，绝不返回空串）。
func (s *Settings) InstallRootDir() string {
	if v := strings.TrimSpace(s.Storage.InstallRoot); v != "" {
		return util.ExpandPath(v)
	}
	return filepath.Join(s.RootDir(), DirApps)
}

// BackupDir 返回某个软件的备份目录。
func (s *Settings) BackupDir(appID string) string {
	return filepath.Join(s.Storage.BackupDir, util.SanitizeFileName(appID))
}

// EnsureDirs 创建布局中的全部目录（启动时调用一次）。
func (s *Settings) EnsureDirs() error {
	dirs := []string{
		s.ConfigDir(), s.Storage.DataDir, s.Storage.CacheDir,
		s.Storage.TempDir, s.Storage.BackupDir, s.Logs.Dir, s.Plugins.Dir,
	}
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		if err := util.EnsureDir(d); err != nil {
			return err
		}
	}
	return nil
}

// Level 返回日志级别的数值序。
func (s *Settings) Level() int {
	switch s.Logs.Level {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn":
		return 3
	case "error":
		return 4
	default:
		return 2
	}
}

// ResolveToken 解析 env: / cmd: 形式的密钥引用。
func ResolveToken(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(ref, "env:"):
		return strings.TrimSpace(os.Getenv(strings.TrimPrefix(ref, "env:")))
	case strings.HasPrefix(ref, "cmd:"):
		return runSecretCmd(strings.TrimPrefix(ref, "cmd:"))
	default:
		return ref
	}
}

// IsSecretRef 报告该值是否为引用形式（引用不落盘、日志里不回显明文）。
func IsSecretRef(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "env:") || strings.HasPrefix(v, "cmd:")
}

// MaskSecret 生成用于展示的脱敏文本。
func MaskSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	if IsSecretRef(v) {
		return v
	}
	return "***"
}

func pickDir(v, def string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return util.ExpandPath(def)
	}
	return util.ExpandPath(v)
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// runSecretCmd 执行外部命令取得密钥。
//
// upkit 只发行 Windows 版本，所以固定用 cmd /c，不为其它 shell 留分支 ——
// 省下的那条分支不会被任何人用到，却会让人以为这程序还支持别的系统。
func runSecretCmd(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	out, err := exec.Command("cmd", "/c", command).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Touch 返回 mtime；用于界面显示配置最后修改时间。
func (s *Settings) Touch() time.Time {
	if info, err := os.Stat(s.Path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}
