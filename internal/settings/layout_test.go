package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 便携布局的定位链：UPKIT_HOME > 可执行文件目录 > 当前目录。
// 前两级都在这里钉住，第三级只在别处都失败时才会走到。
func TestBaseDirLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	if got := BaseDir(); got != home {
		t.Fatalf("UPKIT_HOME 优先: %q，期望 %q", got, home)
	}

	// 没设 UPKIT_HOME 时退到可执行文件所在目录（测试里就是测试二进制的位置）。
	t.Setenv(EnvHome, "   ") // 空白也算没设，避免一个空变量把根目录定到当前目录
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到可执行文件路径: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if got := BaseDir(); got != filepath.Dir(exe) {
		t.Fatalf("应退到可执行文件目录: %q，期望 %q", got, filepath.Dir(exe))
	}
}

// 便携目录不可写（装在 Program Files 的典型场景）时，根目录必须退到用户配置目录，
// 否则设置根本存不下来。这个回退是「装到只读目录也能用」的关键路径。
func TestRootDirFallsBackToUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上目录权限不影响写入（ACL 语义不同），此路径只对类 Unix 有意义")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限，构造不出只读目录")
	}

	base := filepath.Join(t.TempDir(), "portable")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.Chmod(base, 0o555); err != nil {
		t.Fatalf("置只读: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o755) })

	cfg := t.TempDir()
	t.Setenv(EnvHome, base)
	t.Setenv("XDG_CONFIG_HOME", cfg)

	if dirWritable(base) {
		t.Fatal("只读目录不该被判为可写")
	}
	root, err := RootDir()
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	want := filepath.Join(cfg, "upkit")
	if root != want {
		t.Fatalf("根目录 = %q，期望 %q", root, want)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("回退目录应被创建: %v", err)
	}
}

// 可写时用便携布局：ConfigDir/DefaultPath 都落在 UPKIT_HOME 下面，并且目录已建好。
func TestConfigDirAndDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(home, DirConfig); dir != want {
		t.Fatalf("配置目录 = %q，期望 %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("配置目录应被创建: %v", err)
	}

	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, FileName); p != want {
		t.Fatalf("默认设置路径 = %q，期望 %q", p, want)
	}
}

// dirWritable 的三种情形：可写、目录本身只读、路径被一个普通文件挡住。
func TestDirWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("只读目录的语义在 Windows 上不同")
	}
	writable := t.TempDir()
	if !dirWritable(writable) {
		t.Fatal("普通临时目录应可写")
	}
	// 探针文件不该留下痕迹。
	if entries, err := os.ReadDir(writable); err != nil || len(entries) != 0 {
		t.Fatalf("探针文件应被删除: %v %v", entries, err)
	}

	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("写入占位文件: %v", err)
	}
	if dirWritable(filepath.Join(blocked, "sub")) {
		t.Fatal("路径被普通文件挡住时不该判为可写")
	}

	if os.Geteuid() == 0 {
		return
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o555); err != nil {
		t.Fatalf("建只读目录: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if dirWritable(ro) {
		t.Fatal("只读目录不该被判为可写")
	}
}

// Path 为空时 Save 自己推出默认路径，并且顺带把 config 目录建出来。
func TestSaveWithoutPathDerivesDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	s := Default()
	if s.Path != "" {
		t.Fatalf("默认设置的 Path 应为空，实际 %q", s.Path)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := filepath.Join(home, DirConfig, FileName); s.Path != want {
		t.Fatalf("Save 后 Path = %q，期望 %q", s.Path, want)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("设置文件没落盘: %v", err)
	}
	// 落盘内容能被读回来，且目录项已按根目录推导补全。
	got, err := Load(s.Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Logs.Dir != filepath.Join(home, DirLog) {
		t.Fatalf("日志目录应从根目录推导: %q", got.Logs.Dir)
	}
}

// 读文件失败但不属于「文件不存在」时要报错 —— 例如把目录当成设置文件传进来。
// 「不存在」是正常的首次运行，其它 I/O 错误默默吞掉会让用户以为设置生效了。
func TestLoadUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("把目录当设置文件读应报错")
	}
	// 空的 path 参数走默认路径解析；此时默认路径不可读（指向一个目录）也应报错。
	if _, err := Load(" "); err != nil {
		// 默认路径可读时不该报错，这里只保证不 panic。
		t.Logf("Load(空白路径) = %v", err)
	}
}

