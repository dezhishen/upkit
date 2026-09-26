package pluginhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
)

func boolPtr(v bool) *bool { return &v }

// buildExample 编译示例插件到 dir，失败时跳过（例如离线环境）。
func buildExample(t *testing.T, dir string) string {
	t.Helper()
	// 插件文件名恒为 <id>.exe：upkit 只发行 Windows 版本，开发机上也一样。
	out := filepath.Join(dir, "example-static"+extExec)
	cmd := exec.Command("go", "build", "-o", out, "github.com/dezhishen/upkit/cmd/upkit-plugin-example")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("无法编译示例插件: %v\n%s", err, b)
	}
	return out
}

func writeManifest(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".plugin.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("写插件描述失败: %v", err)
	}
}

func TestDiscoverAndResolveExec(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "alpha", "id: alpha\nname: 甲\nmode: full\nexec: alpha.bin\n")
	writeManifest(t, dir, "beta", "name: 乙\n")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("期望 2 个描述，实际 %d: %+v", len(found), found)
	}
	// 按 ID 排序，alpha 在前；beta 没写 id，应从文件名推断。
	if found[0].ID != "alpha" || found[0].Mode != "full" {
		t.Fatalf("alpha 解析错误: %+v", found[0])
	}
	if found[1].ID != "beta" || found[1].Name != "乙" {
		t.Fatalf("beta 解析错误: %+v", found[1])
	}

	if got := ResolveExec(dir, "alpha", "alpha.bin"); got != filepath.Join(dir, "alpha.bin") {
		t.Fatalf("相对路径解析错误: %s", got)
	}
	abs := filepath.Join(dir, "abs.bin")
	if got := ResolveExec(dir, "alpha", abs); got != abs {
		t.Fatalf("绝对路径应原样返回: %s", got)
	}

	// 空目录不应报错。
	empty, err := Discover(filepath.Join(dir, "nope"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("空目录处理错误: %v %+v", err, empty)
	}
}

func TestHashFileAndTrusted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	// echo -n hello | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if sha != want {
		t.Fatalf("哈希不符: %s", sha)
	}
	if !Trusted(want, sha) || !Trusted("  "+want+" ", sha) {
		t.Fatal("相同哈希应当视为可信")
	}
	if Trusted("", sha) || Trusted("deadbeef", sha) {
		t.Fatal("空记录或不同哈希必须视为不可信")
	}
}

func TestToAppSpecMapsPluginMetadata(t *testing.T) {
	sw := upkitplugin.Software{
		ID:          "corp-vpn",
		Name:        "公司 VPN",
		Description: "内网",
		Tags:        []string{"办公"},
		Provides:    []string{"corp-vpn"},
		Target: &upkitplugin.TargetHint{
			PathTemplate: "${ROOT}/CorpVPN",
			Entrypoints:  []string{"vpn.exe"},
			Processes:    []string{"vpn.exe"},
			Preserve:     []string{"config"},
		},
		Defaults: upkitplugin.Defaults{
			Method: "portable-inplace",
			Unpack: "zip",
			Detect: []string{"state-file", "pe-resource"},
			Install: upkitplugin.InstallDefaults{
				Path:     "${ROOT}/Custom",
				Preserve: []string{"a", "b"},
			},
			MethodOptions: upkitplugin.NewOptions("args", "--silent"),
			SourceOptions: upkitplugin.NewOptions("channel", "beta"),
		},
	}

	spec := ToAppSpec(sw, "corp-index", false)
	if spec.ID != "corp-index/corp-vpn" {
		t.Fatalf("限定 ID 错误: %s", spec.ID)
	}
	if spec.Source["kind"] != "plugin:corp-index" || spec.Source["app"] != "corp-vpn" {
		t.Fatalf("来源映射错误: %+v", spec.Source)
	}
	if spec.Source["channel"] != "beta" {
		t.Fatalf("source.<key> 未映射: %+v", spec.Source)
	}
	if spec.Method["kind"] != "portable-inplace" || spec.Method["args"] != "--silent" {
		t.Fatalf("method 映射错误: %+v", spec.Method)
	}
	if spec.Unpack["kind"] != "zip" {
		t.Fatalf("unpack 映射错误: %+v", spec.Unpack)
	}
	if len(spec.Detect) != 2 || spec.Detect[1] != "pe-resource" {
		t.Fatalf("detect 映射错误: %+v", spec.Detect)
	}
	// install.path 显式覆盖了 Target 的路径；preserve 也来自 Defaults。
	if spec.Install.Path != "${ROOT}/Custom" || len(spec.Install.Preserve) != 2 {
		t.Fatalf("install 覆盖错误: %+v", spec.Install)
	}
	if len(spec.Install.Entrypoints) != 1 || spec.Install.Processes[0] != "vpn.exe" {
		t.Fatalf("Target 未合并进 install: %+v", spec.Install)
	}
}

func TestQualifiedIDAndReleaseTranslation(t *testing.T) {
	if QualifiedID("src", "app") != "src/app" || QualifiedID("", "app") != "app" {
		t.Fatal("限定 ID 拼接错误")
	}
	rel := ToCoreRelease(upkitplugin.Release{
		Version:   "1.2.3",
		Tag:       "v1.2.3",
		Artifacts: []upkitplugin.Artifact{{Name: "a.zip", URL: "https://x", Size: 7, Digest: "sha256:z"}},
	})
	if rel.Version != "1.2.3" || len(rel.Artifacts) != 1 || rel.Artifacts[0].Size != 7 {
		t.Fatalf("版本翻译错误: %+v", rel)
	}
}

