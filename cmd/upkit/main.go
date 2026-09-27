// Command upkit 是多软件更新维护工具（TUI 唯一前端）。
//
// 用法示例：
//
//	upkit.exe                     # 启动全屏界面（唯一的交互方式）
//	upkit.exe --print-paths       # 打印便携目录布局后退出（不需要终端）
//	upkit.exe --config D:\x\config\settings.yaml
//	upkit.exe --log-level debug --no-color --ascii
//	upkit.exe --no-mouse            # 关掉鼠标上报，保留终端原生拖选复制
//	upkit.exe --version           # 打印版本信息
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-isatty"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/control"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/tui"
	"github.com/dezhishen/upkit/internal/util"
	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"
)

// 由 -ldflags 注入的构建信息。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		configPath = flag.String("config", "", "设置文件路径（默认 "+defaultConfigPath()+"）")
		logLevel   = flag.String("log-level", "", "日志级别：debug|info|warn|error（覆盖设置文件）")
		logDir     = flag.String("log-dir", "", "日志目录（覆盖设置文件）")
		noColor    = flag.Bool("no-color", false, "禁用颜色输出")
		asciiUI    = flag.Bool("ascii", false, "仅使用 ASCII 字符绘制界面")
		noMouse    = flag.Bool("no-mouse", false, "关闭鼠标支持（保留终端原生的拖选复制与右键粘贴）")
		focusMode  = flag.Bool("focus", false, "以无标题栏的焦点模式重新启动（仅 Windows Terminal）")
		showVer    = flag.Bool("version", false, "打印版本并退出")
		showPaths  = flag.Bool("print-paths", false, "打印目录布局与配置文件位置后退出")
	)
	flag.Usage = printUsage
	flag.Parse()

	if *showVer {
		fmt.Println(versionString())
		return
	}
	if *showPaths {
		printLayout()
		return
	}
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "不支持的位置参数："+strings.Join(flag.Args(), " "))
		flag.Usage()
		os.Exit(2)
	}

	// 焦点模式要把控制权交给新进程，因此排在 --version/--print-paths 之后、
	// 终端检查之前：重新拉起不需要当前进程拥有终端。
	if *focusMode {
		relaunched, err := ensureFocusMode()
		if err != nil {
			fmt.Fprintln(os.Stderr, "upkit:", err)
			os.Exit(2)
		}
		if relaunched {
			return
		}
	}

	// TUI 需要真实终端；重定向或管道时立即报错，不挂起等待输入。
	if !isTerminal() {
		fmt.Fprintln(os.Stderr, "upkit 是全屏终端程序，请在交互式终端中运行。")
		os.Exit(2)
	}

	if err := run(optionSet{
		configPath: *configPath,
		logLevel:   *logLevel,
		logDir:     *logDir,
		noColor:    *noColor,
		ascii:      *asciiUI,
		noMouse:    *noMouse,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "upkit:", err)
		os.Exit(1)
	}
}

// optionSet 保存解析后的命令行参数。
type optionSet struct {
	configPath string
	logLevel   string
	logDir     string
	noColor    bool
	ascii      bool
	noMouse    bool
}