// 枚举字段：既要认合法值（含大小写/空白），也要明确拒绝非法值。
// 合法值过去一个都没被测过 —— 只测了默认值和非法值，中间那档是空的。
func TestNormalizeEnumValues(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Settings)
		check func(*Settings) string
		want  string
	}{
		{"日志 error", func(s *Settings) { s.Logs.Level = "ERROR" }, func(s *Settings) string { return s.Logs.Level }, "error"},
		{"日志 warn", func(s *Settings) { s.Logs.Level = " Warn " }, func(s *Settings) string { return s.Logs.Level }, "warn"},
		{"日志 debug", func(s *Settings) { s.Logs.Level = "debug" }, func(s *Settings) string { return s.Logs.Level }, "debug"},
		{"日志 trace", func(s *Settings) { s.Logs.Level = "trace" }, func(s *Settings) string { return s.Logs.Level }, "trace"},
		{"日志留空回默认", func(s *Settings) { s.Logs.Level = "" }, func(s *Settings) string { return s.Logs.Level }, "info"},
		{"停机策略 force", func(s *Settings) { s.Behavior.StopStrategy = "FORCE" }, func(s *Settings) string { return s.Behavior.StopStrategy }, "force"},
		{"停机策略留空回默认", func(s *Settings) { s.Behavior.StopStrategy = "" }, func(s *Settings) string { return s.Behavior.StopStrategy }, "graceful"},
		{"边框 square", func(s *Settings) { s.UI.Borders = "Square" }, func(s *Settings) string { return s.UI.Borders }, "square"},
		{"边框 ascii", func(s *Settings) { s.UI.Borders = "ascii" }, func(s *Settings) string { return s.UI.Borders }, "ascii"},
		{"边框留空回默认", func(s *Settings) { s.UI.Borders = "" }, func(s *Settings) string { return s.UI.Borders }, "unicode"},
		{"主题 dark", func(s *Settings) { s.UI.Theme = " Dark " }, func(s *Settings) string { return s.UI.Theme }, "dark"},
		{"主题 light", func(s *Settings) { s.UI.Theme = "LIGHT" }, func(s *Settings) string { return s.UI.Theme }, "light"},
		{"主题留空回默认", func(s *Settings) { s.UI.Theme = "" }, func(s *Settings) string { return s.UI.Theme }, "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Default()
			s.Path = filepath.Join(t.TempDir(), "settings.yaml")
			tc.mut(s)
			if err := s.Normalize(); err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if got := tc.check(s); got != tc.want {
				t.Fatalf("归一化后 = %q，期望 %q", got, tc.want)
			}
		})
	}

	invalid := []struct {
		name string
		mut  func(*Settings)
		key  string
	}{
		{"日志级别", func(s *Settings) { s.Logs.Level = "verbose" }, "logs.level"},
		{"停机策略", func(s *Settings) { s.Behavior.StopStrategy = "kill" }, "behavior.stop_strategy"},
		{"边框", func(s *Settings) { s.UI.Borders = "none" }, "ui.borders"},
		{"主题", func(s *Settings) { s.UI.Theme = "neon" }, "ui.theme"},
		{"代理缺协议", func(s *Settings) { s.Network.Proxy = "127.0.0.1:7890" }, "network.proxy"},
	}
	for _, tc := range invalid {
		t.Run("拒绝-"+tc.name, func(t *testing.T) {
			s := Default()
			s.Path = filepath.Join(t.TempDir(), "settings.yaml")
			tc.mut(s)
			err := s.Normalize()
			if err == nil {
				t.Fatalf("非法值应报错")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("错误信息应点名字段 %s: %v", tc.key, err)
			}
		})
	}
}

// 目录派生：留空跟随根目录，写了就展开（$VAR / ~ 都要认）。
func TestDerivedDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv("UPKIT_TEST_APPS", filepath.Join(home, "custom-apps"))

	s := Default()
	s.Path = filepath.Join(home, DirConfig, FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if s.RootDir() != home || s.ConfigDir() != filepath.Join(home, DirConfig) {
		t.Fatalf("布局不对: %q %q", s.RootDir(), s.ConfigDir())
	}
	if s.AppsPath() != filepath.Join(home, DirConfig, AppsFileName) {
		t.Fatalf("apps.yaml 路径不对: %q", s.AppsPath())
	}
	if s.ManifestPath() != filepath.Join(home, DirConfig, ManifestFileName) {
		t.Fatalf("清单路径不对: %q", s.ManifestPath())
	}
	if s.BackupRoot() != filepath.Join(home, DirBackup) {
		t.Fatalf("备份根目录不对: %q", s.BackupRoot())
	}
	if s.InstallRootDir() != filepath.Join(home, DirApps) {
		t.Fatalf("安装根目录默认应跟随根目录: %q", s.InstallRootDir())
	}

	// 显式配置的目录：环境变量与波浪号都要展开。
	s.Storage.InstallRoot = "$UPKIT_TEST_APPS"
	if got, want := s.InstallRootDir(), filepath.Join(home, "custom-apps"); got != want {
		t.Fatalf("安装根目录 = %q，期望 %q", got, want)
	}
	s.Storage.InstallRoot = "~/upkit-apps"
	userHome, err := os.UserHomeDir()
	if err == nil {
		if got, want := s.InstallRootDir(), filepath.Join(userHome, "upkit-apps"); got != want {
			t.Fatalf("~ 没展开: %q，期望 %q", got, want)
		}
	}
	s.Storage.InstallRoot = "   "
	if got, want := s.InstallRootDir(), filepath.Join(home, DirApps); got != want {
		t.Fatalf("纯空白应视为未配置: %q，期望 %q", got, want)
	}
}

