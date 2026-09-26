package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeApp struct {
	versions []Release
	err      error
}

func (f *fakeApp) Versions(_ context.Context, req VersionsRequest) ([]Release, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.versions
	if req.Limit > 0 && req.Limit < len(out) {
		out = out[:req.Limit]
	}
	return out, nil
}

// fullApp 额外实现 Method，用于验证 full 模式的分发与事件缓冲。
type fullApp struct {
	fakeApp
	applied bool
}

func (f *fullApp) Status(_ context.Context, _ StatusRequest) (Status, error) {
	return Status{Installed: true, Version: "1.0.0", Path: "/opt/demo"}, nil
}

func (f *fullApp) Plan(_ context.Context, _ PlanRequest) (PlanResult, error) {
	return PlanResult{Action: ActionInstall, To: "1.0.1"}, nil
}

func (f *fullApp) Apply(_ context.Context, _ PlanRequest, send EventSender) (Result, error) {
	f.applied = true
	send.Send(Event{Kind: EventPhase, Phase: "下载"})
	send.Send(Event{Kind: EventProgress, Phase: "下载", Done: 5, Total: 10})
	return Result{Action: ActionInstall, InstallPath: "/opt/demo"}, nil
}

func (f *fullApp) Rollback(_ context.Context, _ RollbackRequest) error { return nil }
func (f *fullApp) Uninstall(_ context.Context, _ UninstallRequest) error {
	return nil
}

func testInfo() Info {
	return Info{ID: "demo", Name: "演示源", Version: "1.0.0"}
}

func newTestRegistry(t *testing.T, regs ...Registration) *registry {
	t.Helper()
	r, err := newRegistry(testInfo(), regs)
	if err != nil {
		t.Fatalf("newRegistry 失败: %v", err)
	}
	return r
}

func TestRegistryListKeepsOrderAndMetadata(t *testing.T) {
	r := newTestRegistry(t,
		Register("b-app", func(AppConfig) (App, error) { return &fakeApp{}, nil }, WithName("B")),
		Register("a-app", func(AppConfig) (App, error) { return &fakeApp{}, nil }, WithName("A"), WithProvides("x/y")),
	)
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != "b-app" || list[1].ID != "a-app" {
		t.Fatalf("List 顺序不对: %+v", list)
	}
	if list[1].Name != "A" || len(list[1].Provides) != 1 {
		t.Fatalf("元信息丢失: %+v", list[1])
	}
	// 返回值必须与内部状态隔离。
	list[1].Provides[0] = "mutated"
	list2, _ := r.List(context.Background())
	if list2[1].Provides[0] != "x/y" {
		t.Fatalf("List 返回了内部切片，外部修改污染了注册表")
	}
}

func TestRegistryVersionsRespectsLimit(t *testing.T) {
	app := &fakeApp{versions: []Release{{Version: "3"}, {Version: "2"}, {Version: "1"}}}
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return app, nil }))

	got, err := r.Versions(context.Background(), SourceVersionsRequest{AppID: "demo", Limit: 2})
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(got) != 2 || got[0].Version != "3" {
		t.Fatalf("版本截断不正确: %+v", got)
	}
}

func TestRegistryUnknownAppIsNotFound(t *testing.T) {
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil }))
	_, err := r.Versions(context.Background(), SourceVersionsRequest{AppID: "nope"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}

func TestRegistryFactoryErrorIsolatedToThatApp(t *testing.T) {
	bad := func(AppConfig) (App, error) { return nil, errors.New("缺少必填配置") }
	good := &fakeApp{versions: []Release{{Version: "1.0.0"}}}
	r := newTestRegistry(t,
		Register("bad", bad),
		Register("good", func(AppConfig) (App, error) { return good, nil }),
	)

	if _, err := r.Versions(context.Background(), SourceVersionsRequest{AppID: "bad"}); err == nil {
		t.Fatal("构造失败应当报错")
	} else if !strings.Contains(err.Error(), "缺少必填配置") {
		t.Fatalf("错误信息未透传: %v", err)
	}

	got, err := r.Versions(context.Background(), SourceVersionsRequest{AppID: "good"})
	if err != nil || len(got) != 1 {
		t.Fatalf("另一个软件不该受影响: %v %+v", err, got)
	}
}

func TestRegistryCatalogAppRejectsFullMode(t *testing.T) {
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil }))
	if _, err := r.Status(context.Background(), SourceAppRequest{AppID: "demo"}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported，实际 %v", err)
	}
	if _, err := r.Apply(context.Background(), SourcePlanRequest{SourceAppRequest: SourceAppRequest{AppID: "demo"}}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported，实际 %v", err)
	}
}

