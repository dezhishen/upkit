package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/settings"
	"github.com/dezhishen/upkit/internal/version"
)

// ── 假适配器 ──────────────────────────────────────────────────
//
// 这一组测试的目标是把 Apply/Plan/Rollback/Uninstall 这条主干跑起来：真实的四条轴
// （GitHub、zip、portable-inplace、PE 版本）都需要网络或 Windows，而引擎本身只认
// core 的接口 —— 因此用假适配器驱动它，正是「编排」这一层的单元测试。

// fakeSource 返回固定版本，产物指向测试用 HTTP 服务器。
type fakeSource struct {
	version string
	art     core.Artifact
}

func (s *fakeSource) Name() string { return "fake-source" }

func (s *fakeSource) Latest(context.Context, core.AppRef) (core.Release, error) {
	return core.Release{
		Version: s.version, Tag: "v" + s.version, Channel: "stable",
		PublishedAt: time.Now(), Artifacts: []core.Artifact{s.art},
	}, nil
}

func (s *fakeSource) Versions(ctx context.Context, app core.AppRef, _ int) ([]core.Release, error) {
	rel, err := s.Latest(ctx, app)
	if err != nil {
		return nil, err
	}
	return []core.Release{rel}, nil
}

// fakeVerifyingSource 额外实现 core.Verifier（上游 .sha256 那条路）。
type fakeVerifyingSource struct {
	fakeSource
	digest string
}

func (s *fakeVerifyingSource) ExpectedDigest(context.Context, core.AppRef, core.Artifact) (string, error) {
	return s.digest, nil
}

// fakeUnpacker 只造出一个源根目录（真解包器产出的文件树对引擎来说没有区别）。
type fakeUnpacker struct{ calls int32 }

func (u *fakeUnpacker) Name() string { return "fake-unpack" }

func (u *fakeUnpacker) Unpack(_ context.Context, req core.UnpackRequest, _ core.EventSink) (core.UnpackResult, error) {
	atomic.AddInt32(&u.calls, 1)
	if err := os.MkdirAll(req.DestDir, 0o755); err != nil {
		return core.UnpackResult{}, err
	}
	if err := os.WriteFile(filepath.Join(req.DestDir, "demo.exe"), []byte("bin"), 0o755); err != nil {
		return core.UnpackResult{}, err
	}
	return core.UnpackResult{Root: req.DestDir, Files: 1, Bytes: 3}, nil
}

// fakeDetector 返回预先设定的本机状态。
type fakeDetector struct{ st core.Status }

func (d *fakeDetector) Name() string { return "fake-detect" }

func (d *fakeDetector) Detect(context.Context, core.AppRef) (core.Status, error) { return d.st, nil }

// fakeMethod 记录引擎交给它的一切，并按需造出/删掉安装目录。
type fakeMethod struct {
	caps core.Caps
	err  error // Execute 返回的错误（模拟安装失败）

	mu         sync.Mutex
	execs      []core.Request
	rollbacks  []string
	uninstalls []core.UninstallOptions
	backups    []core.Backup
}

func (m *fakeMethod) Name() string    { return "fake-method" }
func (m *fakeMethod) Caps() core.Caps { return m.caps }

func (m *fakeMethod) Plan(_ context.Context, req core.Request) (core.Plan, error) {
	p := req.Plan
	p.Steps = []core.Step{{Kind: core.StepCopy, Desc: "复制到 " + req.App.InstallPath, Critical: true}}
	return p, nil
}

func (m *fakeMethod) Execute(_ context.Context, req core.Request, _ core.EventSink) (core.Result, error) {
	if m.err != nil {
		return core.Result{}, m.err
	}
	m.mu.Lock()
	m.execs = append(m.execs, req)
	m.mu.Unlock()

	if err := os.MkdirAll(req.App.InstallPath, 0o755); err != nil {
		return core.Result{}, err
	}
	// 从源根目录搬一个文件过去，模拟「落地」。
	if req.SourceRoot != "" {
		if b, err := os.ReadFile(filepath.Join(req.SourceRoot, "demo.exe")); err == nil {
			if err := os.WriteFile(filepath.Join(req.App.InstallPath, "demo.exe"), b, 0o755); err != nil {
				return core.Result{}, err
			}
		}
	}
	return core.Result{
		Action: req.Plan.Action, From: req.Plan.From, To: req.Plan.To,
		InstallPath: req.App.InstallPath,
	}, nil
}

