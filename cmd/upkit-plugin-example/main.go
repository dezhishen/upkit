// Command upkit-plugin-example 是一个最小的 catalog 模式插件示例。
//
// 它演示了写一个 upkit 插件的全部动作：
//
//  1. 为每个软件写一个构造器（AppFactory）；
//  2. 用 plugin.Register 注册该软件，并附上元信息；
//  3. 用 plugin.Serve 把注册表交给宿主。
//
// 插件只负责回答「有哪些软件、有哪些版本、下载什么」；下载、解包、落地、版本探测
// 全部复用宿主内置的四条轴，因此这里不需要任何安装逻辑。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/dezhishen/upkit/pkg/plugin"
)

// 本插件私有的配置键（同时用于构造器、ConfigSchema 与建议默认值），
// 以及通道取值。写成常量而不是散落字面量，避免改名时漏改。
const (
	cfgDownloadBase = "download_base"
	cfgChannel      = "channel"

	channelStable = "stable"
	channelBeta   = "beta"

	defaultMirror = "https://mirror.example.com/vpn"
)

// staticApp 是示例用的软件实现：版本来自一个写死的列表。
type staticApp struct {
	cfg      plugin.AppConfig
	id       string
	base     string
	versions []string
}

// corpVPNConfig 是本插件的配置结构体。
//
// 插件作者不需要接触 map：声明结构体 + json tag，用 plugin.DecodeConfig 一次解出来
// （SDK 内部把配置转成 JSON 再解码）。
type corpVPNConfig struct {
	DownloadBase string `json:"download_base"`
	Channel      string `json:"channel"`
}

// newCorpVPN 是「公司 VPN」的构造器。
//
// 构造器只在宿主首次需要该软件时调用一次；返回错误表示这个软件当前不可用，
// 只影响它自己，插件里的其它软件照常工作。
func newCorpVPN(cfg plugin.AppConfig) (plugin.App, error) {
	var c corpVPNConfig
	if err := plugin.DecodeConfig(cfg.Config, &c); err != nil {
		return nil, err
	}
	if c.DownloadBase == "" {
		return nil, fmt.Errorf("%w: 请先配置 download_base（内网镜像地址）", plugin.ErrBadConfig)
	}
	if c.Channel == "" {
		c.Channel = channelStable
	}
	cfg.Log.Info("构造公司 VPN", "base", c.DownloadBase, "channel", c.Channel, "data", cfg.DataDir)
	return &staticApp{
		cfg:      cfg,
		id:       "corp-vpn",
		base:     c.DownloadBase,
		versions: []string{"1.4.3", "1.4.2", "1.4.1"},
	}, nil
}

// newLegacyCRM 是「旧版 CRM」的构造器。它不需要任何配置。
func newLegacyCRM(cfg plugin.AppConfig) (plugin.App, error) {
	return &staticApp{
		cfg:      cfg,
		id:       "legacy-crm",
		base:     "https://mirror.example.com/crm",
		versions: []string{"2.0.0", "1.9.7"},
	}, nil
}

// Versions 是 catalog 模式下唯一必需的方法。
func (a *staticApp) Versions(ctx context.Context, req plugin.VersionsRequest) ([]plugin.Release, error) {
	limit := req.Limit
	if limit <= 0 || limit > len(a.versions) {
		limit = len(a.versions)
	}
	out := make([]plugin.Release, 0, limit)
	for i := 0; i < limit; i++ {
		v := a.versions[i]
		name := fmt.Sprintf("%s-%s-x64.zip", a.id, v)
		out = append(out, plugin.Release{
			Version:     v,
			Tag:         "v" + v,
			Channel:     "stable",
			PublishedAt: time.Now().Add(-time.Duration(i) * 24 * time.Hour),
			Notes:       "示例发布说明 " + v,
			Artifacts: []plugin.Artifact{{
				Name: name,
				URL:  a.base + "/" + v + "/" + name,
				Size: 1 << 20,
			}},
		})
	}
	return out, nil
}

// version 是插件自身的版本号，由构建脚本用 -ldflags -X main.version 注入。
var version = "dev"

func main() {
	plugin.Serve(plugin.Info{
		ID:          "example-static",
		Name:        "示例静态源",
		Version:     version,
		Vendor:      "upkit",
		Description: "从静态列表提供软件与版本，演示 catalog 模式插件的最小写法",
	},
		plugin.Register("corp-vpn", newCorpVPN,
			plugin.WithName("公司 VPN"),
			plugin.WithDescription("内网镜像分发的 VPN 客户端"),
			plugin.WithProvides("corp-vpn", "example/corp-vpn"),
			plugin.WithTarget(plugin.TargetHint{
				PathTemplate: "${ROOT}/CorpVPN",
				Entrypoints:  []string{"vpn.exe"},
			}),
			// 建议的落地方式与安装目标（结构化字段，不需要构造 map）。
			plugin.WithDefaults(plugin.Defaults{
				Method: "portable-inplace",
				Unpack: "zip",
				Install: plugin.InstallDefaults{
					Path:        "${ROOT}/CorpVPN",
					Entrypoints: []string{"vpn.exe"},
					Processes:   []string{"vpn.exe"},
				},
			}),
			plugin.WithConfigSchema(plugin.ConfigSchema{
				Title: "公司 VPN",
				Fields: []plugin.ConfigField{
					{Key: cfgDownloadBase, Label: "内网镜像地址", Type: plugin.FieldString, Required: true, Default: defaultMirror},
					{Key: cfgChannel, Label: "通道", Type: plugin.FieldEnum, Enum: []string{channelStable, channelBeta}, Default: channelStable},
				},
			}),
		),
		plugin.Register("legacy-crm", newLegacyCRM,
			plugin.WithName("旧版 CRM"),
			plugin.WithDescription("只在少数办公机上安装"),
			plugin.WithTags("办公"),
		),
		// full 模式：这个软件的安装由插件自己完成（见 full.go）。
		// 只声明一个 Method 就够了，宿主会自动把探测也指向插件 ——
		// 只有它知道文件被放到哪了。
		plugin.Register("local-stub", newLocalStub,
			plugin.WithName("本地存根"),
			plugin.WithDescription("演示 full 模式：插件自己探测状态并执行安装"),
			plugin.WithDefaults(plugin.Defaults{Method: plugin.MethodPlugin}),
			plugin.WithConfigSchema(plugin.ConfigSchema{
				Title: "本地存根",
				Fields: []plugin.ConfigField{
					{Key: cfgStubVersion, Label: "版本", Type: plugin.FieldString, Default: stubDefaultVersion},
					{Key: cfgStubFail, Label: "总是失败", Type: plugin.FieldBool, Help: "打开后安装必定失败，用于演示错误处理"},
					{Key: cfgStubDelayMS, Label: "模拟耗时(ms)", Type: plugin.FieldInt, Default: "0"},
				},
			}),
		),
	)
}
