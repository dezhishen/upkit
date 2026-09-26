package plugin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dezhishen/upkit/pkg/plugin"
)

// 本文件验证 full 模式跨进程的完整链路：状态探测 → 计划 → 执行（含进度事件）
// → 状态变化 → 卸载，以及失败与取消两条异常路径。
//
// 用的是真实子进程里的示例插件（cmd/upkit-plugin-example 的 local-stub），
// 它自己写文件、自己判断装没装 —— 也就是插件作者会写的那种代码。

const stubAppID = "local-stub"

// stubRequest 组装一次软件级调用。
func stubRequest(appID, installPath string, cfg plugin.Config) plugin.SourceAppRequest {
	return plugin.SourceAppRequest{
		AppID:       appID,
		InstallPath: installPath,
		Runtime:     plugin.RuntimeConfig{Config: cfg},
	}
}

// collectEvents 在后台轮询事件，直到 stop 被关闭（关闭后再收尾拉一次）。
func collectEvents(t *testing.T, src plugin.Source, jobID string, stop <-chan struct{}) func() []plugin.Event {
	t.Helper()
	var mu sync.Mutex
	var all []plugin.Event

	drain := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		evs, err := src.PollEvents(ctx, jobID)
		if err != nil {
			return
		}
		if len(evs) > 0 {
			mu.Lock()
			all = append(all, evs...)
			mu.Unlock()
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				drain()
				return
			case <-time.After(20 * time.Millisecond):
				drain()
			}
		}
	}()

	return func() []plugin.Event {
		<-done
		drain() // 再补一次，确保没有遗漏
		mu.Lock()
		defer mu.Unlock()
		return append([]plugin.Event(nil), all...)
	}
}

// TestFullModeListDeclaresSoftware 确认示例插件同时提供 catalog 与 full 两种软件。
func TestFullModeListDeclaresSoftware(t *testing.T) {
	c := startExample(t)
	sw, err := c.Source().List(context.Background())
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	var found *plugin.Software
	for i := range sw {
		if sw[i].ID == stubAppID {
			found = &sw[i]
		}
	}
	if found == nil {
		t.Fatalf("没有找到 %s: %+v", stubAppID, sw)
	}
	// full 模式由 Defaults.Method 逐个软件声明，而不是靠插件级能力。
	if found.Defaults.Method != plugin.MethodPlugin {
		t.Fatalf("Defaults.Method = %q，期望 %q", found.Defaults.Method, plugin.MethodPlugin)
	}
}

// TestFullModeEndToEnd 走完整条链路。
func TestFullModeEndToEnd(t *testing.T) {
	c := startExample(t)
	src := c.Source()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "install")
	req := stubRequest(stubAppID, dir, plugin.Config{})

	// 1. 全新目录：未安装，且不报错（探测链的约定）。
	st, err := src.Status(ctx, req)
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	if st.Installed {
		t.Fatalf("全新目录不应报告已安装: %+v", st)
	}

	// 2. 计划由插件给出，宿主只做展示。
	plan, err := src.Plan(ctx, plugin.SourcePlanRequest{
		SourceAppRequest: req,
		Plan:             plugin.PlanRequest{From: "", To: "1.0.0"},
	})
	if err != nil {
		t.Fatalf("Plan 失败: %v", err)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("计划里没有步骤")
	}
	if plan.Action != plugin.ActionInstall {
		t.Fatalf("计划动作 = %q，期望 %q", plan.Action, plugin.ActionInstall)
	}

	// 3. 执行；期间并发拉取进度事件。
	const jobID = "job-e2e"
	stop := make(chan struct{})
	eventsOf := collectEvents(t, src, jobID, stop)

	res, err := src.Apply(ctx, plugin.SourcePlanRequest{
		SourceAppRequest: req,
		JobID:            jobID,
		Plan:             plugin.PlanRequest{From: "", To: "1.0.0"},
	})
	close(stop)
	events := eventsOf()
	if err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if res.InstallPath != dir {
		t.Fatalf("结果里的安装路径 = %q，期望 %q", res.InstallPath, dir)
	}

	// 4. 插件真的写了文件。
	for _, name := range []string{"stub-payload.txt", ".upkit-stub-version"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("插件没有写出 %s: %v", name, err)
		}
	}

	// 5. 事件确实跨进程传回来了，并且能还原出阶段与级别。
	if len(events) == 0 {
		t.Fatal("没有收到任何事件")
	}
	kinds := map[string]bool{}
	for _, e := range events {
		kinds[e.Kind] = true
		if e.Kind == plugin.EventLog && e.Level != plugin.LogLevelInfo {
			t.Fatalf("日志事件的级别丢失: %+v", e)
		}
	}
	for _, want := range []string{plugin.EventStarted, plugin.EventFinished} {
		if !kinds[want] {
			t.Fatalf("缺少 %s 事件: %v", want, kinds)
		}
	}

	// 6. 状态随之变化 —— 探测读的就是插件自己写的文件。
	st, err = src.Status(ctx, req)
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	if !st.Installed || st.Version != "1.0.0" {
		t.Fatalf("安装后状态不对: %+v", st)
	}

	// 7. 卸载把文件清掉。
	if err := src.Uninstall(ctx, plugin.SourceUninstallRequest{SourceAppRequest: req}); err != nil {
		t.Fatalf("Uninstall 失败: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("卸载后目录仍存在: %v", err)
	}
}

// TestFullModeApplyFailureCrossProcess 确认插件里的失败原因原样传回宿主，
// 并且错误分类（errors.Is）跨进程仍然成立。
func TestFullModeApplyFailureCrossProcess(t *testing.T) {
	c := startExample(t)
	ctx := context.Background()
	req := stubRequest(stubAppID, filepath.Join(t.TempDir(), "install"),
		plugin.NewConfig(map[string]string{"fail": "true"}))

	_, err := c.Source().Apply(ctx, plugin.SourcePlanRequest{
		SourceAppRequest: req,
		Plan:             plugin.PlanRequest{To: "1.0.0"},
	})
	if err == nil {
		t.Fatal("配置要求失败，却成功了")
	}
	if !errors.Is(err, plugin.ErrBadConfig) {
		t.Fatalf("错误分类丢失: %v", err)
	}
	if !contains(err.Error(), "配置要求本次安装失败") {
		t.Fatalf("插件的原始错误信息丢失: %v", err)
	}
}

// TestFullModeCancelStopsApply 确认宿主的取消通知能真的打断插件的长任务。
func TestFullModeCancelStopsApply(t *testing.T) {
	c := startExample(t)
	src := c.Source()
	ctx := context.Background()
	req := stubRequest(stubAppID, filepath.Join(t.TempDir(), "install"),
		plugin.NewConfig(map[string]string{"delay_ms": "5000"}))

	const jobID = "job-cancel"
	done := make(chan error, 1)
	go func() {
		_, err := src.Apply(ctx, plugin.SourcePlanRequest{
			SourceAppRequest: req,
			JobID:            jobID,
			Plan:             plugin.PlanRequest{To: "1.0.0"},
		})
		done <- err
	}()

	time.Sleep(150 * time.Millisecond)
	if err := src.Cancel(ctx, jobID); err != nil {
		t.Fatalf("Cancel 失败: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后 Apply 仍然成功")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消没有生效，插件仍在跑")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