func (m *fakeMethod) Uninstall(_ context.Context, _ core.Request, opts core.UninstallOptions) error {
	m.mu.Lock()
	m.uninstalls = append(m.uninstalls, opts)
	m.mu.Unlock()
	return nil
}

func (m *fakeMethod) Rollback(_ context.Context, _ core.Request, path string) error {
	m.mu.Lock()
	m.rollbacks = append(m.rollbacks, path)
	m.mu.Unlock()
	return nil
}

func (m *fakeMethod) Backups(context.Context, core.Request) ([]core.Backup, error) {
	return m.backups, nil
}

// boolPtr 取一个布尔指针（清单里的开关字段是指针，nil 表示「没写过」）。
func boolPtr(v bool) *bool { return &v }

// syncRecorder 是并发安全的收集器。
//
// 引擎在批量执行时会并发地发事件、写审计，普通切片在这里真的会丢东西 ——
// race 检测器会抓住它，而且抓得对：真实运行时同样是并发的（CI 的 -race 就是
// 这么抓到的）。
type syncRecorder[T any] struct {
	mu   sync.Mutex
	vals []T
}

func (r *syncRecorder[T]) add(v T) {
	r.mu.Lock()
	r.vals = append(r.vals, v)
	r.mu.Unlock()
}

// list 返回一份快照：调用方拿到的是自己的副本，之后怎么用都不会再碰到收集器。
func (r *syncRecorder[T]) list() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]T(nil), r.vals...)
}

func (r *syncRecorder[T]) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.vals)
}

// fakePrompter 按脚本回答确认框。
type fakePrompter struct {
	answer bool
	calls  int32
}

func (p *fakePrompter) Confirm(context.Context, string, string) (bool, error) {
	atomic.AddInt32(&p.calls, 1)
	return p.answer, nil
}

// ── 夹具 ──────────────────────────────────────────────────────

// payload 是测试里「下载」到的东西。
var testPayload = []byte("payload-bytes-0123456789")

type fixture struct {
	eng      *Engine
	set      *settings.Settings
	afs      *apps.File
	reg      *registry.Registry
	method   *fakeMethod
	unpack   *fakeUnpacker
	detect   *fakeDetector
	prompt   *fakePrompter
	root     string
	install  string
	srvURL   string
	requests int32
	events   syncRecorder[core.Event]
	audit    syncRecorder[map[string]any]
}

// newFixture 装配一个「四条轴都是假的」引擎。
func newFixture(t *testing.T, tweak func(*fixture, *settings.Settings)) *fixture {
	t.Helper()
	root := t.TempDir()

	f := &fixture{
		method:  &fakeMethod{caps: core.Caps{NeedsUnpack: true, Rollbackable: true}},
		unpack:  &fakeUnpacker{},
		detect:  &fakeDetector{},
		prompt:  &fakePrompter{answer: true},
		root:    root,
		install: filepath.Join(root, "install", "demo"),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&f.requests, 1)
		_, _ = w.Write(testPayload)
	}))
	t.Cleanup(srv.Close)
	f.srvURL = srv.URL

	set := settings.Default()
	set.Path = filepath.Join(root, "config", settings.FileName)
	set.Storage.TempDir = filepath.Join(root, "temp")
	set.Storage.CacheDir = filepath.Join(root, "cache")
	set.Storage.BackupDir = filepath.Join(root, "backup")
	set.Storage.DataDir = filepath.Join(root, "data")
	set.Behavior.ConfirmBeforeApply = false
	set.Behavior.WaitForClose = false
	set.Behavior.LaunchAfterUpdate = false
	if err := set.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := set.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	f.set = set

	src := &fakeSource{version: "1.0.0", art: core.Artifact{
		Name: "demo.zip", URL: srv.URL + "/demo.zip", Size: int64(len(testPayload)),
	}}

	afs := apps.Default()
	afs.Path = filepath.Join(root, "config", settings.AppsFileName)
	afs.Apps = []apps.AppSpec{{
		ID:      "demo",
		Name:    "Demo",
		Source:  map[string]any{apps.KeyKind: "fake"},
		Unpack:  map[string]any{apps.KeyKind: "fake"},
		Method:  map[string]any{apps.KeyKind: "fake"},
		Detect:  []string{"fake"},
		Install: apps.InstallSpec{Path: f.install, Entrypoints: []string{"demo.exe"}},
	}}
	f.afs = afs

	reg := registry.New()
	reg.RegisterSource("fake", func(core.AppRef, registry.Deps) (core.SourceResolver, error) { return src, nil })
	reg.RegisterUnpacker("fake", func(core.AppRef, registry.Deps) (core.Unpacker, error) { return f.unpack, nil })
	reg.RegisterMethod("fake", func(core.AppRef, registry.Deps) (core.InstallMethod, error) { return f.method, nil })
	reg.RegisterDetector("fake", func(core.AppRef, registry.Deps) (core.Detector, error) { return f.detect, nil })
	f.reg = reg

	if tweak != nil {
		tweak(f, set)
	}
	f.eng = f.buildEngine(t, nil)
	return f
}

