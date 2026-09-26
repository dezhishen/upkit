// Package pluginsource 把插件来源接进宿主的核心来源轴。
//
// catalog 模式的插件只回答「有哪些版本、下载什么」，因此这里只需要实现
// core.SourceResolver；下载、解包、落地、版本探测全部复用内置的四条轴。
package pluginsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

// kindPrefix 是插件来源的类型前缀，完整形式为 "plugin:<来源ID>"。
const kindPrefix = apps.SourceKindPluginPrefix

// New 构造插件来源适配器。
//
// 该函数的签名必须与 registry.SourceFactory 一致，供 registry 按前缀注册。
func New(app core.AppRef, deps registry.Deps) (core.SourceResolver, error) {
	if deps.Plugins == nil {
		return nil, fmt.Errorf("插件子系统未启用，无法使用来源 %q", app.Source)
	}
	// 限定标识的拼法只在清单层定义一次（apps.ParsePluginRef），三条轴的插件适配器共用。
	ref, err := apps.ParsePluginRef(app)
	if err != nil {
		return nil, err
	}
	return &resolver{sourceID: ref.SourceID, appID: ref.AppID, host: deps.Plugins}, nil
}

type resolver struct {
	sourceID string
	appID    string
	host     registry.PluginHost
}

func (r *resolver) Name() string { return kindPrefix + r.sourceID }

// Latest 返回最新版本；清单里固定了版本时返回被固定的那一个。
func (r *resolver) Latest(ctx context.Context, app core.AppRef) (core.Release, error) {
	pin := strings.TrimSpace(app.Pin)
	if pin == "" {
		return r.host.Latest(ctx, r.sourceID, r.appID)
	}
	rels, err := r.host.Versions(ctx, r.sourceID, r.appID, 0)
	if err != nil {
		return core.Release{}, err
	}
	for _, rel := range rels {
		if rel.Version == pin || rel.Tag == pin {
			return rel, nil
		}
	}
	return core.Release{}, fmt.Errorf("%w: 来源 %s 中没有固定版本 %q", core.ErrNotFound, r.sourceID, pin)
}

// Versions 返回可选版本（新 → 旧）。
func (r *resolver) Versions(ctx context.Context, app core.AppRef, limit int) ([]core.Release, error) {
	return r.host.Versions(ctx, r.sourceID, r.appID, limit)
}
