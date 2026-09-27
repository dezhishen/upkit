package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/util"
)

// newSettingsTestController 装配一个只关心设置的控制层。
//
// 设置文件放在 <临时目录>/config/ 下：根目录由此推导，目录项的默认值才对得上。
func newSettingsTestController(t *testing.T) *Controller {
	t.Helper()
	dir := t.TempDir()
	set, err := settings.Load(filepath.Join(dir, settings.DirConfig, settings.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	afs := apps.Default()
	afs.Path = filepath.Join(dir, settings.DirConfig, settings.AppsFileName)
	ctrl, err := New(Options{Settings: set, Apps: afs, EventBuffer: 8})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ctrl
}

// settingValueOf 取表单里某一项的展示文本。
func settingValueOf(t *testing.T, ctrl *Controller, key string) string {
	t.Helper()
	for _, f := range ctrl.SettingsForm() {
		if f.Key == key {
			return f.Text
		}
	}
	t.Fatalf("设置表单里没有 %s", key)
	return ""
}

// 目录项能整段改写，落盘后还能被读回来（设置文件里的目录字段是真实配置项）。
func TestSetDirSettingPersists(t *testing.T) {
	ctrl := newSettingsTestController(t)
	custom := filepath.Join(t.TempDir(), "upkit-data")

	if err := ctrl.SetSetting("storage.data_dir", custom); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if !ctrl.SettingsDirty() {
		t.Fatalf("改设置后应标记为未保存")
	}
	if got := settingValueOf(t, ctrl, "storage.data_dir"); got != custom {
		t.Fatalf("表单未反映新目录: %q", got)
	}

	if err := ctrl.SaveSettings(); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if ctrl.SettingsDirty() {
		t.Fatalf("保存后不应仍标记为未保存")
	}

	reloaded, err := settings.Load(ctrl.SettingsPath())
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if reloaded.Storage.DataDir != custom {
		t.Fatalf("自定义目录未落盘: %q != %q", reloaded.Storage.DataDir, custom)
	}
}

// 目录项留空 = 跟随根目录。
//
// 两件事都要成立：界面上显示的是推导出来的实际路径（不是「空」），而写进文件的仍是
// 空字符串 —— 否则整目录搬走之后，配置文件里那条死路径会继续指向旧位置。
func TestSetDirSettingEmptyFollowsRoot(t *testing.T) {
	ctrl := newSettingsTestController(t)
	root := filepath.Dir(filepath.Dir(ctrl.SettingsPath()))
	want := filepath.Join(root, settings.DirData)

	if got := settingValueOf(t, ctrl, "storage.data_dir"); got != want {
		t.Fatalf("默认应跟随根目录 %q，实际 %q", want, got)
	}

	// 先改成别处，再留空 —— 两条路径都得走一遍。
	if err := ctrl.SetSetting("storage.data_dir", filepath.Join(t.TempDir(), "elsewhere")); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := ctrl.SetSetting("storage.data_dir", ""); err != nil {
		t.Fatalf("SetSetting(空): %v", err)
	}
	if got := settingValueOf(t, ctrl, "storage.data_dir"); got != want {
		t.Fatalf("留空后应回到 %q，实际 %q", want, got)
	}

	if err := ctrl.SaveSettings(); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	raw, err := os.ReadFile(ctrl.SettingsPath())
	if err != nil {
		t.Fatalf("读设置文件: %v", err)
	}
	if !strings.Contains(string(raw), `data_dir: ""`) {
		t.Fatalf("跟随根目录时字段应为空，实际内容:\n%s", raw)
	}

	reloaded, err := settings.Load(ctrl.SettingsPath())
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	if reloaded.Storage.DataDir != want {
		t.Fatalf("重新加载后应仍是 %q，实际 %q", want, reloaded.Storage.DataDir)
	}
}

// 非法值必须在保存时报出来。
//
// 写进文件之后，下一次启动会在 Load 里直接失败，而那时界面已经关了：用户只看到程序
// 起不来，也没人告诉他哪一项写错了。
func TestSaveSettingsRejectsInvalidValue(t *testing.T) {
	ctrl := newSettingsTestController(t)

	if err := ctrl.SetSetting("network.proxy", "127.0.0.1:7890"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := ctrl.SaveSettings(); err == nil {
		t.Fatalf("缺少协议前缀的代理应被拒绝")
	}
	if !ctrl.SettingsDirty() {
		t.Fatalf("保存失败后应仍然标记为未保存")
	}
	if util.FileExists(ctrl.SettingsPath()) {
		t.Fatalf("校验失败时不应落盘")
	}

	// 改回合法值后应当能保存，且不再被卡住。
	if err := ctrl.SetSetting("network.proxy", "http://127.0.0.1:7890"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := ctrl.SaveSettings(); err != nil {
		t.Fatalf("合法值应能保存: %v", err)
	}
}

// 界面只会把按键转成意图，所以「哪一项能用哪种编辑方式」得在控制层判掉。
func TestSettingIntentValidation(t *testing.T) {
	ctrl := newSettingsTestController(t)

	if err := ctrl.AdjustSetting("storage.data_dir", 1); err == nil {
		t.Fatalf("目录项不能用增减调整")
	}
	if err := ctrl.SetSetting("ui.theme", "dark"); err == nil {
		t.Fatalf("枚举项不能用整段写入")
	}
	if err := ctrl.AdjustSetting("没有这一项", 1); err == nil {
		t.Fatalf("未知设置项应报错")
	}
	if _, err := ctrl.SettingValue("ui.theme"); err == nil {
		t.Fatalf("枚举项没有可整段编辑的值")
	}

	// 数字项可以整段输入，超范围夹到区间内，非数字报错。
	if err := ctrl.SetSetting("ui.refresh_ms", "300"); err != nil {
		t.Fatalf("数字项应能整段写入: %v", err)
	}
	if got := settingValueOf(t, ctrl, "ui.refresh_ms"); got != "300" {
		t.Fatalf("应为 300，实际 %q", got)
	}
	if err := ctrl.SetSetting("ui.refresh_ms", "99999"); err != nil {
		t.Fatalf("超范围应夹到区间而不是报错: %v", err)
	}
	if got := settingValueOf(t, ctrl, "ui.refresh_ms"); got != "5000" {
		t.Fatalf("应夹到上界 5000，实际 %q", got)
	}
	if err := ctrl.SetSetting("ui.refresh_ms", "abc"); err == nil {
		t.Fatalf("非数字应报错")
	}
	if got, err := ctrl.SettingValue("ui.refresh_ms"); err != nil || got != "5000" {
		t.Fatalf("数字项应能取回当前值，实际 %q err=%v", got, err)
	}

	// 数字项按步长走，并夹在区间内。
	if err := ctrl.AdjustSetting("network.timeout_seconds", -1); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "50" {
		t.Fatalf("超时步长应为 10，实际 %q", got)
	}
	if err := ctrl.AdjustSetting("network.timeout_seconds", -100); err != nil {
		t.Fatalf("AdjustSetting: %v", err)
	}
	if got := settingValueOf(t, ctrl, "network.timeout_seconds"); got != "10" {
		t.Fatalf("应夹到下界 10，实际 %q", got)
	}
}

// 恢复默认后，目录项回到跟随根目录。
func TestResetSettingsRestoresDirDefaults(t *testing.T) {
	ctrl := newSettingsTestController(t)
	root := filepath.Dir(filepath.Dir(ctrl.SettingsPath()))
	want := filepath.Join(root, settings.DirBackup)

	if err := ctrl.SetSetting("storage.backup_dir", filepath.Join(t.TempDir(), "bak")); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := ctrl.ResetSettings(); err != nil {
		t.Fatalf("ResetSettings: %v", err)
	}
	if !ctrl.SettingsDirty() {
		t.Fatalf("恢复默认后应标记为待保存")
	}
	if got := settingValueOf(t, ctrl, "storage.backup_dir"); got != want {
		t.Fatalf("恢复默认后应为 %q，实际 %q", want, got)
	}
}
