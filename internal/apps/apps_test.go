package apps

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/settings"
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

// 插件来源的软件用限定 ID（<来源ID>/<软件ID>）写进清单，必须能通过 Build 的校验。
//
// 没有它，插件一加载成功界面就只剩一句「apps[0]: id 含非法字符 "/"」—— 而限定 ID 里
// 的 "/" 是设计的一部分（QualifiedIDSeparator），不是谁写错了。
func TestBuildAcceptsQualifiedPluginAppID(t *testing.T) {
	f := Default()
	f.Apps = []AppSpec{{
		ID:      "upkit-hub/ungoogled-chromium",
		Name:    "ungoogled-chromium",
		Source:  map[string]any{KeyKind: SourceKindPluginPrefix + "upkit-hub"},
		Install: InstallSpec{Path: filepath.Join(t.TempDir(), "Chromium"), Entrypoints: []string{"chrome.exe"}},
	}}
	refs, err := f.Build()
	if err != nil {
		t.Fatalf("限定 ID 应当能通过校验: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != "upkit-hub/ungoogled-chromium" {
		t.Fatalf("限定 ID 不应被改写: %+v", refs)
	}
}

// 放行限定 ID 不等于给 "/" 开任意口子。
func TestValidateIDQualifiedForms(t *testing.T) {
	for _, id := range []string{"a/b/c", "a//b", "a/", "/b", "..", "a/..", "a/../b", "a/ b", "a/é"} {
		if err := validateID(id); err == nil {
			t.Errorf("id %q 应当被拒绝", id)
		}
	}
	for _, id := range []string{"demo", "upkit-hub/ungoogled-chromium", "a.b/c-d_e"} {
		if err := validateID(id); err != nil {
			t.Errorf("id %q 应当被接受，实际 %v", id, err)
		}
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

// ${ROOT} 必须展开成传入的安装根目录。
//
// 这是真实缺陷的回归：宿主此前只做 ExpandPath（os.ExpandEnv），而 ROOT 不是环境变量，
// 于是插件写的 "${ROOT}/fzf" 被展开成空串拼出来的 "\fzf" —— 软件装到了当前盘的根
// 目录，而不是用户设定的位置，而 SDK 文档一直声称支持 ${ROOT}。
func TestInstallPathExpandsRootVar(t *testing.T) {
	root := t.TempDir()
	f := Default(WithInstallRoot(root))
	f.Apps = []AppSpec{{
		ID:      "fzf",
		Name:    "fzf",
		Install: InstallSpec{Path: "${ROOT}/fzf"},
	}}

	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := filepath.Join(root, "fzf"); refs[0].InstallPath != want {
		t.Fatalf("${ROOT} 未展开成安装根目录：%q != %q", refs[0].InstallPath, want)
	}
}

// ${ARCH} 展开成当前平台架构；写死了绝对路径的条目不受安装根目录影响。
func TestInstallPathExpandsArchAndKeepsAbsolute(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(t.TempDir(), "Fixed")
	f := Default(WithInstallRoot(root))
	f.Apps = []AppSpec{
		{ID: "arch", Name: "Arch", Install: InstallSpec{Path: "${ROOT}/${ARCH}/tool"}},
		{ID: "fixed", Name: "Fixed", Install: InstallSpec{Path: abs}},
	}

	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := filepath.Join(root, runtime.GOARCH, "tool"); refs[0].InstallPath != want {
		t.Fatalf("${ARCH} 未展开：%q != %q", refs[0].InstallPath, want)
	}
	if refs[1].InstallPath != abs {
		t.Fatalf("写死的路径不该被改：%q != %q", refs[1].InstallPath, abs)
	}
}

// 没写明安装路径的软件落到 <安装根目录>/<软件名>；没有根目录时仍然报错（不猜位置）。
func TestInstallPathFallsBackToInstallRoot(t *testing.T) {
	root := t.TempDir()
	f := Default(WithInstallRoot(root))
	f.Apps = []AppSpec{{ID: "corp/vpn", Name: "Corp VPN"}}

	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := filepath.Join(root, "Corp_VPN"); refs[0].InstallPath != want {
		t.Fatalf("未写明路径时应落到 %q，实际 %q", want, refs[0].InstallPath)
	}

	// 名字为空时退回 ID（限定 ID 里的斜杠会被清洗掉，不能当成子目录）。
	f2 := Default(WithInstallRoot(root))
	f2.Apps = []AppSpec{{ID: "corp/vpn"}}
	refs2, err := f2.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := filepath.Join(root, "corp_vpn"); refs2[0].InstallPath != want {
		t.Fatalf("无名字时应落到 %q，实际 %q", want, refs2[0].InstallPath)
	}

	// 没有安装根目录 = 不猜：还是报缺少 install.path。
	f3 := Default()
	f3.Apps = []AppSpec{{ID: "no-path", Name: "NoPath"}}
	if _, err := f3.Build(); err == nil || !strings.Contains(err.Error(), "install.path") {
		t.Fatalf("没有安装根目录时应报缺少 install.path，实际 %v", err)
	}
}

// apps 包不知道 settings，两边的默认子目录名必须一致（这里钉住）。
func TestDefaultInstallRootNameMatchesSettings(t *testing.T) {
	if DefaultInstallRootName != settings.DirApps {
		t.Fatalf("默认安装子目录名不一致：apps=%q settings=%q",
			DefaultInstallRootName, settings.DirApps)
	}
}

// 强制安装根目录：插件把目录写死在别处时，用户仍能要求「全部装到一个目录下」。
//
// 官方订阅里就有这种声明：`${LOCALAPPDATA}/UngoogledChromium` —— 它不受安装根目录
// 影响，于是「我想让所有软件都在 C:\apps 下面」根本做不到。
func TestForceInstallRootOverridesPortablePath(t *testing.T) {
	root := t.TempDir()
	f := Default(WithInstallRoot(root), WithForceInstallRoot(true))
	f.Apps = []AppSpec{{
		ID: "ungoogled-chromium", Name: "Ungoogled Chromium",
		Source:  map[string]any{"kind": "github-release", "repo": "o/r"},
		Method:  map[string]any{"kind": "portable-inplace"},
		Install: InstallSpec{Path: filepath.Join(root, "别处", "Chromium")},
	}}

	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// 目录名按文件名规则清洗（空格换成下划线），和「没写安装路径」时的兜底一致。
	want := filepath.Join(root, "Ungoogled_Chromium")
	if refs[0].InstallPath != want {
		t.Fatalf("强制根目录后应装到 %q，实际 %q", want, refs[0].InstallPath)
	}

	// 关掉开关就回到插件声明的路径。
	f.SetForceInstallRoot(false)
	refs, err = f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if refs[0].InstallPath != filepath.Join(root, "别处", "Chromium") {
		t.Fatalf("关掉开关后应恢复声明路径，实际 %q", refs[0].InstallPath)
	}
}

// 由安装器/插件决定落点的方法不受强制根目录影响：那里的路径是「去哪找它」，
// 改成我们的目录只会让探测永远找不到。
func TestForceInstallRootLeavesInstallerMethods(t *testing.T) {
	root := t.TempDir()
	declared := filepath.Join(root, "按安装器自己的规矩")
	for _, method := range []string{"msiexec", "exe-installer", "plugin"} {
		f := Default(WithInstallRoot(root), WithForceInstallRoot(true))
		f.Apps = []AppSpec{{
			ID: "x", Name: "X",
			Method:  map[string]any{"kind": method},
			Install: InstallSpec{Path: declared},
		}}
		refs, err := f.Build()
		if err != nil {
			t.Fatalf("Build(%s): %v", method, err)
		}
		if refs[0].InstallPath != declared {
			t.Fatalf("%s 的落点不该被强制改写：%q", method, refs[0].InstallPath)
		}
	}
}

// 停用的软件照样展开，并且带上标记：界面要能显示它、再启回来。
func TestBuildKeepsDisabledAppsMarked(t *testing.T) {
	root := t.TempDir()
	off := false
	f := Default(WithInstallRoot(root))
	f.Apps = []AppSpec{
		{ID: "on", Name: "On", Enabled: nil},
		{ID: "off", Name: "Off", Enabled: &off},
	}

	refs, err := f.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("停用的软件也该展开（用户要能看见它）：%+v", refs)
	}
	byID := map[string]core.AppRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	if byID["on"].Disabled {
		t.Fatal("启用的软件不该带停用标记")
	}
	if !byID["off"].Disabled {
		t.Fatal("停用的软件应带停用标记")
	}
}
