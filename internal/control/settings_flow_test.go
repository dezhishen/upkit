package control

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/settings"
)

// 增减（←/→，鼠标左右键）只对数字、开关、枚举有意义，而且边界只在这里判一次。
func TestAdjustSettingKinds(t *testing.T) {
	ctrl := newSettingsTestController(t)

	// 数字项按步长走：超范围夹到区间，不会一路加到荒唐的值。
	if err := ctrl.AdjustSetting("network.timeout_seconds", 1); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "70" {
		t.Fatalf("步长 10 生效后应为 70，实际 %q", got)
	}
	if err := ctrl.AdjustSetting("network.timeout_seconds", -1); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "60" {
		t.Fatalf("回退应为 60，实际 %q", got)
	}
	if err := ctrl.AdjustSetting("network.timeout_seconds", 1000); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "600" {
		t.Fatalf("上限应夹到 600，实际 %q", got)
	}
	if err := ctrl.AdjustSetting("network.timeout_seconds", -1000); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "10" {
		t.Fatalf("下限应夹到 10，实际 %q", got)
	}

	// 开关：取反。
	before := settingValueOf(t, ctrl, "behavior.confirm_before_apply")
	if err := ctrl.AdjustSetting("behavior.confirm_before_apply", 1); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "behavior.confirm_before_apply"); got == before {
		t.Fatalf("开关应取反: %q", got)
	}

	// 枚举：循环并在两端回绕。
	cases := []struct {
		delta int
		want  string
	}{
		{1, "warn"}, {1, "error"}, {1, "debug"}, {-1, "error"},
	}
	for _, tc := range cases {
		if err := ctrl.AdjustSetting("logs.level", tc.delta); err != nil {
			t.Fatalf("AdjustSetting: %v", err)
		}
		if got := settingValueOf(t, ctrl, "logs.level"); got != tc.want {
			t.Fatalf("delta=%d 时枚举应为 %q，实际 %q", tc.delta, tc.want, got)
		}
	}

	// 文本项不能靠增减改，未知 key 直接报错。
	if err := ctrl.AdjustSetting("network.proxy", 1); err == nil ||
		!strings.Contains(err.Error(), "整段输入") {
		t.Fatalf("文本项应提示整段输入: %v", err)
	}
	if err := ctrl.AdjustSetting("nope.nothing", 1); err == nil {
		t.Fatal("未知设置项应报错")
	}
	if !ctrl.SettingsDirty() {
		t.Fatal("改过设置就该标记为未保存")
	}
}

// enumCycle 单独钉一遍「值不在选项里」的兜底：手改过设置文件的人会撞上。
func TestCycleSettingFallback(t *testing.T) {
	opts := []string{"a", "b", "c"}
	if got := cycleSetting("b", opts, 1); got != "c" {
		t.Fatalf("应走到下一项: %q", got)
	}
	if got := cycleSetting("c", opts, 1); got != "a" {
		t.Fatalf("末尾应回绕到第一项: %q", got)
	}
	if got := cycleSetting("A", opts, 1); got != "b" {
		t.Fatalf("匹配应忽略大小写: %q", got)
	}
	// 不在选项里（例如手改成了别的值）：从第一项开始，而不是原地不动。
	if got := cycleSetting("zzz", opts, 1); got != "b" {
		t.Fatalf("认不出的值应从第一项起算: %q", got)
	}
	if got := cycleSetting("zzz", nil, 1); got != "zzz" {
		t.Fatalf("没有选项时原样返回: %q", got)
	}
}

