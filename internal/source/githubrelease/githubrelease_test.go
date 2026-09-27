package githubrelease

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/github"
	"github.com/dezhishen/upkit/internal/registry"
)

// rewriteTransport 把发往 api.github.com 的请求改到本地测试服务上。
//
// 适配器自己构造 Client（只认 https://api.github.com），不提供 API 基地址的注入点 ——
// 这是有意的：正式实现就该只跟 GitHub 说话。测试因此从「网络出口」这一层做重定向，
// 拿到的是与真实链路一模一样的代码路径（含 URL 拼接、请求头、JSON 解码）。
type rewriteTransport struct {
	base http.RoundTripper
	host string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" {
		return t.base.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = t.host
	out := req.Clone(req.Context())
	out.URL = &u
	out.Host = t.host
	return t.base.RoundTrip(out)
}

// fakeAPI 是一个可控的 GitHub API 假服务。
type fakeAPI struct {
	t      *testing.T
	routes map[string]func(http.ResponseWriter, *http.Request)
	hits   []string
	srv    *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	a := &fakeAPI{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits = append(a.hits, r.URL.Path+"?"+r.URL.RawQuery)
		if h, ok := a.routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

// json 注册一个返回 JSON 的路由。
func (a *fakeAPI) json(path string, v any) {
	a.routes[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// text 注册一个返回纯文本的路由。
func (a *fakeAPI) text(path, body string) {
	a.routes[path] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}
}

// status 注册一个固定状态码的路由。
func (a *fakeAPI) status(path string, code int) {
	a.routes[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

// newSource 用假 API 造一个适配器。
func (a *fakeAPI) newSource(t *testing.T, app core.AppRef) core.SourceResolver {
	t.Helper()
	if app.SourceOpts == nil {
		app.SourceOpts = map[string]string{"repo": "owner/repo"}
	}
	hc := &http.Client{Transport: &rewriteTransport{
		base: a.srv.Client().Transport,
		host: strings.TrimPrefix(a.srv.URL, "http://"),
	}}
	src, err := New(app, registry.Deps{HTTP: hc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return src
}

func asset(name, digest string) github.Asset {
	return github.Asset{
		Name: name, Size: 1024, Digest: digest,
		DownloadURL: "https://api.github.com/assets/" + name,
	}
}

func rel(tag string, assets ...github.Asset) github.Release {
	return github.Release{
		TagName: tag, Name: tag, Body: "更新说明",
		PublishedAt: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
		HTMLURL:     "https://github.com/owner/repo/releases/tag/" + tag,
		Assets:      assets,
	}
}

func zipName() string {
	token := archToken("")
	return "app_windows_" + token + ".zip"
}

// 没写 repo 时必须立刻报错，并点明是哪个软件的配置缺了它。
func TestNewRequiresRepo(t *testing.T) {
	_, err := New(core.AppRef{ID: "demo", SourceOpts: map[string]string{}}, registry.Deps{})
	if err == nil || !strings.Contains(err.Error(), "source.repo") || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("缺少 repo 时应报错并点名，实际 %v", err)
	}
	if _, err := New(core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "  "}}, registry.Deps{}); err == nil {
		t.Fatal("纯空白的 repo 也该报错")
	}
}

// 默认走 latest：按 {arch} 选资源，版本号取自 tag，元信息原样带过去。
func TestLatestFromLatestRelease(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("v131.0.6778.86-1.1",
		asset("app_2.1.0_windows_x86.zip", ""),
		asset("app_2.1.0_windows_x64.zip", "sha256:3a7bd3e2"),
		asset("app_2.1.0.zip", ""), // 不带平台后缀的，不该被选中
	))

	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": "app_*_windows_{arch}.zip",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Version != "131.0.6778.86-1.1" || got.Tag != "v131.0.6778.86-1.1" {
		t.Fatalf("版本解析不对: %+v", got)
	}
	if got.Channel != "stable" || got.Notes != "更新说明" {
		t.Fatalf("元信息不对: %+v", got)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("应只有一个产物: %+v", got.Artifacts)
	}
	a := got.Artifacts[0]
	if a.Name != "app_2.1.0_windows_x64.zip" || a.Size != 1024 || a.Digest != "sha256:3a7bd3e2" {
		t.Fatalf("产物不对: %+v", a)
	}
	if len(api.hits) != 1 || !strings.Contains(api.hits[0], "/releases/latest") {
		t.Fatalf("应只请求 latest 一次: %v", api.hits)
	}
}

// 没配 asset 时用内置的默认模式（ungoogled-chromium 的发布命名）。
func TestDefaultAssetPattern(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("131.0.6778.86-1.1",
		asset("ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip", ""),
	))
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !strings.Contains(got.Artifacts[0].Name, "ungoogled-chromium_") {
		t.Fatalf("默认模式没生效: %+v", got.Artifacts[0])
	}
	// 版本号从资源名里解析（tag 上没有版本号时）。
	if got.Version != "131.0.6778.86-1.1" {
		t.Fatalf("版本解析不对: %q", got.Version)
	}
}

// 预发布开关打开时不去查 latest（GitHub 的 latest 接口本来就排除预发布），
// 而是列版本取第一条非草稿。
func TestLatestPrereleaseListsReleases(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases", []github.Release{
		{TagName: "v2.0.0-rc.1", Draft: true, Assets: []github.Asset{asset("x_windows_x64.zip", "")}},
		{TagName: "v2.0.0-rc.1", Prerelease: true, Assets: []github.Asset{asset(zipName(), "")}},
	})

	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "prerelease": "是",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Channel != "prerelease" {
		t.Fatalf("应标为预发布: %+v", got)
	}
	if !strings.Contains(api.hits[0], "per_page=20") {
		t.Fatalf("应走列表接口: %v", api.hits)
	}
}

