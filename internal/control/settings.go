package control

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dezhishen/upkit/internal/settings"
)

// SettingKind 是设置项的编辑方式：界面按它决定怎么渲染、按哪个键响应。
type SettingKind string

// 设置项的编辑方式。
const (
	SettingBool SettingKind = "bool" // 开/关
	SettingInt  SettingKind = "int"  // 数字，按步长增减
	SettingEnum SettingKind = "enum" // 枚举，左右循环
	SettingText SettingKind = "text" // 文本，需要弹窗整段输入
)

// SettingItem 是一项设置的当前状态。
//
// 值、范围、步长、选项都由本层给出，界面只负责画出来再按 key 提交意图 —— 跟前后端
// 分家的做法一样：前端不猜字段，也不持有可写的状态。
type SettingItem struct {
	Key   string
	Label string
	Kind  SettingKind
	// Text 是当前值的展示形式：布尔为「开/关」、枚举为当前项、数字为十进制串，
	// 文本项按需遮蔽（secret 时只表明是否已设置）。
	Text string
	// Step 是数字项每次调整的步长。
	Step int
	// Hint 是文本项编辑弹窗的占位说明。
	Hint string
	// Secret 为真表示这是凭据类文本项，输入框按密码模式回显，不预填原值。
	Secret bool
	// Group 是分组标题：界面按它把表单分段显示（同一组的项一定连续）。
	Group string
}

// settingGroups 是设置项的分组：key 前缀 → 段标题，顺序即界面上的顺序。
//
// 分组由 key 的前缀推出来（network. / storage. / logs. …），而不是在每一项上手写一遍：
// 手写的话，新增一项忘写分组、或把 storage 的项塞进 logs 段里，都没人会立刻发现。
var settingGroups = []struct {
	prefix string
	label  string
}{
	{"network.", "网络"},
	{"engine.", "下载与安装"},
	{"storage.", "存储与位置"},
	{"behavior.", "行为"},
	{"logs.", "日志"},
	{"plugins.", "插件"},
	{"ui.", "界面"},
}

// groupOf 返回某项所属的分组标题（认不出的前缀归到空串，界面会单独兜底）。
func groupOf(key string) string {
	for _, g := range settingGroups {
		if strings.HasPrefix(key, g.prefix) {
			return g.label
		}
	}
	return ""
}

// groupOrder 返回分组在界面上的次序（认不出的排最后）。
func groupOrder(label string) int {
	for i, g := range settingGroups {
		if g.label == label {
			return i
		}
	}
	return len(settingGroups)
}

// settingDef 是一项设置的读写定义（取值、范围、解析都在这张表里）。
type settingDef struct {
	key    string
	label  string
	kind   SettingKind
	hint   string
	secret bool

	// dirName 非空表示这是一项目录设置：值的解析与说明都由子目录名推导（留空 =
	// 跟随根目录）。写在一个字段里而不是每项各写一遍，是为了字面量与提示不会对不上。
	dirName string

	// 数字项。
	step, min, max int

	// 枚举项。
	opts []string

	getInt  func(*settings.Settings) int
	setInt  func(*settings.Settings, int)
	getBool func(*settings.Settings) bool
	setBool func(*settings.Settings, bool)
	getStr  func(*settings.Settings) string
	setStr  func(*settings.Settings, string)
}

