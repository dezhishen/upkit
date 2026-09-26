// Package plugin 把插件自带的安装流程接入「安装方式」这条轴。
//
// 它本身不做任何安装：取包、校验、解压、替换全部在插件进程内完成。宿主只把工作
// 目录、参数与事件通道交出去（core.Caps.SelfContained），因此 engine 不需要为
// 插件写特例分支 —— 换一种说法：插件是「一种安装方式」，而不是一处特殊逻辑。
//
// 适配器本身只有装配代码，真正的转发在 internal/pluginhost 里，那儿才拿得到
// 插件连接与事件轮询。
package plugin

import (
	"fmt"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/registry"
)

// Kind 是该适配器在清单里的名字（method: plugin）。
const Kind = pluginhost.KindPlugin

// New 构造插件安装方式。
func New(app core.AppRef, deps registry.Deps) (core.InstallMethod, error) {
	if deps.Plugins == nil {
		return nil, fmt.Errorf("%w: 插件宿主未启用，软件 %s 需要它", core.ErrUnsupported, app.ID)
	}
	m, ok := deps.Plugins.Method(app)
	if !ok {
		return nil, fmt.Errorf("%w: 插件来源 %q 没有接管软件 %s 的安装（需要声明 %s 或把 method 写成 %s）",
			core.ErrUnsupported, app.Source, app.ID, upkitplugin.CapabilityFull, upkitplugin.MethodPlugin)
	}
	return m, nil
}