// 指定版本（app.pin 或 source.version）时按 tag 取，不再看一眼 latest。
func TestLatestWithPinAndVersionOpt(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/tags/v1.2.3", rel("v1.2.3", asset(zipName(), "")))

	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	got, err := src.Latest(context.Background(), core.AppRef{Pin: "v1.2.3"})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("版本不对: %+v", got)
	}
	for _, h := range api.hits {
		if strings.Contains(h, "/releases/latest") {
			t.Fatalf("固定版本时不该查 latest: %v", api.hits)
		}
	}

	// source.version 与 pin 等价。
	api2 := newFakeAPI(t)
	api2.json("/repos/owner/repo/releases/tags/v9.9.9", rel("v9.9.9", asset(zipName(), "")))
	src2 := api2.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "version": "v9.9.9",
	}})
	if got, err := src2.Latest(context.Background(), core.AppRef{}); err != nil || got.Version != "9.9.9" {
		t.Fatalf("source.version 应生效: %+v %v", got, err)
	}
}

// prefer_release 是「优先取某个 tag，取不到再退回 latest」：镜像站/回滚场景用得到。
func TestLatestPreferReleaseFallsBack(t *testing.T) {
	api := newFakeAPI(t)
	api.status("/repos/owner/repo/releases/tags/v1.0.0", http.StatusNotFound)
	api.json("/repos/owner/repo/releases/latest", rel("v1.5.0", asset(zipName(), "")))

	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "prefer_release": "v1.0.0",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Version != "1.5.0" {
		t.Fatalf("应退回 latest: %+v", got)
	}
	if len(api.hits) != 2 {
		t.Fatalf("应先试 prefer_release 再试 latest: %v", api.hits)
	}
}

// 取不到版本时必须报错，不能说「没有更新」—— 那会让用户以为已经是最新。
func TestLatestErrorPropagates(t *testing.T) {
	api := newFakeAPI(t)
	api.status("/repos/owner/repo/releases/latest", http.StatusInternalServerError)
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	if _, err := src.Latest(context.Background(), core.AppRef{}); err == nil {
		t.Fatal("上游出错时应报错")
	}

	api.json("/repos/owner/repo/releases/tags/v1.2.3", rel("", asset(zipName(), "")))
	if _, err := src.Latest(context.Background(), core.AppRef{Pin: "v1.2.3"}); err == nil {
		t.Fatal("tag 里没有版本号时应报错")
	}
}

// 资源匹配不上：单个候选时直接用，多个候选时明确列出候选名。
func TestConvertAssetSelection(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("v1.0.0",
		asset("only_windows_x64.zip", ""),
		asset("notes.txt", ""), // 非 zip 不参与筛选
	))
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": "完全不匹配的名字.zip",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("只有一个 zip 候选时应直接用它: %v", err)
	}
	if got.Artifacts[0].Name != "only_windows_x64.zip" {
		t.Fatalf("选错资源: %+v", got.Artifacts[0])
	}

	api2 := newFakeAPI(t)
	api2.json("/repos/owner/repo/releases/latest", rel("v1.0.0",
		asset("a_windows_x64.zip", ""), asset("b_windows_x64.zip", ""),
	))
	src2 := api2.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": "c_*.zip",
	}})
	if _, err := src2.Latest(context.Background(), core.AppRef{}); err == nil ||
		!strings.Contains(err.Error(), "a_windows_x64.zip") || !strings.Contains(err.Error(), "b_windows_x64.zip") {
		t.Fatalf("多个候选时应列出候选名: %v", err)
	}

	// 没有 zip 时也要说清楚。
	api3 := newFakeAPI(t)
	api3.json("/repos/owner/repo/releases/latest", rel("v1.0.0", asset("notes.txt", "")))
	src3 := api3.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	if _, err := src3.Latest(context.Background(), core.AppRef{}); err == nil ||
		!strings.Contains(err.Error(), "zip") {
		t.Fatalf("没有 zip 资源时应报错: %v", err)
	}
}

