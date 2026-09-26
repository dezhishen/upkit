package plugin_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dezhishen/upkit/pkg/plugin"
)

// 本文件是跨进程的端到端测试：真的编译出示例插件可执行文件，真的以子进程方式启动，
// 验证「引入一个 package + 注册方法 + 单一软件构造器」这条链路在生产形态下可用。

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

func examplePlugin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "upkit-plugin-example-*")
		if err != nil {
			buildErr = err
			return
		}
		name := "example-plugin"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "github.com/dezhishen/upkit/cmd/upkit-plugin-example")
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("编译示例插件失败: %v\n%s", err, b)
			return
		}
		builtBin = out
	})
	if buildErr != nil {
		t.Skipf("无法准备示例插件: %v", buildErr)
	}
	return builtBin
}

func startExample(t *testing.T) *plugin.Client {
	t.Helper()
	c, err := plugin.NewClient(plugin.ClientConfig{
		Exec:    examplePlugin(t),
		Timeout: 30 * time.Second,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	t.Cleanup(c.Kill)
	return c
}

func TestClientHandshakeAndInfo(t *testing.T) {
	c := startExample(t)

	info := c.Info()
	if info.ID != "example-static" {
		t.Fatalf("插件 id 不对: %q", info.ID)
	}
	if info.APIVersion != plugin.APIVersion {
		t.Fatalf("协议版本不对: %q", info.APIVersion)
	}
	if info.Name == "" || info.Version == "" {
		t.Fatalf("插件元信息不完整: %+v", info)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
}

func TestClientListReturnsRegisteredSoftware(t *testing.T) {
	c := startExample(t)

	list, err := c.Source().List(context.Background())
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("期望 3 个软件，实际 %d: %+v", len(list), list)
	}

	byID := map[string]plugin.Software{}
	for _, sw := range list {
		byID[sw.ID] = sw
	}

	vpn, ok := byID["corp-vpn"]
	if !ok {
		t.Fatalf("缺少 corp-vpn: %+v", list)
	}
	if vpn.Name != "公司 VPN" {
		t.Fatalf("注册项的名字没有跨进程保留: %q", vpn.Name)
	}
	if len(vpn.Provides) != 2 {
		t.Fatalf("软身份没有跨进程保留: %+v", vpn.Provides)
	}
	if vpn.Target == nil || vpn.Target.PathTemplate != "${ROOT}/CorpVPN" {
		t.Fatalf("安装目标提示没有跨进程保留: %+v", vpn.Target)
	}
	if vpn.Target != nil && len(vpn.Target.Entrypoints) != 1 {
		t.Fatalf("入口文件没有跨进程保留: %+v", vpn.Target.Entrypoints)
	}
	if vpn.Defaults.Install.Path != "${ROOT}/CorpVPN" {
		t.Fatalf("建议默认值没有跨进程保留: %+v", vpn.Defaults)
	}
	if len(vpn.Defaults.Install.Entrypoints) != 1 || vpn.Defaults.Install.Processes[0] != "vpn.exe" {
		t.Fatalf("建议安装目标没有跨进程保留: %+v", vpn.Defaults.Install)
	}
}

func TestClientVersionsRoundTrip(t *testing.T) {
	c := startExample(t)

	rels, err := c.Source().Versions(context.Background(), plugin.SourceVersionsRequest{
		AppID: "legacy-crm",
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if len(rels) != 1 || rels[0].Version != "2.0.0" {
		t.Fatalf("版本结果不对: %+v", rels)
	}
	if len(rels[0].Artifacts) != 1 || !strings.HasPrefix(rels[0].Artifacts[0].URL, "https://") {
		t.Fatalf("产物信息未跨进程保留: %+v", rels[0].Artifacts)
	}
	if rels[0].PublishedAt.IsZero() {
		t.Fatal("时间字段未跨进程保留（time.Time 编解码有问题）")
	}

	all, err := c.Source().Versions(context.Background(), plugin.SourceVersionsRequest{AppID: "legacy-crm"})
	if err != nil {
		t.Fatalf("Versions(不限) 失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("期望 2 个版本，实际 %d", len(all))
	}
}

// 构造器返回的错误必须原样穿透进程边界，并保留语义分类与文本。
func TestClientFactoryErrorPropagates(t *testing.T) {
	c := startExample(t)

	_, err := c.Source().Versions(context.Background(), plugin.SourceVersionsRequest{
		AppID: "corp-vpn", // 该软件的构造器要求 download_base
	})
	if err == nil {
		t.Fatal("缺少必填配置的软件应当报错")
	}
	if !errors.Is(err, plugin.ErrBadConfig) {
		t.Fatalf("错误分类丢失，期望 ErrBadConfig，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "download_base") {
		t.Fatalf("错误文本没有跨进程透传: %v", err)
	}
}

func TestClientUnknownSoftwareIsNotFound(t *testing.T) {
	c := startExample(t)

	_, err := c.Source().Versions(context.Background(), plugin.SourceVersionsRequest{AppID: "does-not-exist"})
	if !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}

// catalog 插件不该被当成 full 插件使用；宿主据此回退到内置四轴。
func TestClientCatalogPluginRejectsFullMode(t *testing.T) {
	c := startExample(t)

	if _, err := c.Source().Status(context.Background(), plugin.SourceAppRequest{AppID: "legacy-crm"}); !errors.Is(err, plugin.ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported，实际 %v", err)
	}
}

func TestClientContextTimeoutIsHonored(t *testing.T) {
	c := startExample(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, err := c.Source().Versions(ctx, plugin.SourceVersionsRequest{AppID: "legacy-crm"})
	if err == nil {
		t.Fatal("超时的调用应当返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 DeadlineExceeded，实际 %v", err)
	}
}

func TestClientStaticConfigSchema(t *testing.T) {
	c := startExample(t)

	schema, err := c.Source().ConfigSchema(context.Background())
	if err != nil {
		t.Fatalf("ConfigSchema 失败: %v", err)
	}
	if schema.Title != "公司 VPN" || len(schema.Fields) != 2 {
		t.Fatalf("schema 未跨进程保留: %+v", schema)
	}
	if !schema.Fields[0].Required || schema.Fields[1].Type != plugin.FieldEnum || len(schema.Fields[1].Enum) != 2 {
		t.Fatalf("字段定义不完整: %+v", schema.Fields)
	}
}

// 插件可执行文件被直接运行时，必须明确报错退出，而不是静默挂起。
func TestDirectExecutionIsRejected(t *testing.T) {
	bin := examplePlugin(t)

	cmd := exec.Command(bin)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("直接运行插件不应成功退出；stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("直接运行插件时挂起了（应当立即报错退出）")
	}

	combined := stderr.String() + stdout.String()
	if !strings.Contains(strings.ToLower(combined), "plugin") {
		t.Fatalf("退出提示应当说明这是插件：%q", combined)
	}
}

// Kill 之后子进程必须消失，避免宿主退出后残留。
func TestClientKillTerminatesChildProcess(t *testing.T) {
	c, err := plugin.NewClient(plugin.ClientConfig{
		Exec:    examplePlugin(t),
		Timeout: 30 * time.Second,
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}

	c.Kill()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Ping(context.Background()); err != nil {
			return // RPC 已断开，说明进程确实结束了
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Kill 之后插件仍然响应 RPC")
}
