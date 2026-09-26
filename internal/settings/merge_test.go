package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	return path
}

// TestExplicitFalseOverridesDefault 是「开关关不掉」的回归用例。
//
// 旧实现把用户值解码到空结构体、再 merge 到默认值上，而 merge 判断「是否设置过」
// 只能靠零值 —— 对默认 true 的布尔项来说 false 同时是用户意图和零值，
// 于是被默认值覆盖回去。表现是：yaml 里明明写着 false，重启后又变回 true。
func TestExplicitFalseOverridesDefault(t *testing.T) {
	path := writeSettings(t, `behavior:
  confirm_before_apply: false
  wait_for_close: false
logs:
  audit: false
  compress: false
  redact: false
plugins:
  auto_load_trusted: false
`)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if s.Behavior.ConfirmBeforeApply {
		t.Error("confirm_before_apply 写成 false 后仍然是 true")
	}
	if s.Behavior.WaitForClose {
		t.Error("wait_for_close 写成 false 后仍然是 true")
	}
	if s.Logs.Audit {
		t.Error("audit 写成 false 后仍然是 true")
	}
	if s.Logs.Compress {
		t.Error("compress 写成 false 后仍然是 true")
	}
	if s.Logs.Redact {
		t.Error("redact 写成 false 后仍然是 true")
	}
	if s.Plugins.AutoLoadTrusted {
		t.Error("auto_load_trusted 写成 false 后仍然是 true")
	}
}

// TestAbsentFieldsKeepDefaults 确认上一条的修复没有走过头：
// 文件里没写的字段仍应拿到默认值，而不是变成零值。
func TestAbsentFieldsKeepDefaults(t *testing.T) {
	path := writeSettings(t, `network:
  proxy: http://127.0.0.1:7890
`)
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := Default()

	for _, tc := range []struct {
		name     string
		got, def any
	}{
		{"确认开关", got.Behavior.ConfirmBeforeApply, def.Behavior.ConfirmBeforeApply},
		{"日志脱敏", got.Logs.Redact, def.Logs.Redact},
		{"日志审计", got.Logs.Audit, def.Logs.Audit},
		{"备份份数", got.Storage.BackupKeep, def.Storage.BackupKeep},
		{"下载并发", got.Engine.DownloadConcurrency, def.Engine.DownloadConcurrency},
		{"日志级别", got.Logs.Level, def.Logs.Level},
	} {
		if tc.got != tc.def {
			t.Errorf("%s：未在文件里出现时应保持默认值 %v，实际 %v", tc.name, tc.def, tc.got)
		}
	}

	// 写了的字段要生效。
	if got.Network.Proxy != "http://127.0.0.1:7890" {
		t.Errorf("proxy 未生效: %q", got.Network.Proxy)
	}
}

// TestNormalizeFillsPaths 确认 Normalize 仍然把派生路径补全 ——
// 它是 merge 之外的另一半逻辑，不能在重构里丢掉。
func TestNormalizeFillsPaths(t *testing.T) {
	path := writeSettings(t, `{}`)
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	root := filepath.Dir(filepath.Dir(path)) // <tmp>/config/settings.yaml -> <tmp>

	for _, tc := range []struct {
		name, got, want string
	}{
		{"数据目录", s.Storage.DataDir, filepath.Join(root, DirData)},
		{"缓存目录", s.Storage.CacheDir, filepath.Join(root, DirCache)},
		{"备份目录", s.Storage.BackupDir, filepath.Join(root, DirBackup)},
		{"日志目录", s.Logs.Dir, filepath.Join(root, DirLog)},
		{"插件目录", s.Plugins.Dir, filepath.Join(root, DirPlugin)},
	} {
		if !strings.HasSuffix(tc.got, filepath.Base(tc.want)) {
			t.Errorf("%s = %q，期望以 %q 结尾", tc.name, tc.got, tc.want)
		}
		if tc.got == "" {
			t.Errorf("%s 不应为空", tc.name)
		}
	}
}

// TestUnknownFieldRejected 确认严格模式仍在：写错字段名必须报错而不是静默忽略。
func TestUnknownFieldRejected(t *testing.T) {
	path := writeSettings(t, "not_a_real_field: 1\n")
	if _, err := Load(path); err == nil {
		t.Fatal("未知字段应报错，否则用户写错了也发现不了")
	}
}

// TestEmptyFileUsesDefaults 空文件不能报错，应等同于「全默认」。
func TestEmptyFileUsesDefaults(t *testing.T) {
	path := writeSettings(t, "")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("空设置文件应可用，得到 %v", err)
	}
	def := Default()
	if got.Behavior.ConfirmBeforeApply != def.Behavior.ConfirmBeforeApply {
		t.Error("空文件应保持默认值")
	}
}

// TestMissingFileUsesDefaults 文件不存在时返回默认值，并把 Path 指到将创建的位置。
func TestMissingFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "settings.yaml")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Path != path {
		t.Errorf("Path = %q，期望 %q", got.Path, path)
	}
	if !got.Logs.Redact {
		t.Error("默认应开启日志脱敏")
	}
}