// 资源名匹配大小写不敏感，且 archive 里的 `*` 通配要能用。
func TestConvertAssetPatternCaseInsensitiveAndWildcard(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("v1.0.0",
		asset("App_Windows_x64.zip", ""),
		asset("App_Windows_arm64.zip", ""),
	))
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": "app_windows_x64.zip",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("精确名匹配应忽略大小写: %v", err)
	}
	if got.Artifacts[0].Name != "App_Windows_x64.zip" {
		t.Fatalf("选错资源: %+v", got.Artifacts[0])
	}
}

// 列版本：转不过去的（没有版本号/没有匹配资源）直接跳过，而不是整批失败。
func TestVersionsSkipsUnconvertible(t *testing.T) {
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases", []github.Release{
		rel("", asset(zipName(), "")),                                               // 解析不出版本号
		rel("v1.0.0", asset("notes.txt", "")),                                       // 没有 zip
		rel("v1.1.0", asset(zipName(), "")),                                         // 正常
		rel("v1.2.0", asset("app_windows_x64.zip", ""), asset("app.zip", "")),       // 匹配不上且多候选
		rel("v1.3.0", asset("app_windows_x64.zip", ""), asset("app_arm64.zip", "")), // 正常
	})
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": zipName(),
	}})
	got, err := src.Versions(context.Background(), core.AppRef{}, 5)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应跳过 2 条，实际得到 %d 条: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Version == "" || len(r.Artifacts) != 1 {
			t.Fatalf("转换结果不完整: %+v", r)
		}
	}
	// limit 要透传给 API（per_page）。
	if !strings.Contains(api.hits[0], "per_page=5") {
		t.Fatalf("per_page 没透传: %v", api.hits)
	}

	api.status("/repos/owner/repo/releases", http.StatusBadGateway)
	if _, err := src.Versions(context.Background(), core.AppRef{}, 5); err == nil {
		t.Fatal("上游出错时应报错")
	}
}

// 校验值来源优先级：GitHub 自带的 digest > 同目录的 .sha256 附件。
func TestExpectedDigest(t *testing.T) {
	ctx := context.Background()
	sum := strings.Repeat("ab", 32)

	// 一、自带 digest：直接返回，不用再发请求。
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("v1.0.0", asset(zipName(), "sha256:"+sum)))
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	got, err := src.(core.Verifier).ExpectedDigest(ctx, core.AppRef{},
		core.Artifact{Name: zipName(), Digest: "sha256:" + sum})
	if err != nil {
		t.Fatalf("ExpectedDigest: %v", err)
	}
	if got != sum {
		t.Fatalf("摘要不对: %q", got)
	}
	if len(api.hits) != 0 {
		t.Fatalf("自带 digest 时不该发请求: %v", api.hits)
	}

	// 二、没有 digest、但有 .sha256 附件：下载它并解析。
	api2 := newFakeAPI(t)
	chkName := zipName() + ".sha256"
	chkAsset := asset(chkName, "")
	chkAsset.DownloadURL = "https://api.github.com/checksums/" + chkName
	api2.json("/repos/owner/repo/releases/latest", rel("v1.0.0", asset(zipName(), ""), chkAsset))
	api2.text("/checksums/"+chkName, sum+"  "+zipName()+"\n")
	src2 := api2.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	got2, err := src2.(core.Verifier).ExpectedDigest(ctx, core.AppRef{}, core.Artifact{Name: zipName()})
	if err != nil {
		t.Fatalf("ExpectedDigest: %v", err)
	}
	if got2 != sum {
		t.Fatalf("应取到附件里的摘要: %q", got2)
	}

	// 三、附件里没有摘要（或没有附件）：返回空串表示「不校验」，而不是报错。
	api3 := newFakeAPI(t)
	api3.json("/repos/owner/repo/releases/latest", rel("v1.0.0", asset(zipName(), "")))
	src3 := api3.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	if got, err := src3.(core.Verifier).ExpectedDigest(ctx, core.AppRef{}, core.Artifact{Name: zipName()}); err != nil || got != "" {
		t.Fatalf("没有校验附件时应返回空串: %q %v", got, err)
	}

	// 四、产物名在 Release 里找不到：同样返回空串。
	if got, err := src2.(core.Verifier).ExpectedDigest(ctx, core.AppRef{}, core.Artifact{Name: "ghost.zip"}); err != nil || got != "" {
		t.Fatalf("产物不存在时应返回空串: %q %v", got, err)
	}
}