// buildEngine 用同一套夹具再造一个引擎（给 Options 不同的场景用，比如 DryRun）。
func (f *fixture) buildEngine(t *testing.T, tweak func(*Options)) *Engine {
	t.Helper()
	opts := Options{
		Settings: f.set,
		Apps:     f.afs,
		Registry: f.reg,
		Sink:     core.SinkFunc(f.events.add),
		Prompter: f.prompt,
		Audit:    f.audit.add,
	}
	if tweak != nil {
		tweak(&opts)
	}
	eng, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng
}

// addApp 再挂一个软件（同一套假适配器，安装目录各自独立）。
func (f *fixture) addApp(t *testing.T, id string) {
	t.Helper()
	f.afs.Apps = append(f.afs.Apps, apps.AppSpec{
		ID:      id,
		Name:    strings.ToUpper(id),
		Source:  map[string]any{apps.KeyKind: "fake"},
		Unpack:  map[string]any{apps.KeyKind: "fake"},
		Method:  map[string]any{apps.KeyKind: "fake"},
		Detect:  []string{"fake"},
		Install: apps.InstallSpec{Path: filepath.Join(f.root, "install", id), Entrypoints: []string{"demo.exe"}},
	})
}

// eventsOf 取某个软件的事件消息（拼接）。
func (f *fixture) eventsOf(kind core.EventKind) []string {
	var out []string
	for _, e := range f.events.list() {
		if e.Kind == kind {
			out = append(out, e.Msg)
		}
	}
	return out
}