// settingCatalog 是全部可编辑项，顺序即界面上的顺序。
//
// 放在这里而不是界面里：取值范围与解析规则是行为，不是显示；放一起的话界面上任何
// 一次手改都可能绕开它们。
var settingCatalog = []settingDef{
	{key: "network.proxy", label: "网络代理", kind: SettingText,
		hint:   "http:// 或 socks5://（留空表示不使用）",
		getStr: func(s *settings.Settings) string { return s.Network.Proxy },
		setStr: func(s *settings.Settings, v string) { s.Network.Proxy = strings.TrimSpace(v) }},

	{key: "network.timeout_seconds", label: "请求超时（秒）", kind: SettingInt, step: 10, min: 10, max: 600,
		getInt: func(s *settings.Settings) int { return s.Network.TimeoutSeconds },
		setInt: func(s *settings.Settings, v int) { s.Network.TimeoutSeconds = v }},

	{key: "network.retries", label: "失败重试次数", kind: SettingInt, step: 1, min: 0, max: 10,
		getInt: func(s *settings.Settings) int { return s.Network.Retries },
		setInt: func(s *settings.Settings, v int) { s.Network.Retries = v }},

	{key: "network.rate_limit_kbps", label: "下载限速（KB/s，0=不限）", kind: SettingInt, step: 256, min: 0, max: 256 * 1024,
		getInt: func(s *settings.Settings) int { return s.Network.RateLimitKBps },
		setInt: func(s *settings.Settings, v int) { s.Network.RateLimitKBps = v }},

	{key: "network.github_token", label: "GitHub 令牌", kind: SettingText, secret: true,
		hint:   "留空表示不使用；也可写 env:VAR 引用环境变量",
		getStr: func(s *settings.Settings) string { return s.Network.GitHubToken },
		setStr: func(s *settings.Settings, v string) { s.Network.GitHubToken = strings.TrimSpace(v) }},

	{key: "engine.download_concurrency", label: "下载并发", kind: SettingInt, step: 1, min: 1, max: 8,
		getInt: func(s *settings.Settings) int { return s.Engine.DownloadConcurrency },
		setInt: func(s *settings.Settings, v int) { s.Engine.DownloadConcurrency = v }},

	{key: "engine.apply_concurrency", label: "安装并发", kind: SettingInt, step: 1, min: 1, max: 16,
		getInt: func(s *settings.Settings) int { return s.Engine.ApplyConcurrency },
		setInt: func(s *settings.Settings, v int) { s.Engine.ApplyConcurrency = v }},

	{key: "storage.install_root", label: "软件安装根目录", dirName: settings.DirApps,
		hint:   "绝对路径；留空 = 跟随根目录（<根目录>/apps）。插件里写的 ${ROOT} 指这里，没写明安装路径的软件也装到这里",
		getStr: func(s *settings.Settings) string { return s.Storage.InstallRoot },
		setStr: func(s *settings.Settings, v string) { s.Storage.InstallRoot = v }},

	{key: "storage.data_dir", label: "数据目录", dirName: settings.DirData,
		getStr: func(s *settings.Settings) string { return s.Storage.DataDir },
		setStr: func(s *settings.Settings, v string) { s.Storage.DataDir = v }},

	{key: "storage.cache_dir", label: "缓存目录", dirName: settings.DirCache,
		getStr: func(s *settings.Settings) string { return s.Storage.CacheDir },
		setStr: func(s *settings.Settings, v string) { s.Storage.CacheDir = v }},

	{key: "storage.backup_dir", label: "备份目录", dirName: settings.DirBackup,
		getStr: func(s *settings.Settings) string { return s.Storage.BackupDir },
		setStr: func(s *settings.Settings, v string) { s.Storage.BackupDir = v }},

	{key: "storage.temp_dir", label: "临时目录", dirName: settings.DirTemp,
		getStr: func(s *settings.Settings) string { return s.Storage.TempDir },
		setStr: func(s *settings.Settings, v string) { s.Storage.TempDir = v }},

	{key: "storage.cache_keep", label: "缓存保留份数", kind: SettingInt, step: 1, min: 0, max: 50,
		getInt: func(s *settings.Settings) int { return s.Storage.CacheKeep },
		setInt: func(s *settings.Settings, v int) { s.Storage.CacheKeep = v }},

	{key: "storage.backup_keep", label: "备份保留份数", kind: SettingInt, step: 1, min: 0, max: 20,
		getInt: func(s *settings.Settings) int { return s.Storage.BackupKeep },
		setInt: func(s *settings.Settings, v int) { s.Storage.BackupKeep = v }},

	{key: "storage.budget_mb", label: "存储总配额（MB，0=不限）", kind: SettingInt, step: 1024, min: 0, max: 1024 * 1024,
		getInt: func(s *settings.Settings) int { return s.Storage.BudgetMB },
		setInt: func(s *settings.Settings, v int) { s.Storage.BudgetMB = v }},

	{key: "storage.min_free_space_mb", label: "最低磁盘余量（MB）", kind: SettingInt, step: 512, min: 0, max: 512 * 1024,
		getInt: func(s *settings.Settings) int { return s.Storage.MinFreeSpaceMB },
		setInt: func(s *settings.Settings, v int) { s.Storage.MinFreeSpaceMB = v }},

	{key: "behavior.confirm_before_apply", label: "执行前确认", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Behavior.ConfirmBeforeApply },
		setBool: func(s *settings.Settings, v bool) { s.Behavior.ConfirmBeforeApply = v }},

	{key: "behavior.wait_for_close", label: "等待关闭占用进程", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Behavior.WaitForClose },
		setBool: func(s *settings.Settings, v bool) { s.Behavior.WaitForClose = v }},

	{key: "behavior.launch_after_update", label: "更新后自动启动", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Behavior.LaunchAfterUpdate },
		setBool: func(s *settings.Settings, v bool) { s.Behavior.LaunchAfterUpdate = v }},

	{key: "behavior.stop_strategy", label: "结束占用进程方式", kind: SettingEnum, opts: []string{"graceful", "force"},
		getStr: func(s *settings.Settings) string { return s.Behavior.StopStrategy },
		setStr: func(s *settings.Settings, v string) { s.Behavior.StopStrategy = v }},

	{key: "logs.dir", label: "日志目录", dirName: settings.DirLog,
		getStr: func(s *settings.Settings) string { return s.Logs.Dir },
		setStr: func(s *settings.Settings, v string) { s.Logs.Dir = v }},

	{key: "logs.level", label: "日志级别", kind: SettingEnum, opts: []string{"debug", "info", "warn", "error"},
		getStr: func(s *settings.Settings) string { return s.Logs.Level },
		setStr: func(s *settings.Settings, v string) { s.Logs.Level = v }},

	{key: "logs.audit", label: "审计日志", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Logs.Audit },
		setBool: func(s *settings.Settings, v bool) { s.Logs.Audit = v }},

	{key: "logs.max_files", label: "日志保留份数", kind: SettingInt, step: 1, min: 1, max: 200,
		getInt: func(s *settings.Settings) int { return s.Logs.MaxFiles },
		setInt: func(s *settings.Settings, v int) { s.Logs.MaxFiles = v }},

	{key: "logs.max_age_days", label: "日志保留天数", kind: SettingInt, step: 1, min: 1, max: 3650,
		getInt: func(s *settings.Settings) int { return s.Logs.MaxAgeDays },
		setInt: func(s *settings.Settings, v int) { s.Logs.MaxAgeDays = v }},

	{key: "logs.compress", label: "压缩旧日志", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Logs.Compress },
		setBool: func(s *settings.Settings, v bool) { s.Logs.Compress = v }},

	{key: "logs.max_size_mb", label: "单个日志上限（MB）", kind: SettingInt, step: 4, min: 1, max: 512,
		getInt: func(s *settings.Settings) int { return s.Logs.MaxSizeMB },
		setInt: func(s *settings.Settings, v int) { s.Logs.MaxSizeMB = v }},

	{key: "logs.max_total_mb", label: "日志总配额（MB，0=不限）", kind: SettingInt, step: 64, min: 0, max: 64 * 1024,
		getInt: func(s *settings.Settings) int { return s.Logs.MaxTotalMB },
		setInt: func(s *settings.Settings, v int) { s.Logs.MaxTotalMB = v }},

	{key: "logs.redact", label: "日志脱敏", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Logs.Redact },
		setBool: func(s *settings.Settings, v bool) { s.Logs.Redact = v }},

	{key: "plugins.dir", label: "插件目录", dirName: settings.DirPlugin,
		getStr: func(s *settings.Settings) string { return s.Plugins.Dir },
		setStr: func(s *settings.Settings, v string) { s.Plugins.Dir = v }},

	{key: "plugins.auto_load_trusted", label: "自动加载已授权插件", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Plugins.AutoLoadTrusted },
		setBool: func(s *settings.Settings, v bool) { s.Plugins.AutoLoadTrusted = v }},

	{key: "plugins.require_signature", label: "要求插件签名", kind: SettingBool,
		getBool: func(s *settings.Settings) bool { return s.Plugins.RequireSignature },
		setBool: func(s *settings.Settings, v bool) { s.Plugins.RequireSignature = v }},

	{key: "plugins.allowlist", label: "插件域名白名单", kind: SettingText,
		hint:   "逗号分隔；留空表示不限制（跨域下载仍需逐次授权）",
		getStr: func(s *settings.Settings) string { return strings.Join(s.Plugins.Allowlist, ", ") },
		setStr: func(s *settings.Settings, v string) { s.Plugins.Allowlist = splitSettingList(v) }},

	{key: "ui.theme", label: "界面主题", kind: SettingEnum, opts: []string{"auto", "dark", "light"},
		getStr: func(s *settings.Settings) string { return s.UI.Theme },
		setStr: func(s *settings.Settings, v string) { s.UI.Theme = v }},

	{key: "ui.borders", label: "边框样式", kind: SettingEnum, opts: []string{"unicode", "square", "ascii"},
		getStr: func(s *settings.Settings) string { return s.UI.Borders },
		setStr: func(s *settings.Settings, v string) { s.UI.Borders = v }},

	{key: "ui.refresh_ms", label: "刷新间隔（毫秒）", kind: SettingInt, step: 100, min: 100, max: 5000,
		getInt: func(s *settings.Settings) int { return s.UI.RefreshMS },
		setInt: func(s *settings.Settings, v int) { s.UI.RefreshMS = v }},
}

