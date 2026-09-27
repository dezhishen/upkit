package all

import (
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/detect"
	plugindetect "github.com/dezhishen/upkit/internal/detect/plugin"
	"github.com/dezhishen/upkit/internal/method"
	pluginmethod "github.com/dezhishen/upkit/internal/method/plugin"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/source/githubrelease"
	"github.com/dezhishen/upkit/internal/unpacking"
)

// 注册表必须认得文档里承诺的每一种 kind。
//
// 这是「新增适配器 = 在这个文件里多写一行」的兜底：漏掉一行，用户看到的是一句
// 「未知来源类型 "github-release"」，而那句话不会告诉你少的是哪一行。
func TestRegistryResolvesEveryDocumentedKind(t *testing.T) {
	reg := Registry()

	// 来源：内置的那一个要能真的构造出来。
	gh := core.AppRef{Source: githubrelease.Name, SourceOpts: map[string]string{"repo": "owner/repo"}}
	if _, err := reg.Source(gh, registry.Deps{}); err != nil {
		t.Fatalf("内置来源 %q 未注册: %v", githubrelease.Name, err)
	}
	// 插件来源走前缀匹配：宿主没接上时报「插件宿主不可用」是对的，但不该报「未知来源
	// 类型」—— 后者意味着连前缀都没注册。
	if _, err := reg.Source(core.AppRef{Source: apps.SourceKindPluginPrefix + "corp-index"}, registry.Deps{}); err != nil {
		if strings.Contains(err.Error(), "未知来源类型") {
			t.Fatalf("插件来源前缀未注册: %v", err)
		}
	}

	// 解包。
	for _, kind := range []string{unpacking.KindZip, unpacking.KindTarGz, unpacking.KindRaw} {
		if _, err := reg.Unpacker(core.AppRef{Unpack: kind}, registry.Deps{}); err != nil {
			t.Fatalf("解包器 %q 未注册: %v", kind, err)
		}
	}

	// 安装方式。
	for _, kind := range []string{method.KindPortableInPlace, method.KindExeInstaller, method.KindMSIExec} {
		if _, err := reg.Method(core.AppRef{Method: kind}, registry.Deps{}); err != nil {
			t.Fatalf("安装方式 %q 未注册: %v", kind, err)
		}
	}
	// 插件接管安装同理：宿主没接上时报的是「插件宿主不可用」。
	if _, err := reg.Method(core.AppRef{Method: pluginmethod.Kind}, registry.Deps{}); err != nil {
		if strings.Contains(err.Error(), "未知安装方式") {
			t.Fatalf("插件安装方式未注册: %v", err)
		}
	}

	// 探测器：内置的几个要能按声明顺序构造出来。
	kinds := []string{detect.KindStateFile, detect.KindPEResource, detect.KindDirName, detect.KindCLIVersion}
	ds, err := reg.Detectors(core.AppRef{ID: "demo", Detect: kinds}, registry.Deps{})
	if err != nil {
		t.Fatalf("探测器未注册: %v", err)
	}
	if len(ds) != len(kinds) {
		t.Fatalf("探测链长度不对：%d != %d", len(ds), len(kinds))
	}
	for i, d := range ds {
		if d.Name() != kinds[i] {
			t.Fatalf("探测链顺序应与声明一致：第 %d 个是 %q，期望 %q", i, d.Name(), kinds[i])
		}
	}

	// 未知 kind 仍要报错（别把「没注册」变成「静默跳过」）。
	if _, err := reg.Source(core.AppRef{Source: "no-such-source"}, registry.Deps{}); err == nil {
		t.Fatalf("未知来源应报错")
	}
	if _, err := reg.Detectors(core.AppRef{ID: "demo", Detect: []string{"no-such-detect"}}, registry.Deps{}); err == nil {
		t.Fatalf("未知探测器应报错")
	}
	// 没声明探测器也是错误（而不是「什么都不探测」）。
	if _, err := reg.Detectors(core.AppRef{ID: "demo"}, registry.Deps{}); err == nil {
		t.Fatalf("未声明 detect 应报错")
	}
}

// Kind 列表要如实反映已注册的东西（界面与错误信息都读它）。
func TestRegistryKindListing(t *testing.T) {
	reg := Registry()

	srcs := strings.Join(reg.SourceKinds(), ",")
	if !strings.Contains(srcs, githubrelease.Name) || !strings.Contains(srcs, apps.SourceKindPluginPrefix) {
		t.Fatalf("来源列表不完整: %v", srcs)
	}
	if got := strings.Join(reg.MethodKinds(), ","); !strings.Contains(got, method.KindMSIExec) {
		t.Fatalf("安装方式列表不完整: %v", got)
	}
	if got := strings.Join(reg.UnpackerKinds(), ","); !strings.Contains(got, unpacking.KindRaw) {
		t.Fatalf("解包列表不完整: %v", got)
	}
	if got := strings.Join(reg.DetectorKinds(), ","); !strings.Contains(got, detect.KindDirName) ||
		!strings.Contains(got, plugindetect.Kind) {
		t.Fatalf("探测列表不完整: %v", got)
	}
}