func TestManagerStatesWithoutStartingProcesses(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "disabled-src", "id: disabled-src\n")
	writeManifest(t, dir, "missing-src", "id: missing-src\nexec: nope.exe\n")
	writeManifest(t, dir, "untrusted-src", "id: untrusted-src\nexec: untrusted.bin\n")
	if err := os.WriteFile(filepath.Join(dir, "untrusted.bin"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(Config{
		Dir: dir,
		Entries: []apps.SourceSpec{
			{ID: "disabled-src", Enabled: boolPtr(false)},
			{ID: "missing-src"},
			{ID: "untrusted-src"},
			{ID: "not-installed"},
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	// 未声明的描述文件也会被发现，因此共 4 个来源。
	if got := len(m.SourceIDs()); got != 4 {
		t.Fatalf("期望 4 个来源，实际 %d: %v", got, m.SourceIDs())
	}

	m.Load(context.Background())

	got := map[string]SourceStatus{}
	for _, st := range m.Sources() {
		got[st.ID] = st
	}
	cases := map[string]State{
		"disabled-src":  StateDisabled,
		"missing-src":   StateMissing,
		"untrusted-src": StateUntrusted,
		"not-installed": StateMissing,
	}
	for id, want := range cases {
		if got[id].State != want {
			t.Errorf("来源 %s 状态应为 %s，实际 %s（%s）", id, want, got[id].State, got[id].Detail)
		}
	}
	// 未信任时应当把哈希打出来，方便用户写进清单。
	if got["untrusted-src"].SHA256 == "" {
		t.Error("未信任来源应当给出 sha256")
	}
	if got["untrusted-src"].Detail == "" {
		t.Error("未信任来源应当说明原因")
	}
}

func TestNewManagerRejectsBadEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []apps.SourceSpec
	}{
		{"缺少 id", []apps.SourceSpec{{}}},
		{"非法 id", []apps.SourceSpec{{ID: "Bad/Path"}}},
		{"不支持的 kind", []apps.SourceSpec{{ID: "ok", Kind: "builtin"}}},
		{"重复 id", []apps.SourceSpec{{ID: "dup"}, {ID: "dup"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManager(Config{Dir: t.TempDir(), Entries: tc.entries}); err == nil {
				t.Fatal("期望报错")
			}
		})
	}
}

// 端到端：编译示例插件 → 信任 → 启动 → 展开软件 → 查询版本。
func TestManagerLoadsTrustedPluginEndToEnd(t *testing.T) {
	dir := t.TempDir()
	bin := buildExample(t, dir)
	writeManifest(t, dir, "example-static", "id: example-static\nname: 示例静态源\nmode: catalog\n")

	sha, err := HashFile(bin)
	if err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(Config{
		Dir: dir,
		Entries: []apps.SourceSpec{{
			ID:    "example-static",
			Kind:  apps.KindPlugin,
			Trust: sha,
			Apps:  []apps.SourceAppSpec{{ID: "legacy-crm", Enabled: boolPtr(false)}},
		}},
		DataRoot: t.TempDir(),
		LogRoot:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	m.Load(context.Background())

	st := m.Sources()[0]
	if st.State != StateOK {
		t.Fatalf("来源应可用，实际 %s：%s", st.State, st.Detail)
	}
	if st.Apps != 3 || st.Version == "" || st.Name != "示例静态源" {
		t.Fatalf("来源信息不完整: %+v", st)
	}

	// legacy-crm 在清单里被禁用，展开时应当被排除。
	specs, err := m.AppSpecs("example-static")
	if err != nil {
		t.Fatalf("AppSpecs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("展开结果错误: %+v", specs)
	}
	byID := map[string]apps.AppSpec{}
	for _, s := range specs {
		byID[s.ID] = s
	}
	corp, ok := byID["example-static/corp-vpn"]
	if !ok {
		t.Fatalf("缺少 corp-vpn: %+v", specs)
	}
	if corp.Source["kind"] != "plugin:example-static" {
		t.Fatalf("来源标识错误: %+v", corp.Source)
	}

	// local-stub 声明了由插件自己安装，安装方式与探测器都要指向插件。
	stub, ok := byID["example-static/local-stub"]
	if !ok {
		t.Fatalf("缺少 local-stub: %+v", specs)
	}
	if stub.Method[apps.KeyKind] != KindPlugin {
		t.Fatalf("local-stub 的安装方式应为 %s: %+v", KindPlugin, stub.Method)
	}
	if len(stub.Detect) != 1 || stub.Detect[0] != KindPlugin {
		t.Fatalf("local-stub 的探测器应自动跟随: %+v", stub.Detect)
	}

	// 版本查询走完整链路：宿主 → go-plugin → 插件进程 → 返回。
	rels, err := m.Versions(context.Background(), "example-static", "legacy-crm", 1)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(rels) != 1 || rels[0].Version != "2.0.0" {
		t.Fatalf("版本结果错误: %+v", rels)
	}

	latest, err := m.Latest(context.Background(), "example-static", "corp-vpn")
	if err == nil {
		// corp-vpn 的构造器缺 download_base，应当把错误透传上来。
		t.Fatalf("corp-vpn 缺少必填配置时不应成功: %+v", latest)
	}

	// 未知来源与未知软件都要给出明确错误。
	if _, err := m.Versions(context.Background(), "nope", "x", 0); err == nil {
		t.Error("未知来源应当报错")
	}
	if _, err := m.Versions(context.Background(), "example-static", "nope", 0); err == nil {
		t.Error("未知软件应当报错")
	}
}