// 固定版本的软件查校验值时也要按 pin 取对应的 Release，不能拿 latest 的摘要去校验旧版本。
func TestExpectedDigestHonoursPin(t *testing.T) {
	api := newFakeAPI(t)
	chkName := zipName() + ".sha256"
	chkAsset := asset(chkName, "")
	chkAsset.DownloadURL = "https://api.github.com/checksums/" + chkName
	api.json("/repos/owner/repo/releases/tags/v1.2.3", rel("v1.2.3", asset(zipName(), ""), chkAsset))
	api.text("/checksums/"+chkName, strings.Repeat("cd", 32)+"  "+zipName()+"\n")
	api.json("/repos/owner/repo/releases/latest", rel("v9.0.0", asset(zipName(), "sha256:"+strings.Repeat("ee", 32))))

	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{"repo": "owner/repo"}})
	got, err := src.(core.Verifier).ExpectedDigest(context.Background(), core.AppRef{Pin: "v1.2.3"}, core.Artifact{Name: zipName()})
	if err != nil {
		t.Fatalf("ExpectedDigest: %v", err)
	}
	if got != strings.Repeat("cd", 32) {
		t.Fatalf("应按 pin 取校验值，实际 %q", got)
	}
	for _, h := range api.hits {
		if strings.Contains(h, "/releases/latest") {
			t.Fatalf("固定版本时不该查 latest: %v", api.hits)
		}
	}
}

// 摘要串的解析：去掉 `sha256:` 这类算法前缀，统一小写。
func TestSumFromDigest(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"   ":             "",
		"sha256:ABC":      "abc",
		"  SHA256: AbC  ": "abc",
		"abc":             "abc",
		"no-algo-prefix":  "no-algo-prefix",
		"sha256:aa:bb":    "aa:bb",
	}
	for in, want := range cases {
		if got := sumFromDigest(in); got != want {
			t.Fatalf("sumFromDigest(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 架构令牌：auto 按运行平台推，显式值接受常见别名，认不出来的原样返回。
func TestArchToken(t *testing.T) {
	auto := map[string]string{"amd64": "x64", "386": "x86", "arm64": "arm64"}[runtime.GOARCH]
	if auto != "" {
		for _, v := range []string{"", "auto", "AUTO", "  "} {
			if got := archToken(v); got != auto {
				t.Fatalf("archToken(%q) = %q，期望 %q", v, got, auto)
			}
		}
	}
	cases := map[string]string{
		"amd64": "x64", "x86_64": "x64", "X64": "x64",
		"386": "x86", "i386": "x86", "x86": "x86",
		"aarch64": "arm64", "ARM64": "arm64",
		"riscv": "riscv",
	}
	for in, want := range cases {
		if got := archToken(in); got != want {
			t.Fatalf("archToken(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 自定义 asset 里的 {arch} 要按配置替换。
	api := newFakeAPI(t)
	api.json("/repos/owner/repo/releases/latest", rel("v1.0.0", asset("tool_x86.zip", "")))
	src := api.newSource(t, core.AppRef{ID: "demo", SourceOpts: map[string]string{
		"repo": "owner/repo", "asset": "tool_{arch}.zip", "arch": "386",
	}})
	got, err := src.Latest(context.Background(), core.AppRef{})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Artifacts[0].Name != "tool_x86.zip" {
		t.Fatalf("架构没替换: %+v", got.Artifacts[0])
	}
}

// 名字与真值开关：先钉住契约，避免以后改配置写法时漏改。
func TestNameAndTruthy(t *testing.T) {
	api := newFakeAPI(t)
	src := api.newSource(t, core.AppRef{ID: "demo"})
	if src.Name() != Name || Name != "github-release" {
		t.Fatalf("注册名不对: %q %q", src.Name(), Name)
	}
	for _, v := range []string{"1", "true", "TRUE", "Yes", " on ", "是"} {
		if !truthy(v) {
			t.Fatalf("truthy(%q) 应为真", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "随便"} {
		if truthy(v) {
			t.Fatalf("truthy(%q) 应为假", v)
		}
	}
	if got := firstNonEmpty(" ", "", "b", "c"); got != "b" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty(" \t "); got != "" {
		t.Fatalf("全空白应返回空串，实际 %q", got)
	}
}