// run 装配设置、清单、日志、引擎并进入 TUI。
func run(opts optionSet) error {
	cfgPath := opts.configPath
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = defaultConfigPath()
	}
	set, err := settings.Load(cfgPath)
	if err != nil {
		return err
	}
	if opts.logLevel != "" {
		set.Logs.Level = opts.logLevel
	}
	if opts.logDir != "" {
		set.Logs.Dir = opts.logDir
	}
	set.Touch()
	if err := set.Normalize(); err != nil {
		return err
	}
	// 便携布局：config/ log/ plugin/ data/ cache/ backup/ temp/ 全部在 upkit 同级目录下
	if err := set.EnsureDirs(); err != nil {
		return err
	}

	// 软件清单
	afs, err := loadApps(set)
	if err != nil {
		return err
	}
	// 首次运行：把默认设置与空清单落到 config/ 下，方便直接编辑
	if err := set.Bootstrap(); err != nil {
		return err
	}
	if !util.FileExists(afs.Path) {
		if err := afs.Save(); err != nil {
			return err
		}
	}

	// 日志（TUI 模式下不再输出到终端，避免破坏界面）
	mgr, err := logging.New(logging.Options{
		Level:      set.Logs.Level,
		Dir:        set.Logs.Dir,
		Audit:      set.Logs.Audit,
		MaxSizeMB:  set.Logs.MaxSizeMB,
		MaxFiles:   set.Logs.MaxFiles,
		MaxAgeDays: set.Logs.MaxAgeDays,
		MaxTotalMB: set.Logs.MaxTotalMB,
		Compress:   set.Logs.Compress,
		Redact:     set.Logs.Redact,
		Console:    false,
	})
	if err != nil {
		return err
	}
	defer mgr.Close()
	mgr.Info("upkit 启动", "version", versionString(), "config", set.Path, "apps", afs.Path)

	// 插件来源：发现 → 信任校验 → 启动子进程。
	// 未信任的插件只记录状态、不启动：插件等于任意代码执行，必须显式信任。
	host, err := pluginhost.NewManager(pluginhost.Config{
		Dir:      set.Plugins.Dir,
		Entries:  afs.Sources,
		DataRoot: set.Storage.DataDir,
		LogRoot:  set.Logs.Dir,
		Log:      mgr,
		Stderr:   &pluginLogWriter{log: mgr},
	})
	if err != nil {
		return err
	}
	defer host.Close()
	host.Load(context.Background())
	logSourceStates(mgr, host)

	// 把插件来源里的软件并入清单；清单里的显式条目优先，可覆盖插件默认值。
	fillPluginApps(afs, host)

	// 订阅与授权记录：由界面维护，程序独占读写。
	// 手工编辑这个文件等价于跳过授权确认，所以界面上不提供“直接改文件”的路径。
	subStore, err := pluginfeed.LoadStore(filepath.Join(set.ConfigDir(), pluginfeed.FileName))
	if err != nil {
		return err
	}

	// 控制层：它持有全部领域服务与运行期状态，界面只跟它打交道。引擎也在这里面
	// 构造 —— 引擎要的事件接收器就是控制层的 Events()，两头各自接线容易接错。
	ctrl, err := control.New(control.Options{
		Settings: set,
		Apps:     afs,
		Logger:   mgr,
		Host:     host,
		Feed:     subStore,
	})
	if err != nil {
		return err
	}

	model := tui.New(tui.Options{
		Ctrl:       ctrl,
		Version:    version, // 短版本号：界面标题已含工具名
		NoColor:    opts.noColor,
		ASCII:      opts.ascii,
		NoMouse:    opts.noMouse,
		Borders:    set.UI.Borders,
		ConfigPath: set.Path,
	})
	// bubbletea v2 起，终端特性（备用屏幕、鼠标模式、窗口标题）改由 View 的字段声明，
	// 不再是 NewProgram 的选项。备用屏幕在 Model.View 内设置。
	p := tea.NewProgram(model)
	_, err = p.Run()
	return err
}

// loadApps 读取软件清单。
func loadApps(set *settings.Settings) (*apps.File, error) {
	return apps.Load(set.AppsPath())
}

// logSourceStates 把每个插件来源的结局写进日志（未信任的会把哈希打出来，方便启用）。
func logSourceStates(log core.Logger, host *pluginhost.Manager) {
	for _, st := range host.Sources() {
		if st.State == pluginhost.StateOK {
			log.Info("插件来源就绪", "source", st.ID, "version", st.Version, "apps", st.Apps, "mode", st.Mode, "exec", st.Exec)
			continue
		}
		log.Warn("插件来源未启用", "source", st.ID, "state", string(st.State), "detail", st.Detail)
	}
}

// fillPluginApps 用各插件来源提供的软件重建运行时列表。
//
// 软件只能来自订阅，因此这里每次都从插件全量重取，不再保留任何用户手写条目；
// 用户没有手写路径，也就没有“手写优先”可言。
// 失败只影响该来源，其它来源照常。
func fillPluginApps(afs *apps.File, host *pluginhost.Manager) {
	out := make([]apps.AppSpec, 0, len(afs.Apps))
	for _, id := range host.SourceIDs() {
		specs, err := host.AppSpecs(id)
		if err != nil {
			continue // 来源不可用的原因已在状态与日志里说明，不影响其它来源
		}
		for _, spec := range specs {
			// 尊重来源内的单软件开关（界面上的空格键写的就是它）。
			if !afs.SourceAppEnabled(id, spec.ID) {
				spec.Enabled = new(bool)
			}
			out = append(out, spec)
		}
	}
	afs.Apps = out
}

// pluginLogWriter 把插件进程的 stdout/stderr 逐行转发到宿主日志。
//
// 插件的 Logger 以 logfmt（level=... msg=...）写 stderr，这里还原级别后再落到
// upkit 的日志文件，从而让插件日志与宿主日志共用轮转、脱敏与 run 关联。
type pluginLogWriter struct {
	log core.Logger
	mu  sync.Mutex
	buf []byte
}

// logfmtSeparator 是插件日志的键值分隔符（与 pkg/plugin 的写入端一致）。
const logfmtSeparator = "="

func (w *pluginLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.emit(line)
	}
	return len(p), nil
}

