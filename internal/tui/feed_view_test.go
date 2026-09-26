package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/pluginfeed"
)

// errFeedStub 用于模拟拉取/安装失败。
var errFeedStub = errors.New("boom")

// feedTestModel 返回一个已进入订阅详情、并载入固定条目的模型。
func feedTestModel(t *testing.T, entries ...pluginfeed.Entry) Model {
	t.Helper()
	m := newTestModel(t)
	store, err := pluginfeed.LoadStore(filepath.Join(t.TempDir(), pluginfeed.FileName))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	// 能列出订阅，说明订阅功能此前已被用户授权启用。
	if err := store.AuthorizeFeature(); err != nil {
		t.Fatalf("AuthorizeFeature: %v", err)
	}
	m.feed = store
	m.feedFor = "https://example.com/plugins.yaml"
	m.feedLoaded = true
	m.feedEntries = entries
	m = update(t, m, key('6')) // 切到「来源」页，按键才会分发到订阅详情
	return m
}

func sameOriginPkg(id, name, version, sha string) pluginfeed.Entry {
	return pluginfeed.Entry{
		Plugin:   pluginfeed.Plugin{ID: id, Name: name, Version: version},
		Package:  pluginfeed.Package{SHA256: sha},
		Location: pluginfeed.Location{URL: "https://example.com/" + id + ".zip", Host: "example.com", SameOrigin: true},
	}
}

// 列表要区分「可安装 / 可更新 / 已是最新 / 降级」。
func TestFeedDetailListsActions(t *testing.T) {
	fresh := sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64))
	old := sameOriginPkg("beta", "Beta", "1.0.0", strings.Repeat("b", 64))
	old.Installed = "0.9.0"
	same := sameOriginPkg("gamma", "Gamma", "2.0.0", strings.Repeat("c", 64))
	same.Installed = "2.0.0"
	same.InstalledSHA256 = pluginfeed.NormalizeSHA256(strings.Repeat("c", 64))
	down := sameOriginPkg("delta", "Delta", "1.0.0", strings.Repeat("d", 64))
	down.Installed = "3.0.0"

	m := feedTestModel(t, fresh, old, same, down)
	out := m.viewFeedDetail(100, 30)

	for _, want := range []string{"Alpha", "可安装", "Beta", "可更新", "Gamma", "已是最新", "Delta", "降级"} {
		if !strings.Contains(out, want) {
			t.Fatalf("订阅详情缺少 %q:\n%s", want, out)
		}
	}
}

// 拉取失败、拉取中、空订阅都应有明确文案，而不是空白或 panic。
func TestFeedDetailStates(t *testing.T) {
	m := feedTestModel(t)
	m.feedLoaded = false
	if out := m.viewFeedDetail(80, 20); !strings.Contains(out, "正在拉取") {
		t.Fatalf("加载中提示缺失:\n%s", out)
	}

	m.feedLoaded = true
	m.feedErr = errFeedStub
	if out := m.viewFeedDetail(80, 20); !strings.Contains(out, "拉取失败") {
		t.Fatalf("失败提示缺失:\n%s", out)
	}

	m.feedErr = nil
	if out := m.viewFeedDetail(80, 20); !strings.Contains(out, "没有适配当前平台") {
		t.Fatalf("空订阅提示缺失:\n%s", out)
	}
}

// esc 从订阅详情返回来源列表。
func TestFeedDetailEscReturns(t *testing.T) {
	m := feedTestModel(t, sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64)))
	m = update(t, m, key(tea.KeyEsc))
	if m.feedFor != "" {
		t.Fatalf("esc 应返回来源列表，实际 feedFor=%q", m.feedFor)
	}
}

// 同源条目直接开始安装：进入忙碌态并返回可执行的下载命令。
func TestFeedDetailInstallsSameOrigin(t *testing.T) {
	e := sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64))
	m := feedTestModel(t, e)

	next, cmd := m.startInstall(e)
	got := next.(Model)
	if !got.feedBusy {
		t.Fatalf("应进入安装忙碌态")
	}
	if cmd == nil {
		t.Fatalf("应返回安装命令")
	}
	if got.confirm != nil {
		t.Fatalf("同源下载不应弹授权窗")
	}
}

// 跨域下载必须先经用户确认，确认后域名授权落盘。
func TestFeedDetailCrossOriginNeedsConfirm(t *testing.T) {
	e := sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64))
	e.Location = pluginfeed.Location{URL: "https://cdn.example.com/alpha.zip", Host: "cdn.example.com"}
	m := feedTestModel(t, e)

	next, cmd := m.startInstall(e)
	got := next.(Model)
	if got.confirm == nil {
		t.Fatalf("跨域下载应弹授权确认")
	}
	if got.feedBusy {
		t.Fatalf("未授权前不应开始下载")
	}
	if cmd != nil {
		t.Fatalf("未授权前不应有下载命令")
	}

	// 用户点「同意」：授权落盘并开始安装。
	if got.confirm.OnYes == nil {
		t.Fatalf("确认框缺少 OnYes")
	}
	if c := got.confirm.OnYes(&got); c == nil {
		t.Fatalf("同意后应返回安装命令")
	}
	if !got.feed.HostAuthorized("cdn.example.com") {
		t.Fatalf("同意后域名应被授权")
	}
	if !got.feedBusy {
		t.Fatalf("同意后应进入忙碌态")
	}
}

