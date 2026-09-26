package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

// 清单为空时 Load 应返回带默认冲突策略的结构。
func TestLoadMissingFileReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.yaml")
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Version != 2 || f.Conflicts.SameTarget != "block" {
		t.Fatalf("默认值不正确: %+v", f)
	}
	if len(f.Apps) != 0 {
		t.Fatalf("期望空清单，得到 %d 个条目", len(f.Apps))
	}
}

// 便携式软件的默认推断：unpack=zip、探测链含 pe-resource、进程名回退为入口文件。
func TestBuildPortableDefaults(t *testing.T) {
	f := Default()
	f.Apps = []AppSpec{{
		ID:      "demo",
		Name:    "Demo",
		Install: InstallSpec{Path: filepath.Join(t.TempDir(), "Demo"), Entrypoints: []string{"demo.exe"}},
	}}
	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("期望 1 个 AppRef，得到 %d", len(refs))
	}
	r := refs[0]
	if r.Method != "portable-inplace" || r.Unpack != "zip" || r.Source != "github-release" {
		t.Fatalf("默认适配器不正确: %+v", r)
	}
	if len(r.Detect) == 0 || r.Detect[0] != "state-file" {
		t.Fatalf("探测链不正确: %v", r.Detect)
	}
	if len(r.Processes) != 1 || r.Processes[0] != "demo.exe" {
		t.Fatalf("进程名应回退为入口文件: %v", r.Processes)
	}
	if len(r.Preserve) == 0 {
		t.Fatalf("默认保护路径不应为空")
	}
}

// 安装器类软件的默认推断：unpack=raw、探测链含 cli-version。
func TestBuildInstallerDefaults(t *testing.T) {
	f := Default()
	f.Apps = []AppSpec{{
		ID:      "setup-demo",
		Method:  map[string]any{"kind": "exe-installer", "args": "/S"},
		Install: InstallSpec{Path: filepath.Join(t.TempDir(), "SetupDemo")},
	}}
	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if refs[0].Unpack != "raw" {
		t.Fatalf("安装器应使用 raw 解包，得到 %q", refs[0].Unpack)
	}
	if refs[0].MethodOpts["args"] != "/S" {
		t.Fatalf("方法选项未解析: %v", refs[0].MethodOpts)
	}
	hasCLI := false
	for _, d := range refs[0].Detect {
		hasCLI = hasCLI || d == "cli-version"
	}
	if !hasCLI {
		t.Fatalf("安装器探测链应包含 cli-version: %v", refs[0].Detect)
	}
}

// 缺少 id 或安装路径应报错；重复 id 也应报错。
func TestBuildValidation(t *testing.T) {
	cases := map[string][]AppSpec{
		"缺少 id":   {{Install: InstallSpec{Path: "/tmp/x"}}},
		"缺少路径":    {{ID: "a"}},
		"重复 id":   {{ID: "a", Install: InstallSpec{Path: "/tmp/a"}}, {ID: "a", Install: InstallSpec{Path: "/tmp/b"}}},
		"id 含 ..": {{ID: "a..b", Install: InstallSpec{Path: "/tmp/a"}}},
	}
	for name, specs := range cases {
		t.Run(name, func(t *testing.T) {
			f := Default()
			f.Apps = specs
			if _, err := f.Build(); err == nil {
				t.Fatalf("期望报错，但成功了")
			}
		})
	}
}

// 启用状态：软件由插件提供（不从文件读），启停开关落在来源声明上并写回文件。
func TestSourceAppEnabledRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.yaml")
	writeFile(t, path, `version: 2
sources:
  - id: demo-src
    kind: plugin
    exec: demo-src.exe
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 插件提供的软件是运行时字段，这里直接模拟插件已填充。
	f.Apps = []AppSpec{{
		ID:      "demo",
		Name:    "Demo",
		Source:  map[string]any{"kind": "plugin:demo-src", "app": "demo"},
		Install: InstallSpec{Path: "/tmp/demo"},
	}}

	if !f.Enabled("demo") {
		t.Fatalf("未声明 enabled 时应默认启用")
	}
	if err := f.SetEnabled("demo", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if f.Enabled("demo") {
		t.Fatalf("停用未在内存生效")
	}

	again, err := Load(path)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if again.SourceAppEnabled("demo-src", "demo") {
		t.Fatalf("停用状态未落盘到来源声明")
	}
}

// 软件不由文件声明：写进去的 apps 段会被严格模式拒绝，避免回退到手工清单。
func TestAppsFieldIsNotSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.yaml")
	f := Default()
	f.Path = path
	f.Apps = []AppSpec{{ID: "demo", Install: InstallSpec{Path: "/tmp/demo"}}}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取: %v", err)
	}
	if strings.Contains(string(data), "demo") {
		t.Fatalf("运行时软件条目不应落盘，实际写入：\n%s", data)
	}
}