func (w *pluginLogWriter) emit(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	level, msg := splitPluginLine(line)
	kv := []any{"line", msg}
	switch level {
	case upkitplugin.LogLevelDebug:
		w.log.Debug("插件日志", kv...)
	case upkitplugin.LogLevelWarn:
		w.log.Warn("插件日志", kv...)
	case upkitplugin.LogLevelError:
		w.log.Error("插件日志", kv...)
	default:
		w.log.Info("插件日志", kv...)
	}
}

// splitPluginLine 从插件日志行里取出级别与正文。
//
// 新插件用 zap 的 JSON encoder（值会被转义，杜绝日志注入）；
// 旧插件发的是手拼 logfmt，仍然要认，否则升级宿主后老插件的日志会整段变成正文。
func splitPluginLine(line string) (level, msg string) {
	if lv, m, ok := splitPluginJSON(line); ok {
		return lv, m
	}
	return splitPluginLogfmt(line)
}

// splitPluginJSON 解析 slog 输出的 JSON 行。
func splitPluginJSON(line string) (level, msg string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return "", "", false
	}
	var rec struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return "", "", false
	}
	if rec.Msg == "" && rec.Level == "" {
		return "", "", false
	}
	level = strings.ToLower(rec.Level)
	if level == "" {
		level = upkitplugin.LogLevelInfo
	}
	return level, rec.Msg, true
}

// splitPluginLogfmt 兼容旧插件的 "level=x msg=y" 行。
func splitPluginLogfmt(line string) (level, msg string) {
	level, rest := upkitplugin.LogLevelInfo, line
	if after, ok := strings.CutPrefix(rest, upkitplugin.LogKeyLevel+logfmtSeparator); ok {
		rest = after
		if i := strings.IndexByte(rest, ' '); i > 0 {
			level = strings.ToLower(rest[:i])
			rest = rest[i+1:]
		}
	}
	if after, ok := strings.CutPrefix(rest, upkitplugin.LogKeyMessage+logfmtSeparator); ok {
		rest = after
	}
	return level, strings.TrimSpace(rest)
}

// isTerminal 判断当前是否运行在真实终端里。
func isTerminal() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// defaultConfigPath 返回默认设置文件路径（不可用时退回当前目录）。
func defaultConfigPath() string {
	p, err := settings.DefaultPath()
	if err != nil {
		return settings.FileName
	}
	return p
}

// printLayout 打印便携布局的解析结果（排障用，不需要终端、不修改磁盘）。
func printLayout() {
	cfgPath, err := settings.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "upkit:", err)
		os.Exit(1)
	}
	set, err := settings.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upkit:", err)
		os.Exit(1)
	}
	if err := set.Normalize(); err != nil {
		fmt.Fprintln(os.Stderr, "upkit:", err)
		os.Exit(1)
	}
	rows := [][2]string{
		{"根目录", set.RootDir()},
		{"设置文件", set.Path},
		{"软件清单", set.AppsPath()},
		{"清单导出", set.ManifestPath()},
		{"日志目录", set.Logs.Dir},
		{"插件目录", set.Plugins.Dir},
		{"数据目录", set.Storage.DataDir},
		{"缓存目录", set.Storage.CacheDir},
		{"备份目录", set.Storage.BackupDir},
		{"临时目录", set.Storage.TempDir},
	}
	for _, r := range rows {
		fmt.Printf("%-10s %s\n", r[0], r[1])
	}
	fmt.Printf("%-10s %s\n", "切换布局", "用 --config 指定设置文件，或用 "+settings.EnvHome+" 环境变量整体迁移根目录")
}

// printUsage 输出帮助。
func printUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `upkit %s —— 多软件更新维护工具（TUI）

用法：
  upkit [选项]

选项：
`, version)
	flag.PrintDefaults()
	fmt.Fprint(out, `
界面快捷键（按 ? 查看全部）：
  1..6/Tab  切换面板        c / C  检查选中 / 检查全部
  u / U     更新选中 / 全部  p      生成执行计划
  x         卸载            r      回滚到最近备份
  E / I     导出 / 导入清单  q      退出

目录布局（默认便携模式，全部在 upkit 同级目录下）：
  config/   settings.yaml、apps.yaml、`+settings.ManifestFileName+`
  log/      运行日志与审计日志
  plugin/   插件（子系统尚未实现，目录预留）
  data/ cache/ backup/ temp/

根目录优先级：`+settings.EnvHome+` 环境变量 > upkit 所在目录（可写时）> 用户配置目录。
当前设置文件：`+defaultConfigPath()+`

退出码：
  0  正常退出
  1  启动失败（配置、日志或界面初始化错误）
  2  命令行参数错误，或未在交互式终端中运行
`)
}

// versionString 返回带构建信息的版本描述。
func versionString() string {
	return fmt.Sprintf("upkit %s (commit %s, built %s, %s %s/%s)",
		version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
