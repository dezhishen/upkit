package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 便携布局：设置、清单、日志、插件等全部落在 config 的上一级目录下。
func TestPortableLayout(t *testing.T) {
	root := t.TempDir()
	s := Default()
	s.Path = filepath.Join(root, DirConfig, FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	want := map[string]string{
		"data":   filepath.Join(root, DirData),
		"cache":  filepath.Join(root, DirCache),
		"temp":   filepath.Join(root, DirTemp),
		"backup": filepath.Join(root, DirBackup),
		"log":    filepath.Join(root, DirLog),
		"plugin": filepath.Join(root, DirPlugin),
	}
	got := map[string]string{
		"data":   s.Storage.DataDir,
		"cache":  s.Storage.CacheDir,
		"temp":   s.Storage.TempDir,
		"backup": s.Storage.BackupDir,
		"log":    s.Logs.Dir,
		"plugin": s.Plugins.Dir,
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Fatalf("%s 目录应为 %s，实际 %s", name, expected, got[name])
		}
	}

	if s.RootDir() != root {
		t.Fatalf("根目录应为 %s，实际 %s", root, s.RootDir())
	}
	if s.ConfigDir() != filepath.Join(root, DirConfig) {
		t.Fatalf("设置目录应为 %s，实际 %s", filepath.Join(root, DirConfig), s.ConfigDir())
	}
	if s.AppsPath() != filepath.Join(root, DirConfig, AppsFileName) {
		t.Fatalf("清单路径不正确: %s", s.AppsPath())
	}
	if s.ManifestPath() != filepath.Join(root, DirConfig, ManifestFileName) {
		t.Fatalf("清单导出路径不正确: %s", s.ManifestPath())
	}
	if s.BackupDir("demo") != filepath.Join(root, DirBackup, "demo") {
		t.Fatalf("软件备份目录不正确: %s", s.BackupDir("demo"))
	}

	// EnsureDirs 应真正建出目录
	if err := s.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for name, dir := range want {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("%s 目录未创建: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录：%s", name, dir)
		}
	}
}

// 用 --config 指向别处时，以该文件所在目录为根布局。
func TestRootForCustomConfig(t *testing.T) {
	dir := t.TempDir()
	s := Default()
	s.Path = filepath.Join(dir, "my-settings.yaml")
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if s.RootDir() != dir {
		t.Fatalf("根目录应为 %s，实际 %s", dir, s.RootDir())
	}
	if s.Logs.Dir != filepath.Join(dir, DirLog) {
		t.Fatalf("日志目录应为 %s，实际 %s", filepath.Join(dir, DirLog), s.Logs.Dir)
	}
}

// UPKIT_HOME 可覆盖便携根目录。
func TestBaseDirFromEnvHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	want := home
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		want = resolved
	}
	if got := BaseDir(); got != want {
		t.Fatalf("BaseDir 应为 %s，实际 %s", want, got)
	}
}

// 默认值应通过 Normalize 补全出可用的目录。
func TestNormalizeDerivesDirs(t *testing.T) {
	s := Default()
	s.Path = filepath.Join(t.TempDir(), FileName)
	s.Storage.DataDir = filepath.Join(t.TempDir(), "data")
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for name, got := range map[string]string{
		"cache": s.Storage.CacheDir,
		"temp":  s.Storage.TempDir,
		"logs":  s.Logs.Dir,
	} {
		if got == "" {
			t.Fatalf("%s 目录未派生", name)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("%s 目录应为绝对路径: %s", name, got)
		}
	}
}

// 目录项的解析规则：留空跟随根目录，填了就用填的（并展开 ~ 与相对写法）。
//
// 设置面板显示的就是这个结果，落盘用的「是否跟随根目录」判断（snapshot）也依赖
// 它 —— 两处必须一致。
func TestExpandDir(t *testing.T) {
	root := t.TempDir()
	s := Default()
	s.Path = filepath.Join(root, DirConfig, FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	custom := filepath.Join(t.TempDir(), "elsewhere")
	cases := []struct {
		name, in, want string
	}{
		{"留空跟随根目录", "", filepath.Join(root, DirData)},
		{"只有空白也跟随根目录", "   ", filepath.Join(root, DirData)},
		{"自定义目录原样使用", custom, custom},
	}
	for _, c := range cases {
		if got := s.ExpandDir(c.in, DirData); got != c.want {
			t.Fatalf("%s: %q -> %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// 首次运行（设置文件还不存在）也要拿到补全好的目录。
//
// 漏掉这一步的话，调用方拿到的是目录字段全空的设置：它得自己再 Normalize 一次，
// 忘了就是「数据写进了当前目录」这类问题。
func TestLoadMissingFileDerivesDirs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DirConfig, FileName)

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Path != path {
		t.Fatalf("Path 应指向将要创建的位置，实际 %s", s.Path)
	}
	if s.Storage.DataDir != filepath.Join(root, DirData) {
		t.Fatalf("数据目录应为 %s，实际 %s", filepath.Join(root, DirData), s.Storage.DataDir)
	}
	if s.Logs.Dir != filepath.Join(root, DirLog) || s.Plugins.Dir != filepath.Join(root, DirPlugin) {
		t.Fatalf("日志/插件目录未派生: %s / %s", s.Logs.Dir, s.Plugins.Dir)
	}
}

// 首次运行落盘：Bootstrap 写出默认设置，但绝不覆盖已经存在的文件。
//
// 「已存在就不动」是关键：默认值覆盖用户改过的配置，比不写文件严重得多。
func TestBootstrapKeepsExistingFile(t *testing.T) {
	root := t.TempDir()
	s := Default()
	s.Path = filepath.Join(root, DirConfig, FileName)
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := s.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	if err := s.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("设置文件应被写出: %v", err)
	}
	if s.Touch().IsZero() {
		t.Fatalf("Touch 应返回写入后的 mtime")
	}

	if err := os.WriteFile(s.Path, []byte("network:\n  proxy: http://127.0.0.1:7890\n"), 0o644); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if err := s.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	got, err := Load(s.Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Network.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("Bootstrap 不应覆盖已有设置，实际 %q", got.Network.Proxy)
	}
}

// 日志级别的数值序（界面与日志过滤都按它比较）。
func TestLevelOrder(t *testing.T) {
	s := Default()
	for level, want := range map[string]int{
		"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "": 2, "乱七八糟": 2,
	} {
		s.Logs.Level = level
		if got := s.Level(); got != want {
			t.Fatalf("级别 %q 应为 %d，实际 %d", level, want, got)
		}
	}
}

// 代理与日志级别等非法值应被拒绝。
func TestNormalizeValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Settings)
	}{
		{"日志级别非法", func(s *Settings) { s.Logs.Level = "verbose" }},
		{"代理缺少协议", func(s *Settings) { s.Network.Proxy = "127.0.0.1:7890" }},
		{"结束策略非法", func(s *Settings) { s.Behavior.StopStrategy = "whatever" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Default()
			s.Path = filepath.Join(t.TempDir(), FileName)
			s.Storage.DataDir = t.TempDir()
			c.mut(s)
			if err := s.Normalize(); err == nil {
				t.Fatalf("期望校验失败")
			}
		})
	}
}

