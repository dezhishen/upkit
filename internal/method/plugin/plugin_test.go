package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

// fakeHost 是最小的插件宿主窄接口实现。
type fakeHost struct {
	takeover bool
	method   core.InstallMethod
	status   core.Status
	err      error
	calls    int
}

func (f *fakeHost) Versions(context.Context, string, string, int) ([]core.Release, error) {
	return nil, nil
}

func (f *fakeHost) Latest(context.Context, string, string) (core.Release, error) {
	return core.Release{}, nil
}

func (f *fakeHost) Method(core.AppRef) (core.InstallMethod, bool) {
	f.calls++
	return f.method, f.takeover
}

func (f *fakeHost) Status(context.Context, core.AppRef) (core.Status, bool, error) {
	f.calls++
	return f.status, f.takeover, f.err
}

// fakeMethod 只用于证明适配器把宿主的实现原样交了出去。
type fakeMethod struct{ core.InstallMethod }

func (fakeMethod) Name() string { return "fake" }

func pluginRef(appID string) core.AppRef {
	return core.AppRef{
		ID:         "corp-index/" + appID,
		Source:     "plugin:corp-index",
		SourceOpts: map[string]string{"app": appID},
	}
}

// 插件接管安装时，适配器把宿主的实现原样交回去 —— 不做任何包装或改写。
func TestNewReturnsHostMethod(t *testing.T) {
	want := fakeMethod{}
	host := &fakeHost{takeover: true, method: want}
	got, err := New(pluginRef("vpn"), registry.Deps{Plugins: host})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if got.Name() != "fake" {
		t.Fatalf("没有拿到宿主的实现: %v", got)
	}
	if host.calls != 1 {
		t.Fatalf("调用次数错误: %d", host.calls)
	}
}

// 插件没接管时应当报错而不是回退：清单里写了这个软件却没有安装器，
// 静默回退会让用户以为装上了别的东西。
func TestNewFailsWhenPluginDoesNotTakeOver(t *testing.T) {
	host := &fakeHost{takeover: false}
	_, err := New(pluginRef("vpn"), registry.Deps{Plugins: host})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("期望 ErrUnsupported，实际 %v", err)
	}
}

// 宿主未启用时给出明确的错误，而不是 nil 指针崩溃。
func TestNewWithoutHost(t *testing.T) {
	_, err := New(pluginRef("vpn"), registry.Deps{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("期望 ErrUnsupported，实际 %v", err)
	}
}