// tempEntries 返回临时目录里剩下的条目（正常情况应当为空）。
func (f *fixture) tempEntries(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(f.set.Storage.TempDir)
	if err != nil {
		t.Fatalf("读临时目录: %v", err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// ── 测试 ──────────────────────────────────────────────────────

// 全新安装的全流程：下载 → 解包 → 落地 → 写状态 → 审计 → 清理临时目录。
func TestApplyInstallEndToEnd(t *testing.T) {
	f := newFixture(t, nil)

	res, err := f.eng.Apply(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Action != core.ActionInstall || res.To != "1.0.0" {
		t.Fatalf("结果不对: %+v", res)
	}
	if res.Downloaded != int64(len(testPayload)) || res.SHA256 == "" {
		t.Fatalf("下载信息缺失: downloaded=%d sha=%q", res.Downloaded, res.SHA256)
	}

	req := f.method.execs[0]
	if req.ArtifactPath == "" || req.SourceRoot == "" {
		t.Fatalf("适配器没拿到产物/源根目录: %+v", req)
	}
	if !strings.Contains(req.SourceRoot, "extract") {
		t.Fatalf("解包目录应落在工作目录下的 extract，实际 %q", req.SourceRoot)
	}
	if _, err := os.Stat(filepath.Join(f.install, "demo.exe")); err != nil {
		t.Fatalf("安装目录里没有落地文件: %v", err)
	}

	// 状态记录：下次探测才能知道装的是哪个版本。
	rec, err := version.ReadRecord(f.install)
	if err != nil || rec == nil {
		t.Fatalf("状态记录未写入: rec=%+v err=%v", rec, err)
	}
	if rec.Version != "1.0.0" || rec.SHA256 == "" {
		t.Fatalf("状态记录内容不对: %+v", rec)
	}

	// 缓存留着复用，临时目录要清干净。
	if ents, _ := os.ReadDir(f.set.Storage.CacheDir); len(ents) != 1 {
		t.Fatalf("缓存里应有 1 个产物，实际 %d", len(ents))
	}
	if left := f.tempEntries(t); len(left) != 0 {
		t.Fatalf("临时目录未清理: %v", left)
	}

	// 事件与审计：界面靠事件显示进度，审计是事后追溯的唯一凭据。
	if got := f.eventsOf(core.EventStarted); len(got) != 1 {
		t.Fatalf("应有 1 个 started 事件，实际 %v", f.eventsOf(core.EventStarted))
	}
	if got := f.eventsOf(core.EventFinished); len(got) != 1 {
		t.Fatalf("应有 1 个 finished 事件，实际 %v", got)
	}
	if entries := f.audit.list(); len(entries) == 0 || entries[len(entries)-1]["result"] != "success" {
		t.Fatalf("审计条目缺失或未记成功: %+v", entries)
	}
}

// 第二次安装命中缓存：不再下载（服务器请求数不涨）。
func TestApplyReusesDownloadedArtifact(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	if _, err := f.eng.Apply(ctx, "demo"); err != nil {
		t.Fatalf("第一次 Apply: %v", err)
	}
	first := atomic.LoadInt32(&f.requests)

	res, err := f.eng.Apply(ctx, "demo")
	if err != nil {
		t.Fatalf("第二次 Apply: %v", err)
	}
	if !res.ReusedCache {
		t.Fatalf("第二次应命中缓存，实际 reused=false")
	}
	if got := atomic.LoadInt32(&f.requests); got != first {
		t.Fatalf("命中缓存不该再下载：请求数 %d → %d", first, got)
	}
	if len(f.method.execs) != 2 {
		t.Fatalf("两次都应真正执行落地，实际 %d 次", len(f.method.execs))
	}
}

// 自包含方式（插件全权接管）：宿主不下载、不解包，只交工作目录。
func TestApplySelfContainedSkipsDownload(t *testing.T) {
	f := newFixture(t, func(f *fixture, _ *settings.Settings) {
		f.method.caps = core.Caps{SelfContained: true}
	})

	res, err := f.eng.Apply(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := atomic.LoadInt32(&f.requests); n != 0 {
		t.Fatalf("自包含方式不该由宿主下载，实际发了 %d 次请求", n)
	}
	if res.Downloaded != 0 {
		t.Fatalf("自包含方式的下载量应为 0，实际 %d", res.Downloaded)
	}
	req := f.method.execs[0]
	if req.WorkDir == "" || req.ArtifactPath != "" {
		t.Fatalf("应只交工作目录，实际 workDir=%q artifact=%q", req.WorkDir, req.ArtifactPath)
	}
}

// DryRun 只出计划，什么都不做。
func TestApplyDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t, nil)

	eng := f.buildEngine(t, func(o *Options) { o.DryRun = true })
	res, err := eng.Apply(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Action != core.ActionInstall {
		t.Fatalf("DryRun 也应给出动作，实际 %+v", res)
	}
	if len(f.method.execs) != 0 {
		t.Fatalf("DryRun 不该执行落地")
	}
	if _, err := os.Stat(f.install); !os.IsNotExist(err) {
		t.Fatalf("DryRun 不该创建安装目录")
	}
	if n := atomic.LoadInt32(&f.requests); n != 0 {
		t.Fatalf("DryRun 不该下载，实际 %d 次", n)
	}
}

// 用户在执行前确认里点了「否」：中止，且不落地。
func TestApplyAbortsWhenUserDeclines(t *testing.T) {
	f := newFixture(t, func(f *fixture, s *settings.Settings) {
		s.Behavior.ConfirmBeforeApply = true
		f.prompt.answer = false
	})

	_, err := f.eng.Apply(context.Background(), "demo")
	if !errors.Is(err, core.ErrUserAborted) {
		t.Fatalf("应报用户中止，实际 %v", err)
	}
	if len(f.method.execs) != 0 {
		t.Fatalf("中止后不该落地")
	}
	// 落地的临时目录同样要清掉。
	if left := f.tempEntries(t); len(left) != 0 {
		t.Fatalf("临时目录未清理: %v", left)
	}
}

// 安装失败时也要清理工作目录，并把失败写进审计。
func TestApplyCleansUpOnFailure(t *testing.T) {
	f := newFixture(t, func(f *fixture, _ *settings.Settings) {
		f.method.err = errors.New("替换文件失败")
	})

	if _, err := f.eng.Apply(context.Background(), "demo"); err == nil {
		t.Fatalf("应报错")
	}
	if left := f.tempEntries(t); len(left) != 0 {
		t.Fatalf("失败后临时目录未清理: %v", left)
	}
	if got := f.eventsOf(core.EventFailed); len(got) != 1 {
		t.Fatalf("应有 1 个失败事件，实际 %v", got)
	}
	entries := f.audit.list()
	found := false
	for _, e := range entries {
		if e["result"] == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计里应记失败: %+v", entries)
	}
}

// 上游没有产物（且方式不自包含）时明确报错，而不是空转。
func TestApplyFailsWithoutArtifacts(t *testing.T) {
	f := newFixture(t, nil)
	f.reg.RegisterSource("fake", func(core.AppRef, registry.Deps) (core.SourceResolver, error) {
		return &fakeSource{version: "1.0.0"}, nil
	})

	_, err := f.eng.Apply(context.Background(), "demo")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("应报找不到产物，实际 %v", err)
	}
}

// 磁盘余量不足时提前拦住（下载/解包都是白费）。
func TestApplyChecksFreeSpace(t *testing.T) {
	f := newFixture(t, func(_ *fixture, s *settings.Settings) {
		s.Storage.MinFreeSpaceMB = 1 << 24 // 16 TB，任何测试机都不够
	})

	_, err := f.eng.Apply(context.Background(), "demo")
	if err == nil || !strings.Contains(err.Error(), "磁盘空间不足") {
		t.Fatalf("应报磁盘空间不足，实际 %v", err)
	}
	if n := atomic.LoadInt32(&f.requests); n != 0 {
		t.Fatalf("空间不足不该开始下载，实际 %d 次", n)
	}
}

// 上游摘要不符：报校验错，并把坏包删掉（否则下次会命中缓存复用坏包）。
func TestApplyRejectsBadChecksum(t *testing.T) {
	f := newFixture(t, nil)
	f.reg.RegisterSource("fake", func(core.AppRef, registry.Deps) (core.SourceResolver, error) {
		return &fakeVerifyingSource{
			fakeSource: fakeSource{version: "1.0.0", art: core.Artifact{
				Name: "demo.zip", URL: f.srvURL + "/demo.zip", Size: int64(len(testPayload)),
			}},
			digest: strings.Repeat("00", 32),
		}, nil
	})

	_, err := f.eng.Apply(context.Background(), "demo")
	if !errors.Is(err, core.ErrChecksum) {
		t.Fatalf("应报校验失败，实际 %v", err)
	}
	if ents, _ := os.ReadDir(f.set.Storage.CacheDir); len(ents) != 0 {
		t.Fatalf("坏包不该留在缓存里: %v", ents)
	}
}

// 已是最新时不做任何事。
func TestApplyNoopWhenUpToDate(t *testing.T) {
	f := newFixture(t, func(f *fixture, _ *settings.Settings) {
		f.detect.st = core.Status{Installed: true, Version: "1.0.0", Path: f.install}
	})

	res, err := f.eng.Apply(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.AlreadyInPlace || res.Action != core.ActionNoOp {
		t.Fatalf("应识别为已是最新: %+v", res)
	}
	if len(f.method.execs) != 0 {
		t.Fatalf("已是最新不该落地")
	}
}

// 检查：本机 0.9、上游 1.0 → 可更新。
func TestCheckOneDetectsUpdate(t *testing.T) {
	f := newFixture(t, func(f *fixture, _ *settings.Settings) {
		f.detect.st = core.Status{Installed: true, Version: "0.9.0", Path: f.install}
	})

	if _, err := f.eng.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	a, err := f.eng.CheckOne(context.Background(), "demo")
	if err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if a.Action != core.ActionUpdate || a.Release.Version != "1.0.0" || a.Status.Version != "0.9.0" {
		t.Fatalf("状态不对: action=%v status=%+v release=%+v", a.Action, a.Status, a.Release)
	}
	// 计划里应带上落地步骤（界面靠它展示「将要做什么」）。
	p, err := f.eng.Plan(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Steps) == 0 || p.To != "1.0.0" || p.Backup == false {
		t.Fatalf("计划不对: %+v", p)
	}
}

// 回滚：不指定备份时用最近一个；没有备份、或方式不支持回滚时明确报错。
func TestRollbackPicksLatestBackup(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	f := newFixture(t, func(f *fixture, _ *settings.Settings) {
		f.method.backups = []core.Backup{
			{Path: filepath.Join(f.root, "backup", "old"), Version: "0.9.0", CreatedAt: older},
			{Path: filepath.Join(f.root, "backup", "new"), Version: "1.0.0", CreatedAt: time.Now()},
		}
	})

	if err := f.eng.Rollback(context.Background(), "demo", ""); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(f.method.rollbacks) != 1 || !strings.HasSuffix(f.method.rollbacks[0], "new") {
		t.Fatalf("应回滚到最近一次备份，实际 %v", f.method.rollbacks)
	}
}

func TestRollbackErrors(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	// 没有备份。
	if err := f.eng.Rollback(ctx, "demo", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("没有备份时应报 ErrNotFound，实际 %v", err)
	}
	// 方式不支持回滚。
	f.method.caps = core.Caps{NeedsUnpack: true}
	if err := f.eng.Rollback(ctx, "demo", "/tmp/bak"); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("不支持回滚时应报 ErrUnsupported，实际 %v", err)
	}
	// 未知软件。
	if err := f.eng.Rollback(ctx, "nope", ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("未知软件应报 ErrNotFound，实际 %v", err)
	}
}

// 卸载把「是否保留用户数据」原样交给适配器，并记审计。
func TestUninstallPassesKeepUserData(t *testing.T) {
	f := newFixture(t, nil)

	if err := f.eng.Uninstall(context.Background(), "demo", true); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(f.method.uninstalls) != 1 || !f.method.uninstalls[0].KeepUserData {
		t.Fatalf("应把 KeepUserData 传给适配器，实际 %+v", f.method.uninstalls)
	}
	if entries := f.audit.list(); len(entries) == 0 || entries[0]["action"] != "uninstall" {
		t.Fatalf("应记审计: %+v", entries)
	}
}

// 批量执行：每个软件各一次；重复的 id 去重（并发跑同一个目录会互相践踏）。
func TestApplyManyDeduplicatesAndRunsAll(t *testing.T) {
	f := newFixture(t, nil)
	for _, id := range []string{"two", "three"} {
		f.addApp(t, id)
	}

	results := f.eng.ApplyMany(context.Background(), []string{"demo", "two", "demo", "three"}, 2)
	if len(results) != 3 {
		t.Fatalf("去重后应有 3 个结果，实际 %d", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s 执行失败: %v", r.AppID, r.Err)
		}
		if r.Result == nil || r.Result.InstallPath == "" {
			t.Fatalf("%s 结果不完整: %+v", r.AppID, r.Result)
		}
	}
	if len(f.method.execs) != 3 {
		t.Fatalf("三个软件各落地一次，实际 %d 次（重复 id 会并发改写同一目录）", len(f.method.execs))
	}
}

// 缓存清理只留最新的 keep 份。
func TestPruneCacheKeepsNewest(t *testing.T) {
	f := newFixture(t, func(_ *fixture, s *settings.Settings) { s.Storage.CacheKeep = 2 })

	for i, name := range []string{"a.zip", "b.zip", "c.zip", "d.zip"} {
		path := filepath.Join(f.set.Storage.CacheDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("写入缓存: %v", err)
		}
		// 依次拉开修改时间，谁是「最新」才可控。
		mod := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("改时间: %v", err)
		}
	}

	f.eng.pruneCache()

	ents, err := os.ReadDir(f.set.Storage.CacheDir)
	if err != nil {
		t.Fatalf("读缓存目录: %v", err)
	}
	got := map[string]bool{}
	for _, e := range ents {
		got[e.Name()] = true
	}
	if len(got) != 2 || !got["c.zip"] || !got["d.zip"] {
		t.Fatalf("应只留最新的两份，实际 %v", got)
	}
}

// 停用的软件不参与检查与更新，但必须留在列表里（用户要能看见它、再启回来）。
//
// 以前清单层直接把停用的软件过滤掉：按了空格之后它就从界面上消失，既看不到也
// 启不回来。
func TestDisabledAppVisibleButNotActedOn(t *testing.T) {
	f := newFixture(t, nil)
	f.addApp(t, "off")
	f.afs.Apps[1].Enabled = boolPtr(false)

	ctx := context.Background()
	list, err := f.eng.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("停用的软件也要在列表里：%+v", list)
	}
	off := f.eng.Find("off")
	if off == nil || !off.Ref.Disabled {
		t.Fatalf("停用标记没带上：%+v", off)
	}

	// 检查：只做本地探测，不问上游（上游是 httptest 服务，问一次 requests 会 +1）。
	before := atomic.LoadInt32(&f.requests)
	if _, err := f.eng.CheckOne(ctx, "off"); err != nil {
		t.Fatalf("检查停用的软件不该报错: %v", err)
	}
	if got := atomic.LoadInt32(&f.requests); got != before {
		t.Fatalf("停用的软件不该去问上游（请求数 %d → %d）", before, got)
	}
	if off.Note != "" && !strings.Contains(off.Note, "已停用") {
		t.Fatalf("应说明它被停用：%q", off.Note)
	}

	// 计划与执行都要明确拒绝，并告诉用户怎么恢复。
	if _, err := f.eng.Plan(ctx, "off"); !errors.Is(err, core.ErrDisabled) {
		t.Fatalf("停用的软件不该能生成计划: %v", err)
	}
	if _, err := f.eng.Apply(ctx, "off"); !errors.Is(err, core.ErrDisabled) {
		t.Fatalf("停用的软件不该能执行: %v", err)
	}
	if len(f.method.execs) != 0 {
		t.Fatalf("停用的软件不该真的被安装：%+v", f.method.execs)
	}

	// 批量执行直接跳过它：不参与，不是失败。
	results := f.eng.ApplyMany(ctx, []string{"demo", "off"}, 2)
	if len(results) != 1 || results[0].AppID != "demo" {
		t.Fatalf("批量执行应只留启用的软件：%+v", results)
	}
}

// 停用的软件不参与冲突判定：它不该把在用的那个软件标成「冲突」。
func TestDisabledAppDoesNotShadowOthers(t *testing.T) {
	f := newFixture(t, nil)
	f.addApp(t, "dup")
	f.afs.Apps[1].Install.Path = f.afs.Apps[0].Install.Path // 同目标
	f.afs.Apps[1].Name = f.afs.Apps[0].Name
	f.afs.Apps[1].Enabled = boolPtr(false)

	list, err := f.eng.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list[0].Shadowed {
		t.Fatalf("在用的软件不该被一个停用的软件挤掉：%+v", list[0].Conflict)
	}
	if list[1].Conflict != nil {
		t.Fatalf("停用的软件本身也不参与冲突判定：%+v", list[1].Conflict)
	}
}