// 整段写入：数字项也接受（从 60 调到 480 不该按几十次右箭头），列表项按分隔符切。
func TestSetSettingKinds(t *testing.T) {
	ctrl := newSettingsTestController(t)
	set := ctrl.Settings()

	if err := ctrl.SetSetting("network.timeout_seconds", "480"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if set.Network.TimeoutSeconds != 480 {
		t.Fatalf("整段写入没生效: %d", set.Network.TimeoutSeconds)
	}
	// 超范围夹取与增减走同一套边界。
	if err := ctrl.SetSetting("network.timeout_seconds", "99999"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if set.Network.TimeoutSeconds != 600 {
		t.Fatalf("超范围应夹到 600: %d", set.Network.TimeoutSeconds)
	}
	if err := ctrl.SetSetting("storage.backup_keep", " 7 "); err != nil {
		t.Fatalf("两侧空白应被容忍: %v", err)
	}
	if set.Storage.BackupKeep != 7 {
		t.Fatalf("带空白的整数没解析对: %d", set.Storage.BackupKeep)
	}
	if err := ctrl.SetSetting("network.timeout_seconds", "abc"); err == nil ||
		!strings.Contains(err.Error(), "整数") {
		t.Fatalf("非整数应报错: %v", err)
	}

	// 布尔与枚举只能增减（或由界面弹窗选择），不接受整段写入。
	if err := ctrl.SetSetting("logs.audit", "true"); err == nil {
		t.Fatal("布尔项不该接受整段写入")
	}
	if err := ctrl.SetSetting("logs.level", "debug"); err == nil {
		t.Fatal("枚举项不该接受整段写入")
	}

	// 列表项：逗号、分号、换行都算分隔符，空白与空项丢掉。
	if err := ctrl.SetSetting("plugins.allowlist", "a.com, b.com;c.com\nd.com , "); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if want := []string{"a.com", "b.com", "c.com", "d.com"}; !reflect.DeepEqual(set.Plugins.Allowlist, want) {
		t.Fatalf("白名单切分不对: %#v", set.Plugins.Allowlist)
	}
	if got := settingValueOf(t, ctrl, "plugins.allowlist"); got != "a.com, b.com, c.com, d.com" {
		t.Fatalf("表单展示应重新用逗号连接: %q", got)
	}
	// 清空表示不限制。
	if err := ctrl.SetSetting("plugins.allowlist", "   "); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if set.Plugins.Allowlist != nil {
		t.Fatalf("空输入应得到 nil（表示不限制）: %#v", set.Plugins.Allowlist)
	}

	if err := ctrl.SetSetting("nope.nothing", "x"); err == nil {
		t.Fatal("未知设置项应报错")
	}
}

// SettingValue：给编辑弹窗预填用。文本与数字可以取回真实值，其它类型明确拒绝。
func TestSettingValueKinds(t *testing.T) {
	ctrl := newSettingsTestController(t)
	set := ctrl.Settings()

	set.Network.Proxy = "http://127.0.0.1:7890"
	if got, err := ctrl.SettingValue("network.proxy"); err != nil || got != "http://127.0.0.1:7890" {
		t.Fatalf("文本项应取回真实值: %q %v", got, err)
	}
	set.Network.Retries = 5
	if got, err := ctrl.SettingValue("network.retries"); err != nil || got != "5" {
		t.Fatalf("数字项应取回十进制串: %q %v", got, err)
	}
	for _, key := range []string{"logs.audit", "logs.level"} {
		if _, err := ctrl.SettingValue(key); err == nil {
			t.Fatalf("%s 没有可整段编辑的值，应报错", key)
		}
	}
	if _, err := ctrl.SettingValue("nope.nothing"); err == nil {
		t.Fatal("未知设置项应报错")
	}
}

// 凭据项在界面上只表明「已设置」，不把令牌铺在屏幕上。
func TestSecretSettingMasked(t *testing.T) {
	ctrl := newSettingsTestController(t)
	set := ctrl.Settings()

	if got := settingValueOf(t, ctrl, "network.github_token"); got != "—" {
		t.Fatalf("没设令牌时应显示占位符，实际 %q", got)
	}
	set.Network.GitHubToken = "ghp_secret"
	if got := settingValueOf(t, ctrl, "network.github_token"); got != "********" {
		t.Fatalf("凭据项应打码，实际 %q", got)
	}
	var secret bool
	for _, item := range ctrl.SettingsForm() {
		if item.Key == "network.github_token" {
			secret = item.Secret
		}
	}
	if !secret {
		t.Fatal("凭据项要标记 Secret，界面才知道按密码模式回显")
	}
	// 引用形式（env:/cmd:）同样是秘密：值本身会出现在日志里，但没必要展示。
	set.Network.GitHubToken = "env:GITHUB_TOKEN"
	if got := settingValueOf(t, ctrl, "network.github_token"); got != "********" {
		t.Fatalf("引用形式也应打码，实际 %q", got)
	}
}

// 设置还没加载时的护栏：表单与路径类接口要返回空，落盘类要报错，而不是 panic。
func TestSettingsNilGuards(t *testing.T) {
	ctrl := newSettingsTestController(t)
	ctrl.set = nil

	if form := ctrl.SettingsForm(); form != nil {
		t.Fatalf("没有设置时表单应为空: %+v", form)
	}
	if got := ctrl.SettingsPath(); got != "" {
		t.Fatalf("没有设置时路径应为空: %q", got)
	}
	if got := ctrl.ManifestPath(); got != "" {
		t.Fatalf("没有设置时清单路径应为空: %q", got)
	}
	if paths := ctrl.SettingsPaths(); paths != nil {
		t.Fatalf("没有设置时只读路径列表应为空: %+v", paths)
	}
	if _, err := ctrl.SettingValue("network.proxy"); err == nil {
		t.Fatal("没有设置时取值应报错")
	}
	if err := ctrl.AdjustSetting("network.proxy", 1); err == nil {
		t.Fatal("没有设置时调整应报错")
	}
	if err := ctrl.SetSetting("network.proxy", "x"); err == nil {
		t.Fatal("没有设置时写入应报错")
	}
	if err := ctrl.SaveSettings(); err == nil {
		t.Fatal("没有设置时保存应报错")
	}
	if err := ctrl.ResetSettings(); err == nil {
		t.Fatal("没有设置时恢复默认应报错")
	}
	if ctrl.SettingsDirty() {
		t.Fatal("没改过就不该是脏的")
	}
	// 清单没加载时推安装根目录是空操作，不能崩。
	ctrl.afs = nil
	ctrl.applyInstallRoot()
}

// 落盘失败要报错：设置文件写不进去（例如路径被占）时必须让用户知道。
func TestSaveSettingsFailure(t *testing.T) {
	ctrl := newSettingsTestController(t)
	_ = os.Remove(ctrl.Settings().Path)
	if err := os.MkdirAll(ctrl.Settings().Path, 0o755); err != nil {
		t.Fatalf("把设置路径变成目录: %v", err)
	}
	if err := ctrl.SaveSettings(); err == nil {
		t.Fatal("设置写不进去时应报错")
	}
}

// 保存与恢复默认之后，安装根目录要推给清单层：界面里改了位置、下一次检查还用旧值
// 的话，插件声明的 ${ROOT} 会指到旧地方。
func TestSaveAndResetPropagateInstallRoot(t *testing.T) {
	ctrl := newSettingsTestController(t)
	custom := filepath.Join(t.TempDir(), "我的软件")

	if err := ctrl.SetSetting("storage.install_root", custom); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := ctrl.SaveSettings(); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := ctrl.afs.InstallRoot; got != custom {
		t.Fatalf("安装根目录没推给清单层: %q，期望 %q", got, custom)
	}
	if ctrl.SettingsDirty() {
		t.Fatal("保存之后不该还是脏的")
	}

	if err := ctrl.ResetSettings(); err != nil {
		t.Fatalf("ResetSettings: %v", err)
	}
	if !ctrl.SettingsDirty() {
		t.Fatal("恢复默认之后要标记为待保存")
	}
	want := filepath.Join(ctrl.Settings().RootDir(), settings.DirApps)
	if got := ctrl.afs.InstallRoot; got != want {
		t.Fatalf("恢复默认后安装根目录应回到 %q，实际 %q", want, got)
	}
}