// 已是最新不重复安装；降级默认拒绝。
func TestFeedDetailSkipsCurrentAndDowngrade(t *testing.T) {
	same := sameOriginPkg("gamma", "Gamma", "2.0.0", strings.Repeat("c", 64))
	same.Installed = "2.0.0"
	same.InstalledSHA256 = pluginfeed.NormalizeSHA256(strings.Repeat("c", 64))
	m := feedTestModel(t, same)
	next, cmd := m.startInstall(same)
	got := next.(Model)
	if got.feedBusy || cmd != nil {
		t.Fatalf("已是最新不应触发安装")
	}
	if !strings.Contains(got.status, "已是最新") {
		t.Fatalf("应提示已是最新，实际 %q", got.status)
	}

	down := sameOriginPkg("delta", "Delta", "1.0.0", strings.Repeat("d", 64))
	down.Installed = "3.0.0"
	m = feedTestModel(t, down)
	next, cmd = m.startInstall(down)
	got = next.(Model)
	if got.feedBusy || cmd != nil {
		t.Fatalf("降级不应触发安装")
	}
	if !strings.Contains(got.status, "拒绝降级") {
		t.Fatalf("应提示拒绝降级，实际 %q", got.status)
	}
}

// 下载进度到达界面后继续等待下一帧；结束哨兵清空进度。
func TestFeedProgressMessages(t *testing.T) {
	m := feedTestModel(t, sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64)))
	m.feedBusy = true

	next, cmd := m.Update(installProgressMsg{done: 512, total: 4096})
	m = next.(Model)
	if m.feedProg.done != 512 || m.feedProg.total != 4096 {
		t.Fatalf("进度未记录: %+v", m.feedProg)
	}
	if cmd == nil {
		t.Fatalf("应继续等待下一帧进度")
	}
	if out := m.viewFeedDetail(80, 20); !strings.Contains(out, "12%") {
		t.Fatalf("进度百分比缺失:\n%s", out)
	}

	next, _ = m.Update(installProgressMsg{done: progressDone})
	m = next.(Model)
	if m.feedProg.total != 0 {
		t.Fatalf("结束哨兵应清空进度: %+v", m.feedProg)
	}
}

// 安装完成后退出忙碌态、记日志，并重新拉取订阅。
func TestInstallDoneReloadsFeed(t *testing.T) {
	e := sameOriginPkg("alpha", "Alpha", "1.0.0", strings.Repeat("a", 64))
	m := feedTestModel(t, e)
	m.feedBusy = true
	m.feedLoaded = true

	next, cmd := m.Update(installDoneMsg{entry: e, result: &pluginfeed.Installed{}})
	m = next.(Model)
	if m.feedBusy {
		t.Fatalf("完成后应退出忙碌态")
	}
	if m.feedLoaded {
		t.Fatalf("完成后应重新拉取订阅")
	}
	if cmd == nil {
		t.Fatalf("完成后应返回刷新命令")
	}
	if !strings.Contains(m.status, "已安装 Alpha") {
		t.Fatalf("状态未更新: %q", m.status)
	}

	// 失败路径：报错并退出忙碌态。
	m2 := feedTestModel(t, e)
	m2.feedBusy = true
	next, _ = m2.Update(installDoneMsg{entry: e, err: errFeedStub})
	m2 = next.(Model)
	if m2.feedBusy {
		t.Fatalf("失败后应退出忙碌态")
	}
	if m2.fatal == nil {
		t.Fatalf("失败后应提示错误")
	}
}

// 迟到的拉取结果不能覆盖已切换的订阅。
func TestFeedLoadedMessageIgnoredForOtherURL(t *testing.T) {
	m := feedTestModel(t)
	next, _ := m.Update(feedLoadedMsg{url: "https://other.example.com/f.yaml",
		entries: []pluginfeed.Entry{sameOriginPkg("x", "X", "1.0.0", strings.Repeat("a", 64))}})
	m = next.(Model)
	if len(m.feedEntries) != 0 {
		t.Fatalf("过期结果不应写入: %+v", m.feedEntries)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:     "512 B",
		2048:    "2 KB",
		5 << 20: "5.0 MB",
		3 << 30: "3.00 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %q，期望 %q", in, got, want)
		}
	}
}