// init 补全目录项：编辑方式与提示都由 dirName 推导。
//
// 目录项都是文本项，弹窗里的说明要写出「留空 = 跟随根目录（<根目录>/data）」——
// 六个地方各写一遍，早晚会有一处与实际解析用的子目录名不一致。
func init() {
	for i := range settingCatalog {
		def := &settingCatalog[i]
		if def.dirName == "" {
			continue
		}
		def.kind = SettingText
		if def.hint == "" {
			def.hint = "绝对路径；留空 = 跟随根目录（<根目录>/" + def.dirName + "）"
		}
	}
}

// SettingsForm 返回设置表单的当前状态。
func (c *Controller) SettingsForm() []SettingItem {
	if c.set == nil {
		return nil
	}
	out := make([]SettingItem, 0, len(settingCatalog))
	for _, def := range settingCatalog {
		hint := def.hint
		if hint == "" && def.kind == SettingInt {
			// 数字项整段编辑时要有说明：输入框里不写清范围，用户就得靠试。
			hint = fmt.Sprintf("范围 %d–%d，步长 %d", def.min, def.max, def.step)
		}
		out = append(out, SettingItem{
			Key:    def.key,
			Label:  def.label,
			Kind:   def.kind,
			Text:   settingText(c.set, def),
			Step:   def.step,
			Hint:   hint,
			Secret: def.secret,
			Group:  groupOf(def.key),
		})
	}
	// 按分组表排一遍（组内保持清单里的相对顺序）：界面只要按「Group 变了」插一个
	// 标题就行，不必自己维护分组顺序，清单顺序写乱了也不会散开。
	sort.SliceStable(out, func(i, j int) bool {
		return groupOrder(out[i].Group) < groupOrder(out[j].Group)
	})
	return out
}

