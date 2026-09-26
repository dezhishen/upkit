package pluginhost

import (
	"context"
	"testing"
	"time"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
)

// KindPlugin 必须与 SDK 暴露的 MethodPlugin 是同一个值：插件在 Defaults.Method
// 里写下的就是这个字符串，两处一旦漂移，插件会莫名其妙地"没有可用安装器"。
func TestKindPluginMatchesSDKConstant(t *testing.T) {
	if KindPlugin != upkitplugin.MethodPlugin {
		t.Fatalf("KindPlugin = %q，SDK 的 MethodPlugin = %q", KindPlugin, upkitplugin.MethodPlugin)
	}
}

// 插件给出的计划要能翻译成宿主计划，缺失的字段回落到宿主算出来的值。
func TestToCorePlanFallsBackToHostValues(t *testing.T) {
	req := core.Request{
		App: core.AppRef{ID: "corp/stub", Name: "存根", InstallPath: "/opt/stub"},
		Plan: core.Plan{
			Action: core.ActionUpdate,
			From:   "1.0.0",
			To:     "2.0.0",
		},
	}
	got := toCorePlan(req, upkitplugin.PlanResult{
		Steps: []upkitplugin.Step{{Kind: upkitplugin.StepCopy, Desc: "写入"}},
		Note:  "由插件完成",
	})
	if got.Action != core.ActionUpdate || got.From != "1.0.0" || got.To != "2.0.0" {
		t.Fatalf("未回落到宿主值: %+v", got)
	}
	if got.App.ID != "corp/stub" || got.Note != "由插件完成" {
		t.Fatalf("应用信息丢失: %+v", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].Kind != core.StepCopy || !got.Steps[0].Critical {
		t.Fatalf("步骤翻译错误: %+v", got.Steps)
	}
}

// 插件显式给出的动作与版本必须优先于宿主推断。
func TestToCorePlanPrefersPluginValues(t *testing.T) {
	req := core.Request{Plan: core.Plan{Action: core.ActionInstall, To: "1.0.0"}}
	got := toCorePlan(req, upkitplugin.PlanResult{
		Action:  upkitplugin.ActionReinstall,
		To:      "1.0.1",
		Release: upkitplugin.Release{Version: "1.0.1"},
	})
	if got.Action != core.ActionReinstall || got.To != "1.0.1" {
		t.Fatalf("没有采用插件给出的值: %+v", got)
	}
	if got.Release.Version != "1.0.1" {
		t.Fatalf("版本没有带到 Release 上: %+v", got.Release)
	}
}

// 插件没给 To 时用 Release 里的版本号兜底。
func TestToCorePlanUsesReleaseVersionAsFallback(t *testing.T) {
	got := toCorePlan(core.Request{}, upkitplugin.PlanResult{
		Release: upkitplugin.Release{Version: "3.1.4"},
	})
	if got.To != "3.1.4" || got.Release.Version != "3.1.4" {
		t.Fatalf("Release 版本兜底失败: %+v", got)
	}
}

// 执行结果的翻译：插件没填的用宿主已知值兜底。
func TestToCoreResultFallsBack(t *testing.T) {
	req := core.Request{
		App:  core.AppRef{InstallPath: "/opt/stub"},
		Plan: core.Plan{Action: core.ActionUpdate, From: "1.0.0", To: "2.0.0"},
	}
	got := toCoreResult(req, upkitplugin.Result{ElapsedMS: 1500})
	if got.Action != core.ActionUpdate || got.From != "1.0.0" || got.To != "2.0.0" {
		t.Fatalf("未回落到宿主值: %+v", got)
	}
	if got.InstallPath != "/opt/stub" {
		t.Fatalf("安装路径未兜底: %q", got.InstallPath)
	}
	if got.Elapsed != 1500*time.Millisecond {
		t.Fatalf("耗时换算错误: %v", got.Elapsed)
	}
	// 自包含方式下宿主不下载，这些字段必须保持为空。
	if got.Downloaded != 0 || got.SHA256 != "" || got.ReusedCache {
		t.Fatalf("自包含结果不应带上宿主下载信息: %+v", got)
	}
}

// 事件翻译：缺 kind / level 时补默认值，别让界面拿到空类型。
func TestToCoreEventDefaults(t *testing.T) {
	got := toCoreEvent("app-1", "run-1", upkitplugin.Event{Msg: "hi"})
	if got.Kind != core.EventLog || got.Level != core.LevelInfo {
		t.Fatalf("默认值缺失: %+v", got)
	}
	if got.AppID != "app-1" || got.RunID != "run-1" || got.Msg != "hi" {
		t.Fatalf("字段丢失: %+v", got)
	}

	got = toCoreEvent("app-1", "run-1", upkitplugin.Event{
		Kind: upkitplugin.EventProgress, Level: upkitplugin.LogLevelWarn,
		Phase: "下载", Done: 5, Total: 10,
	})
	if got.Kind != core.EventProgress || got.Level != core.LevelWarn || got.Done != 5 || got.Total != 10 {
		t.Fatalf("字段翻译错误: %+v", got)
	}
}

// 请求翻译必须带上插件落地所需的本机上下文。
func TestToPluginPlanRequestCarriesContext(t *testing.T) {
	req := core.Request{
		App:      core.AppRef{InstallPath: "/opt/stub"},
		Plan:     core.Plan{From: "1.0.0", To: "2.0.0", Release: core.Release{Version: "2.0.0"}},
		WorkDir:  "/tmp/work",
		CacheDir: "/tmp/cache",
	}
	got := toPluginPlanRequest(req)
	if got.InstallPath != "/opt/stub" || got.WorkDir != "/tmp/work" || got.CacheDir != "/tmp/cache" {
		t.Fatalf("本机上下文丢失: %+v", got)
	}
	if got.From != "1.0.0" || got.To != "2.0.0" || got.Release.Version != "2.0.0" {
		t.Fatalf("版本信息丢失: %+v", got)
	}
}

// takeover 的两条途径：插件级能力声明与单个软件声明，都算接管；都不是则不接管。
func TestTakeoverDetection(t *testing.T) {
	full := &item{
		info: upkitplugin.Info{Capabilities: []string{upkitplugin.CapabilityFull}},
		apps: []upkitplugin.Software{{ID: "a"}},
	}
	if _, _, ok := (&Manager{byID: map[string]*item{}}).takeover(core.AppRef{}); ok {
		t.Fatal("未知来源不应判定为接管")
	}
	m := &Manager{byID: map[string]*item{"src": full}}
	ref := core.AppRef{Source: "plugin:src", SourceOpts: map[string]string{"app": "a"}}
	if _, appID, ok := m.takeover(ref); !ok || appID != "a" {
		t.Fatalf("插件级声明未被识别: ok=%v appID=%q", ok, appID)
	}

	perSoftware := &item{
		apps: []upkitplugin.Software{
			{ID: "a"},
			{ID: "b", Defaults: upkitplugin.Defaults{Method: upkitplugin.MethodPlugin}},
		},
	}
	m = &Manager{byID: map[string]*item{"src": perSoftware}}
	if _, _, ok := m.takeover(ref); ok {
		t.Fatal("软件 a 没有声明接管，不应判定为接管")
	}
	refB := core.AppRef{Source: "plugin:src", SourceOpts: map[string]string{"app": "b"}}
	if _, appID, ok := m.takeover(refB); !ok || appID != "b" {
		t.Fatalf("软件级声明未被识别: ok=%v appID=%q", ok, appID)
	}
	// 非插件来源：ParsePluginRef 报错，直接判定为不接管。
	if _, _, ok := m.takeover(core.AppRef{Source: "github-release"}); ok {
		t.Fatal("非插件来源不应判定为接管")
	}
}

// 插件的安装方式必须是「自包含」：宿主不该替它下载或解包。
func TestAppMethodCapsIsSelfContained(t *testing.T) {
	a := &appMethod{
		mgr:   &Manager{cfg: Config{}},
		it:    &item{entry: apps.SourceSpec{ID: "src"}},
		appID: "a",
	}
	caps := a.Caps()
	if !caps.SelfContained {
		t.Fatal("插件安装方式必须是自包含的")
	}
	if !caps.Rollbackable || !caps.Silent {
		t.Fatalf("能力声明不完整: %+v", caps)
	}
	if caps.NeedsUnpack {
		t.Fatal("自包含方式不应要求宿主解包")
	}
	if name := a.Name(); name != KindPlugin+":src" {
		t.Fatalf("适配器名错误: %q", name)
	}
}

// 非插件来源直接判定为不接管（而不是报错）：探测器轴会被其它来源的清单复用，
// 在这里抛错会让无关软件也装不上。
func TestStatusOnNonPluginSource(t *testing.T) {
	if _, ok, err := (&Manager{}).Status(context.Background(), core.AppRef{}); ok || err != nil {
		t.Fatalf("非插件来源应当安静地返回未接管: ok=%v err=%v", ok, err)
	}
}
