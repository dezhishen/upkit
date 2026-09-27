package tui

import (
	"path/filepath"
	"testing"

	"github.com/dezhishen/upkit/internal/settings"
)

// 端到端：设置文件里写 light，启动时必须真的用浅色一套。
// 这条链路跨越 settings → main → tui，任何一环漏掉 theme 都会让选项"不生效"。
func TestThemeFromSettingsFileEndToEnd(t *testing.T) {
	t.Setenv("COLORFGBG", "") // 终端没说底色：auto 会落到 dark，好区分
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.yaml")

	set := settings.Default()
	set.Path = p
	set.UI.Theme = "light"
	if err := set.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := set.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	back, err := settings.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := back.Normalize(); err != nil {
		t.Fatal(err)
	}
	if back.UI.Theme != "light" {
		t.Fatalf("落盘再读回来变成了 %q", back.UI.Theme)
	}

	// 模拟 main：把设置文件里的值交给界面。
	m := New(Options{Ctrl: nil, Version: "test", Theme: back.UI.Theme})
	if got := m.theme.Variant(); got != "light" {
		t.Fatalf("设置文件写 light，界面实际用 %q", got)
	}
}