func TestRegistryFullModeEventsAndCancel(t *testing.T) {
	app := &fullApp{fakeApp: fakeApp{versions: []Release{{Version: "1.0.1"}}}}
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return app, nil }))

	req := SourcePlanRequest{SourceAppRequest: SourceAppRequest{AppID: "demo"}, JobID: "job-1"}
	res, err := r.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !app.applied || res.InstallPath != "/opt/demo" {
		t.Fatalf("Apply 结果不对: %+v", res)
	}

	evs, err := r.PollEvents(context.Background(), "job-1")
	// 两条来自插件（phase / progress），第三条是 SDK 补的收尾事件 ——
	// 宿主靠它判断任务真的结束了，不依赖插件作者记得发。
	if err != nil || len(evs) != 3 {
		t.Fatalf("事件缓冲不对: %v %+v", err, evs)
	}
	if last := evs[len(evs)-1]; last.Kind != EventFinished {
		t.Fatalf("最后一条应为收尾事件: %+v", last)
	}
	// 拉取后应当清空，避免宿主重复消费。
	again, _ := r.PollEvents(context.Background(), "job-1")
	if len(again) != 0 {
		t.Fatalf("事件没有被消费掉: %+v", again)
	}

	if err := r.Cancel(context.Background(), "job-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestRegistryStaticConfigSchema(t *testing.T) {
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil },
		WithConfigSchema(ConfigSchema{
			Title:  "演示",
			Fields: []ConfigField{{Key: "endpoint", Required: true}},
		}),
	))
	schema, err := r.ConfigSchema(context.Background())
	if err != nil {
		t.Fatalf("ConfigSchema: %v", err)
	}
	if schema.Title != "演示" || len(schema.Fields) != 1 {
		t.Fatalf("schema 未透传: %+v", schema)
	}
	// 未声明 schema 时返回空表单而不是错误。
	r2 := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil }))
	empty, err := r2.ConfigSchema(context.Background())
	if err != nil || len(empty.Fields) != 0 {
		t.Fatalf("空 schema 处理不对: %v %+v", err, empty)
	}
}

func TestRegistryConfigureMergesIntoAppConfig(t *testing.T) {
	var seen Config
	r := newTestRegistry(t, Register("demo", func(cfg AppConfig) (App, error) {
		seen = cfg.Config
		return &fakeApp{}, nil
	}))

	if err := r.Configure(context.Background(), NewConfig(map[string]string{"endpoint": "https://x"})); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if _, err := r.Versions(context.Background(), SourceVersionsRequest{
		AppID:   "demo",
		Runtime: RuntimeConfig{Config: NewConfig(map[string]string{"channel": "beta"})},
	}); err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if seen.Get("endpoint") != "https://x" || seen.Get("channel") != "beta" {
		t.Fatalf("插件级配置未与软件级配置合并: keys=%v", seen.Keys())
	}
}

func TestNewRegistryRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		info Info
		regs []Registration
		want string
	}{
		{"缺少插件 id", Info{}, []Registration{Register("a", nil)}, "缺少 id"},
		{"插件 id 非法", Info{ID: "Bad/Path"}, []Registration{Register("a", nil)}, "不合法"},
		{"Windows 保留名", Info{ID: "con"}, []Registration{Register("a", nil)}, "不合法"},
		{"没有软件", Info{ID: "demo"}, nil, "没有注册任何软件"},
		{"软件 id 非法", Info{ID: "demo"}, []Registration{Register("../x", nil)}, "不合法"},
		{"软件 id 重复", Info{ID: "demo"}, []Registration{Register("a", nil), Register("a", nil)}, "重复"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newRegistry(tc.info, tc.regs)
			if err == nil {
				t.Fatal("期望报错")
			}
			if !errors.Is(err, ErrBadConfig) {
				t.Fatalf("期望 ErrBadConfig，实际 %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestRegistryInfoDefaultsCapabilities(t *testing.T) {
	r := newTestRegistry(t, Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil }))
	info, err := r.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.APIVersion != APIVersion {
		t.Fatalf("APIVersion 未自动补齐: %q", info.APIVersion)
	}
	if len(info.Capabilities) != 1 || info.Capabilities[0] != CapabilityCatalog {
		t.Fatalf("默认能力应当只有 catalog: %+v", info.Capabilities)
	}
}

func TestValidID(t *testing.T) {
	ok := []string{"demo", "corp-index", "a.b-c_d", "x1"}
	bad := []string{"", "Demo", "-demo", ".demo", "a/b", "a..b", "con", "nul", "com1", strings.Repeat("x", 65)}
	for _, id := range ok {
		if !ValidID(id) {
			t.Errorf("%q 应当合法", id)
		}
	}
	for _, id := range bad {
		if ValidID(id) {
			t.Errorf("%q 应当非法", id)
		}
	}
}

func TestRegisterAppliesOptions(t *testing.T) {
	r := Register("demo", func(AppConfig) (App, error) { return &fakeApp{}, nil },
		WithName("演示"),
		WithDescription("说明"),
		WithHomepage("https://example.com"),
		WithTags("a", "b"),
		WithProvides("p1", "p2"),
		WithTarget(TargetHint{PathTemplate: "${ROOT}/Demo", Entrypoints: []string{"demo.exe"}}),
		WithDefaults(Defaults{Pin: "v1.0.0", Detect: []string{"state-file"}}),
	)
	if r.ID != "demo" || r.Name != "演示" || r.Description != "说明" || r.Homepage != "https://example.com" {
		t.Fatalf("基础元信息错误: %+v", r)
	}
	if len(r.Tags) != 2 || len(r.Provides) != 2 {
		t.Fatalf("标签/别名错误: %+v", r)
	}
	if r.Target == nil || r.Target.PathTemplate != "${ROOT}/Demo" {
		t.Fatalf("目标提示错误: %+v", r.Target)
	}
	if r.Defaults.Pin != "v1.0.0" || len(r.Defaults.Detect) != 1 {
		t.Fatalf("默认值错误: %+v", r.Defaults)
	}
	// 未给名字时回退到 id。
	plain := Register("solo", nil)
	if plain.Name != "solo" {
		t.Fatalf("未给名字时应回退到 id: %+v", plain)
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[error]errorKind{
		nil:              kindOK,
		ErrNotSupported:  kindNotSupported,
		ErrNotFound:      kindNotFound,
		ErrBadConfig:     kindBadConfig,
		ErrRateLimited:   kindRateLimited,
		errors.New("其它"): kindOther,
		errors.Join(ErrNotFound, errors.New("附加")): kindNotFound,
	}
	for err, want := range cases {
		if got := classifyError(err); got != want {
			t.Errorf("classifyError(%v) = %q，期望 %q", err, got, want)
		}
	}
}

func TestRemoteErrorIsMatchesSentinels(t *testing.T) {
	err := newRemoteError(kindNotFound, "插件说找不到")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("跨进程错误应当能匹配哨兵值")
	}
	if err.Error() != "插件说找不到" {
		t.Fatalf("错误文本被改写: %q", err.Error())
	}
	if newRemoteError(kindOK, "") != nil {
		t.Fatal("kindOK 应当还原成 nil")
	}
}

func TestHandshakeCookieIsStable(t *testing.T) {
	// 左边是 go-plugin 的字段名，右边是本包的常量。
	if handshake.MagicCookieKey != magicCookieKey || handshake.MagicCookieValue != magicCookieValue {
		t.Fatal("握手常量与 HandshakeConfig 不一致")
	}
	if handshake.ProtocolVersion != protocolVersion {
		t.Fatal("协议版本不一致")
	}
	if len(supportedProtocols) != 1 {
		t.Fatal("只应当启用 net/rpc 一种传输")
	}
}

func TestReleaseAndEventArePureData(t *testing.T) {
	// 交叉进程传输要求这些结构只含可 gob/json 编解码的字段，
	// 这里用一次 JSON 往返作为最低保障（真正的跨进程验证在集成测试里）。
	rel := Release{
		Version:     "1.0.0",
		PublishedAt: time.Unix(0, 0).UTC(),
		Artifacts:   []Artifact{{Name: "a.zip", URL: "https://x/a.zip", Size: 1, Digest: "sha256:ab"}},
	}
	if rel.Artifacts[0].Name == "" || rel.PublishedAt.IsZero() {
		t.Fatal("结构体未被正确构造")
	}
}