// 保存后再读取，关键字段应保持不变。
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	s := Default()
	s.Storage.DataDir = filepath.Join(dir, "data")
	s.Network.Proxy = "http://127.0.0.1:7890"
	s.Engine.ApplyConcurrency = 7
	s.Path = path
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Network.Proxy != "http://127.0.0.1:7890" || got.Engine.ApplyConcurrency != 7 {
		t.Fatalf("往返后字段不一致: %+v", got.Network)
	}
	if got.Path != path {
		t.Fatalf("Path 未回填: %s", got.Path)
	}
}

// 未知字段应被拒绝（避免拼写错误被静默忽略）。
func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("unknown_section:\n  a: 1\n"), 0o644); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("期望解析失败")
	}
}

func TestTokenHelpers(t *testing.T) {
	s := Default()
	s.Network.GitHubToken = "env:UPKIT_TEST_TOKEN_UNSET"
	t.Setenv("UPKIT_TEST_TOKEN_UNSET", "")
	if got := ResolveToken(s.Network.GitHubToken); got != "" {
		t.Fatalf("未设置的环境变量应解析为空，得到 %q", got)
	}
	t.Setenv("UPKIT_TEST_TOKEN_UNSET", "secret-value")
	if got := ResolveToken(s.Network.GitHubToken); got != "secret-value" {
		t.Fatalf("env: 引用未解析: %q", got)
	}
	if got := ResolveToken("cmd:   "); got != "" {
		t.Fatalf("cmd: 后面没写命令时应返回空，得到 %q", got)
	}
	if !IsSecretRef(s.Network.GitHubToken) {
		t.Fatalf("env: 应被识别为引用而非明文")
	}
	if MaskSecret("abcdef123456") == "abcdef123456" {
		t.Fatalf("明文应被脱敏")
	}
}

// BackupDir 应按软件 id 派生目录。
func TestBackupDir(t *testing.T) {
	s := Default()
	s.Path = filepath.Join(t.TempDir(), FileName)
	s.Storage.DataDir = t.TempDir()
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	got := s.BackupDir("chromium")
	if filepath.Base(got) != "chromium" {
		t.Fatalf("备份目录应以软件 id 结尾: %s", got)
	}
}

// 示例配置必须与当前 schema 保持一致，防止字段漂移（KnownFields 会拒绝未知字段）。
func TestExampleConfigParses(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "settings.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("示例配置不存在: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("示例配置无法解析: %v", err)
	}
	if s.Network.TimeoutSeconds <= 0 || s.Logs.Level == "" {
		t.Fatalf("示例配置缺少关键字段: %+v", s.Network)
	}
	// 示例文件位于 <repo>/configs 下，派生目录不应改变示例文件内容
	if !filepath.IsAbs(s.Storage.CacheDir) {
		t.Fatalf("示例配置的缓存目录未派生: %s", s.Storage.CacheDir)
	}
}

// 保存设置时不应把「按布局推导的目录」写死，保证整目录搬迁后路径自动跟随；
// 用户手工填写的自定义目录必须保留。
func TestSaveKeepsLayoutPortable(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(t.TempDir(), "my-cache")

	s := Default()
	s.Path = filepath.Join(root, DirConfig, FileName)
	s.Storage.CacheDir = custom
	if err := s.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	text := string(data)
	if strings.Contains(text, root) || strings.Contains(text, filepath.ToSlash(root)) {
		t.Fatalf("设置文件不应写死推导出的根目录路径:\n%s", text)
	}
	if !strings.Contains(text, custom) && !strings.Contains(text, filepath.ToSlash(custom)) {
		t.Fatalf("自定义缓存目录应被保留:\n%s", text)
	}

	// 重新加载后目录依然按布局推导
	got, err := Load(s.Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Logs.Dir != filepath.Join(root, DirLog) {
		t.Fatalf("日志目录应重新推导为 %s，实际 %s", filepath.Join(root, DirLog), got.Logs.Dir)
	}
	if got.Storage.CacheDir != custom {
		t.Fatalf("自定义缓存目录应保留，实际 %s", got.Storage.CacheDir)
	}
}
