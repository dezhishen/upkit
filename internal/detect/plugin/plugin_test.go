package plugin

import (
	"context"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

type fakeHost struct {
	takeover bool
	status   core.Status
	gotRef   core.AppRef
}

func (f *fakeHost) Versions(context.Context, string, string, int) ([]core.Release, error) {
	return nil, nil
}

func (f *fakeHost) Latest(context.Context, string, string) (core.Release, error) {
	return core.Release{}, nil
}

func (f *fakeHost) Method(core.AppRef) (core.InstallMethod, bool) { return nil, f.takeover }

func (f *fakeHost) Status(_ context.Context, ref core.AppRef) (core.Status, bool, error) {
	f.gotRef = ref
	return f.status, f.takeover, nil
}

func ref(appID string) core.AppRef {
	return core.AppRef{
		ID:          "corp-index/" + appID,
		Source:      "plugin:corp-index",
		SourceOpts:  map[string]string{"app": appID},
		InstallPath: "/opt/" + appID,
	}
}

// 探测器必须用「本次请求的」条目去问插件：用户可能刚改过安装路径。
func TestDetectPassesCurrentRef(t *testing.T) {
	host := &fakeHost{takeover: true, status: core.Status{Installed: true, Version: "1.2.3"}}
	d, err := New(ref("vpn"), registry.Deps{Plugins: host})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if d.Name() != Kind+":plugin:corp-index" {
		t.Fatalf("探测器名错误: %q", d.Name())
	}

	changed := ref("vpn")
	changed.InstallPath = "/opt/moved"
	st, err := d.Detect(context.Background(), changed)
	if err != nil {
		t.Fatalf("Detect 失败: %v", err)
	}
	if !st.Installed || st.Version != "1.2.3" {
		t.Fatalf("状态翻译错误: %+v", st)
	}
	if host.gotRef.InstallPath != "/opt/moved" {
		t.Fatalf("没有把最新路径传给插件: %q", host.gotRef.InstallPath)
	}
}

// 插件没接管安装时视为未安装，且不报错 —— 探测链靠这个约定继续往下走。
func TestDetectWithoutTakeover(t *testing.T) {
	host := &fakeHost{takeover: false}
	d, err := New(ref("vpn"), registry.Deps{Plugins: host})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	st, err := d.Detect(context.Background(), ref("vpn"))
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if st.Installed {
		t.Fatalf("不应报告已安装: %+v", st)
	}
}

// 宿主未启用时构造失败，避免探测阶段才崩。
func TestNewWithoutHost(t *testing.T) {
	if _, err := New(ref("vpn"), registry.Deps{}); err == nil {
		t.Fatal("宿主未启用时应当报错")
	}
}