// EnsureDirs 负责把整套目录建出来，并且跳过空项。
func TestEnsureDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	s := Default()
	s.Path = filepath.Join(home, DirConfig, FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := s.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, d := range []string{
		s.ConfigDir(), s.Storage.DataDir, s.Storage.CacheDir,
		s.Storage.TempDir, s.Storage.BackupDir, s.Logs.Dir, s.Plugins.Dir,
	} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Fatalf("目录没建出来 %q: %v", d, err)
		}
	}

	// 空目录项跳过而不是报错：手工把某一项清成空串不该让启动直接失败。
	s.Storage.DataDir = ""
	s.Storage.CacheDir = "  "
	if err := s.EnsureDirs(); err != nil {
		t.Fatalf("空目录项应跳过: %v", err)
	}
}

// 用不存在的路径当目录时 EnsureDirs 必须报错（父路径是个普通文件）。
func TestEnsureDirsError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("写占位文件: %v", err)
	}
	s := Default()
	s.Path = filepath.Join(t.TempDir(), "cfg", FileName)
	s.Storage.DataDir = filepath.Join(file, "data")
	if err := s.EnsureDirs(); err == nil {
		t.Fatal("目录建不出来时应报错")
	}
}

// 密钥解引用：env: / 明文 / cmd: 三种；cmd: 固定走 Windows 的 cmd /c，
// 在非 Windows 上必然拿不到值 —— 这里把「拿不到就是空」这个契约钉住。
func TestResolveTokenRefs(t *testing.T) {
	t.Setenv("UPKIT_TEST_TOKEN", "s3cret")

	if got := ResolveToken(""); got != "" {
		t.Fatalf("空引用应为空，实际 %q", got)
	}
	if got := ResolveToken("  "); got != "" {
		t.Fatalf("空白引用应为空，实际 %q", got)
	}
	if got := ResolveToken("env:UPKIT_TEST_TOKEN"); got != "s3cret" {
		t.Fatalf("env 引用没解析: %q", got)
	}
	if got := ResolveToken("env:UPKIT_TEST_TOKEN_MISSING"); got != "" {
		t.Fatalf("缺失的环境变量应为空，实际 %q", got)
	}
	if got := ResolveToken("ghp_plaintext"); got != "ghp_plaintext" {
		t.Fatalf("明文应原样返回: %q", got)
	}
	if got := ResolveToken("cmd:echo hi"); runtime.GOOS != "windows" && got != "" {
		t.Fatalf("非 Windows 上 cmd: 引用应为空，实际 %q", got)
	}
	if got := ResolveToken("cmd:   "); got != "" {
		t.Fatalf("空的 cmd 引用应为空，实际 %q", got)
	}

	// 引用形式不落盘、日志里也不回显明文，因此脱敏时保持原样；明文一律打码。
	if !IsSecretRef("env:A") || !IsSecretRef("cmd:B") || IsSecretRef("ghp_x") {
		t.Fatal("IsSecretRef 判定不对")
	}
	if got := MaskSecret("ghp_plaintext"); got != "***" {
		t.Fatalf("明文应打码，实际 %q", got)
	}
	if got := MaskSecret("env:GITHUB_TOKEN"); got != "env:GITHUB_TOKEN" {
		t.Fatalf("引用应保持原样，实际 %q", got)
	}
	if got := MaskSecret("   "); got != "" {
		t.Fatalf("空值脱敏后仍为空，实际 %q", got)
	}
}

// Token 的兜底来源：设置里没写时按 GITHUB_TOKEN → GH_TOKEN 顺序取。
func TestNormalizeTokenFromEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "from-gh")
	s := Default()
	s.Path = filepath.Join(t.TempDir(), FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s.Network.GitHubToken != "from-gh" {
		t.Fatalf("应回退到 GH_TOKEN，实际 %q", s.Network.GitHubToken)
	}

	t.Setenv("GITHUB_TOKEN", "from-gh-token")
	s2 := Default()
	s2.Path = s.Path
	if err := s2.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s2.Network.GitHubToken != "from-gh-token" {
		t.Fatalf("GITHUB_TOKEN 优先，实际 %q", s2.Network.GitHubToken)
	}

	// 设置里写了就不该被环境变量盖掉。
	s3 := Default()
	s3.Path = s.Path
	s3.Network.GitHubToken = "env:GITHUB_TOKEN"
	if err := s3.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s3.Network.GitHubToken != "env:GITHUB_TOKEN" {
		t.Fatalf("显式配置不该被覆盖，实际 %q", s3.Network.GitHubToken)
	}
}

// Touch 用于界面显示「配置最后修改时间」：存在时给 mtime，不存在时给零值。
func TestTouch(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	s := Default()
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := s.Touch()
	if got.IsZero() {
		t.Fatal("刚写完的设置应有 mtime")
	}
	if d := time.Since(got); d > time.Minute || d < -time.Minute {
		t.Fatalf("mtime 明显不对: %v", got)
	}

	missing := Default()
	missing.Path = filepath.Join(home, "nope.yaml")
	if !missing.Touch().IsZero() {
		t.Fatal("文件不存在时应返回零值")
	}
}