// AdjustSetting 按 key 调整一项：数字往 delta 方向走一步，布尔取反，枚举循环。
//
// 范围与选项都在这里判，界面只管按键 —— 这样「哪些值合法」只有一处定义。
func (c *Controller) AdjustSetting(key string, delta int) error {
	def, err := c.settingDef(key)
	if err != nil {
		return err
	}
	switch def.kind {
	case SettingInt:
		def.setInt(c.set, clampSettingValue(def.getInt(c.set)+delta*def.step, def.min, def.max))
	case SettingBool:
		def.setBool(c.set, !def.getBool(c.set))
	case SettingEnum:
		def.setStr(c.set, cycleSetting(def.getStr(c.set), def.opts, delta))
	default:
		return fmt.Errorf("设置项 %s 需要整段输入，不能用增减调整", key)
	}
	c.settingsDirty = true
	return nil
}

// SetSetting 按 key 整段写入（文本项用）。
func (c *Controller) SetSetting(key, raw string) error {
	def, err := c.settingDef(key)
	if err != nil {
		return err
	}
	switch def.kind {
	case SettingInt:
		// 数字项也允许整段写入：从 60 调到 480 不该靠按几十次 →。
		// 超范围的输入夹到区间内，和增减走同一套边界。
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("设置项 %s 需要整数，收到 %q", key, raw)
		}
		def.setInt(c.set, clampSettingValue(n, def.min, def.max))
		c.settingsDirty = true
		return nil
	case SettingText:
		// 继续走下面的文本分支。
	default:
		return fmt.Errorf("设置项 %s 不能用整段写入", key)
	}
	if def.dirName != "" {
		// 目录项：留空表示跟随根目录，解析成实际路径再存 —— 界面上显示的始终是
		// 实际生效的那个位置，而不是「空」。
		raw = c.set.ExpandDir(raw, def.dirName)
	}
	def.setStr(c.set, raw)
	c.settingsDirty = true
	return nil
}

// SettingValue 返回文本项的当前真实值，供编辑弹窗预填。
//
// 凭据类（Secret）项界面不会取用：输入框按密码模式回显，不预填原值，免得令牌铺在
// 屏幕与终端回滚缓冲里。
func (c *Controller) SettingValue(key string) (string, error) {
	def, err := c.settingDef(key)
	if err != nil {
		return "", err
	}
	switch def.kind {
	case SettingText:
		return def.getStr(c.set), nil
	case SettingInt:
		return strconv.Itoa(def.getInt(c.set)), nil
	default:
		return "", fmt.Errorf("设置项 %s 没有可整段编辑的值", key)
	}
}

