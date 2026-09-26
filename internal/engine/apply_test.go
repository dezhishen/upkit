package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/settings"
)

// stubSource 记录被查询的次数并总是失败 —— 这样不必真的准备安装包，
// 也能观察「同一个 id 被处理了几次」。
type stubSource struct{ calls *int32 }

func (stubSource) Name() string { return "stub" }

func (s stubSource) Latest(context.Context, core.AppRef) (core.Release, error) {
	atomic.AddInt32(s.calls, 1)
	return core.Release{}, errors.New("stub: 不提供版本")
}

func (s stubSource) Versions(context.Context, core.AppRef, int) ([]core.Release, error) {
	return nil, errors.New("stub: 不提供版本")
}

// newStubEngine 构造一个只接了假来源的 Engine。
func newStubEngine(t *testing.T, calls *int32, ids ...string) *Engine {
	t.Helper()

	reg := registry.New()
	reg.RegisterSource("stub", func(core.AppRef, registry.Deps) (core.SourceResolver, error) {
		return stubSource{calls: calls}, nil
	})
	// 探测链与安装方式都必须是可用的：Build 在构建 AppRef 时会校验它们存在，
	// 否则 List 阶段就会失败，根本走不到被测试的执行路径。
	reg.RegisterDetector("noop", func(core.AppRef, registry.Deps) (core.Detector, error) {
		return stubDetector{}, nil
	})
	reg.RegisterMethod("stub", func(core.AppRef, registry.Deps) (core.InstallMethod, error) {
		return stubMethod{}, nil
	})
	reg.RegisterUnpacker("stub", func(core.AppRef, registry.Deps) (core.Unpacker, error) {
		return stubUnpacker{}, nil
	})

	afs := apps.Default()
	for _, id := range ids {
		afs.Apps = append(afs.Apps, apps.AppSpec{
			ID:      id,
			Name:    id,
			Source:  map[string]any{"kind": "stub"},
			Method:  map[string]any{"kind": "stub"},
			Unpack:  map[string]any{"kind": "stub"},
			Install: apps.InstallSpec{Path: t.TempDir()},
			Detect:  []string{"noop"},
		})
	}

	eng, err := New(Options{
		Settings: settings.Default(),
		Apps:     afs,
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng
}

type stubDetector struct{}

func (stubDetector) Name() string { return "noop" }

func (stubDetector) Detect(context.Context, core.AppRef) (core.Status, error) {
	return core.Status{}, nil
}

// stubMethod 只用于让清单能通过校验；真正被执行时一律报错，
// 这样测试观察的是「谁被调用了、调了几次」而不是安装结果。
type stubMethod struct{}

func (stubMethod) Name() string    { return "stub" }
func (stubMethod) Caps() core.Caps { return core.Caps{} }
func (stubMethod) Plan(context.Context, core.Request) (core.Plan, error) {
	return core.Plan{}, errors.New("stub: 不提供计划")
}
func (stubMethod) Execute(context.Context, core.Request, core.EventSink) (core.Result, error) {
	return core.Result{}, errors.New("stub: 不执行")
}
func (stubMethod) Uninstall(context.Context, core.Request, core.UninstallOptions) error {
	return errors.New("stub: 不卸载")
}
func (stubMethod) Rollback(context.Context, core.Request, string) error {
	return errors.New("stub: 不回滚")
}
func (stubMethod) Backups(context.Context, core.Request) ([]core.Backup, error) {
	return nil, nil
}

// stubUnpacker 同 stubMethod：只为了让清单通过校验。
type stubUnpacker struct{}

func (stubUnpacker) Name() string { return "stub" }

func (stubUnpacker) Unpack(context.Context, core.UnpackRequest, core.EventSink) (core.UnpackResult, error) {
	return core.UnpackResult{}, errors.New("stub: 不解包")
}

// TestApplyManyDeduplicates 是「同一软件并发更新互相践踏」的回归用例。
//
// 重复的 id 以前会被原样并发执行，于是两个 goroutine 同时改写同一个
// InstallPath：交错执行删除与复制会互相删掉对方刚写入的文件，备份目录名
// 只精确到秒也可能碰撞，而最终仍可能报告「成功」。
func TestApplyManyDeduplicates(t *testing.T) {
	var calls int32
	eng := newStubEngine(t, &calls, "app-a", "app-b")

	if _, err := eng.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}

	results := eng.ApplyMany(context.Background(), []string{"app-a", "app-a", "app-b", "app-a"}, 4)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("两个不同的软件应各处理一次（共 2 次），实际 %d 次 —— 去重未生效", got)
	}
	if len(results) != 2 {
		t.Fatalf("结果数应等于去重后的数量 2，实际 %d", len(results))
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.AppID] {
			t.Fatalf("结果里出现重复的 %q", r.AppID)
		}
		seen[r.AppID] = true
	}
}

