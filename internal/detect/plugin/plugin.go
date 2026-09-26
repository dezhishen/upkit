// Package plugin 把插件自报的本机状态接入「版本探测」这条轴。
//
// full 模式下只有插件自己知道装在哪、装的哪个版本（它可能就是往注册表或别处写），
// 所以插件声明 CapabilityFull 时，探测链默认只有这一环。
package plugin

import (
	"context"
	"fmt"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/registry"
)

// Kind 是该适配器在清单里的名字（detect: plugin）。
const Kind = pluginhost.KindPlugin

// New 构造插件探测器。
func New(app core.AppRef, deps registry.Deps) (core.Detector, error) {
	if deps.Plugins == nil {
		return nil, fmt.Errorf("%w: 插件宿主未启用，软件 %s 需要它", core.ErrUnsupported, app.ID)
	}
	return &detector{host: deps.Plugins, name: Kind + ":" + app.Source}, nil
}

type detector struct {
	host registry.PluginHost
	name string
}

var _ core.Detector = (*detector)(nil)

// Name 返回探测器名，形如 plugin:plugin:corp-index。
func (d *detector) Name() string { return d.name }

// Detect 询问插件本机状态；插件未接管安装时视为未安装。
//
// 这里用传进来的 ref 而不是构造时的：engine 可能在检查过程中调整安装路径
// （比如用户在界面上改过配置），应以本次请求为准。
func (d *detector) Detect(ctx context.Context, app core.AppRef) (core.Status, error) {
	st, ok, err := d.host.Status(ctx, app)
	if err != nil {
		return core.Status{}, err
	}
	if !ok {
		return core.Status{}, nil
	}
	return st, nil
}