// SaveSettings 把设置落盘，并清掉「未保存」标记。
func (c *Controller) SaveSettings() error {
	if c.set == nil {
		return fmt.Errorf("设置未加载")
	}
	// 先归一化再落盘。非法值（代理缺协议前缀、枚举写错）必须在这里就报出来：写进
	// 去之后下一次启动会在 Load 里直接失败，而那时界面已经关了，用户只看到程序
	// 起不来、也没人告诉他哪一项写错了。
	if err := c.set.Normalize(); err != nil {
		return err
	}
	if err := c.set.Save(); err != nil {
		return err
	}
	c.applyInstallRoot()
	c.settingsDirty = false
	return nil
}

// applyInstallRoot 把设置里的安装根目录同步给清单层。
//
// 清单层不认识 settings，所以由这里推过去：插件声明的 ${ROOT} 与「没写明安装路径的
// 软件」的落点都取自它，保存后不推的话，界面里改了位置、下一次检查还用旧值。
func (c *Controller) applyInstallRoot() {
	if c.afs == nil || c.set == nil {
		return
	}
	c.afs.SetInstallRoot(c.set.InstallRootDir())
}

// ResetSettings 恢复内置默认值（保留设置文件路径），并标记为待保存。
func (c *Controller) ResetSettings() error {
	if c.set == nil {
		return fmt.Errorf("设置未加载")
	}
	def := settings.Default()
	def.Path = c.set.Path
	if err := def.Normalize(); err != nil {
		return err
	}
	*c.set = *def
	c.applyInstallRoot()
	c.settingsDirty = true
	return nil
}

// SettingsDirty 报告有没有尚未落盘的修改。
//
// 由本层记而不是界面记：改设置的是本层，只有它知道哪次改动还没保存。
func (c *Controller) SettingsDirty() bool { return c.settingsDirty }

// SettingsPath 返回设置文件的落盘位置（界面上展示用）。
func (c *Controller) SettingsPath() string {
	if c.set == nil {
		return ""
	}
	return c.set.Path
}

// ManifestPath 返回清单导出路径（界面上展示用）。
func (c *Controller) ManifestPath() string {
	if c.set == nil {
		return ""
	}
	return c.set.ManifestPath()
}

// SettingsPaths 返回由启动方式决定、界面上改不了的位置，供界面只读展示。
//
// 能在界面上改的目录不在这里 —— 它们是设置项（storage.*_dir / logs.dir /
// plugins.dir），已经列在表单里。同一件事在屏幕上出现两遍、且一份能改一份不能，
// 只会让人先按错那个。
func (c *Controller) SettingsPaths() [][2]string {
	if c.set == nil {
		return nil
	}
	s := c.set
	return [][2]string{
		{"根目录", s.RootDir()},
		{"设置文件", s.Path},
		{"来源与配置", s.AppsPath()},
		{"清单导出", s.ManifestPath()},
	}
}

// settingDef 按 key 取定义（顺带做 nil 检查）。
func (c *Controller) settingDef(key string) (settingDef, error) {
	if c.set == nil {
		return settingDef{}, fmt.Errorf("设置未加载")
	}
	for _, def := range settingCatalog {
		if def.key == key {
			return def, nil
		}
	}
	return settingDef{}, fmt.Errorf("未知设置项 %q", key)
}

// settingText 把一项设置渲染成展示文本。
func settingText(s *settings.Settings, def settingDef) string {
	switch def.kind {
	case SettingBool:
		if def.getBool(s) {
			return "开"
		}
		return "关"
	case SettingInt:
		return strconv.Itoa(def.getInt(s))
	case SettingEnum:
		return def.getStr(s)
	default:
		if def.secret {
			if strings.TrimSpace(def.getStr(s)) == "" {
				return "—"
			}
			return "********"
		}
		if v := strings.TrimSpace(def.getStr(s)); v != "" {
			return v
		}
		return "—"
	}
}

// cycleSetting 在枚举选项间循环（当前值不在选项里时从第一项开始）。
func cycleSetting(cur string, opts []string, delta int) string {
	if len(opts) == 0 {
		return cur
	}
	idx := 0
	for i, o := range opts {
		if strings.EqualFold(o, cur) {
			idx = i
			break
		}
	}
	return opts[(idx+delta+len(opts))%len(opts)]
}

// clampSettingValue 把数值夹到区间内。
func clampSettingValue(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// splitSettingList 把逗号/分号/换行分隔的输入切成字段列表。
func splitSettingList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