// TestApplyManyEmpty 空前不该 panic，也不该启动任何任务。
func TestApplyManyEmpty(t *testing.T) {
	var calls int32
	eng := newStubEngine(t, &calls)

	if got := eng.ApplyMany(context.Background(), nil, 2); len(got) != 0 {
		t.Fatalf("空输入应返回空结果，实际 %v", got)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("空输入不应触发任何来源调用")
	}
}

// TestCacheFileNameDistinguishesSources 是「缓存串包」的回归用例。
//
// 缓存键以前只取产物的 base name，于是两个软件的同名产物（setup.exe、
// app.zip 极常见）会互相命中：A 的安装包被当成 B 的包安装，还会写进 B 的
// 版本记录，用户看到的是一次「成功」的错误安装。
func TestCacheFileNameDistinguishesSources(t *testing.T) {
	artA := core.Artifact{Name: "setup.exe", URL: "https://a.test/setup.exe"}
	artB := core.Artifact{Name: "setup.exe", URL: "https://b.test/setup.exe"}

	nameA := cacheFileName(core.AppRef{ID: "app-a"}, artA)
	nameB := cacheFileName(core.AppRef{ID: "app-b"}, artB)
	if nameA == nameB {
		t.Fatalf("不同来源的同名产物不应共用一个缓存名：%q", nameA)
	}

	// 同一个软件换了下载地址（上游换 CDN）也必须换名，否则会复用旧包。
	nameA2 := cacheFileName(core.AppRef{ID: "app-a"}, core.Artifact{
		Name: "setup.exe", URL: "https://cdn.test/setup.exe"})
	if nameA2 == nameA {
		t.Fatalf("同一软件换地址后不应复用旧缓存名：%q", nameA)
	}

	// 同一软件同一地址必须稳定，否则缓存永远命不中。
	if again := cacheFileName(core.AppRef{ID: "app-a"}, artA); again != nameA {
		t.Fatalf("同名同址应得到稳定结果：%q vs %q", again, nameA)
	}
}

func TestCacheFileNameIsSafe(t *testing.T) {
	// appID 来自清单，可能含路径分隔符或 Windows 非法字符；
	// 它会被拼进文件名，必须清洗干净。
	for _, id := range []string{"a/b", `a\b`, "a:b", "../evil", "a b"} {
		got := cacheFileName(core.AppRef{ID: id}, core.Artifact{
			Name: "setup.exe", URL: "https://x.test/setup.exe"})
		for _, bad := range []string{"/", `\`, ":", ".."} {
			if strings.Contains(got, bad) {
				t.Errorf("appID %q 产生的缓存名 %q 不应含 %q", id, got, bad)
			}
		}
	}
}

// TestLockForSameID 确认同一软件的锁是同一把 —— 这是「并发更新互相践踏」的
// 修复基础：不同 goroutine 拿到不同锁就等于没锁。
func TestLockForSameID(t *testing.T) {
	e := &Engine{}

	l1 := e.lockFor("demo")
	l2 := e.lockFor("demo")
	if l1 != l2 {
		t.Fatal("同一 id 必须返回同一把锁")
	}
	if other := e.lockFor("other"); other == l1 {
		t.Fatal("不同 id 不应共用一把锁")
	}
}

// TestLockForSerializes 验证锁真的能串行化临界区。
func TestLockForSerializes(t *testing.T) {
	e := &Engine{}
	lock := e.lockFor("demo")

	var mu sync.Mutex
	inside := 0
	maxInside := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := e.lockFor("demo")
			l.Lock()
			defer l.Unlock()

			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()

			// 制造一点临界区宽度，让并发真的可能重叠。
			for j := 0; j < 1000; j++ {
				_ = j
			}

			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Fatalf("临界区同时进入了 %d 个 goroutine，锁没有生效", maxInside)
	}
	_ = lock
}
