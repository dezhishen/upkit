package apps

import (
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
)

// 限定标识的拼法只在这里定义一次，三条轴的插件适配器共用（见 registry 的注释）。
func TestParsePluginRef(t *testing.T) {
	cases := []struct {
		name string
		app  core.AppRef
		want PluginRef
		ok   bool
	}{
		{
			name: "优先用显式声明的 source.app",
			app:  core.AppRef{ID: "corp/vpn", Source: "plugin:corp", SourceOpts: map[string]string{SourceKeyApp: "vpn"}},
			want: PluginRef{SourceID: "corp", AppID: "vpn"},
			ok:   true,
		},
		{
			name: "没有 source.app 时按限定 ID 拆分",
			app:  core.AppRef{ID: "corp/vpn", Source: "plugin:corp"},
			want: PluginRef{SourceID: "corp", AppID: "vpn"},
			ok:   true,
		},
		{
			name: "显式声明与限定 ID 不一致时以显式声明为准",
			app:  core.AppRef{ID: "corp/other", Source: "plugin:corp", SourceOpts: map[string]string{SourceKeyApp: "vpn"}},
			want: PluginRef{SourceID: "corp", AppID: "vpn"},
			ok:   true,
		},
		{
			name: "非插件来源",
			app:  core.AppRef{ID: "x", Source: "github-release"},
			ok:   false,
		},
		{
			name: "空来源",
			app:  core.AppRef{ID: "x"},
			ok:   false,
		},
		{
			name: "来源 ID 为空",
			app:  core.AppRef{ID: "a", Source: SourceKindPluginPrefix},
			ok:   false,
		},
		{
			name: "软件 ID 缺失且无法从限定 ID 推导",
			app:  core.AppRef{ID: "corp", Source: "plugin:corp"},
			ok:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePluginRef(c.app)
			if c.ok {
				if err != nil {
					t.Fatalf("不应报错: %v", err)
				}
				if got != c.want {
					t.Fatalf("解析结果 = %+v，期望 %+v", got, c.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("应当报错，实际得到 %+v", got)
			}
			// 非插件来源要能靠 errors.Is 识别出来，方便适配器给出统一的提示。
			if strings.HasPrefix(c.app.Source, SourceKindPluginPrefix) {
				if !strings.Contains(err.Error(), "缺少来源 ID 或软件 ID") {
					t.Fatalf("错误信息不够明确: %v", err)
				}
			}
		})
	}
}
