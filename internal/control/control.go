// Package control 是界面无关的控制层。
//
// 前端（TUI，将来也可能是 GUI）只做两件事：把状态画出来、把输入转成意图。
// 意图交给这里执行，多步流程的完整顺序也只在这个包里有一份。
package control

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/engine"
	"github.com/dezhishen/upkit/internal/logging"
	"github.com/dezhishen/upkit/internal/manifest"
	"github.com/dezhishen/upkit/internal/pluginfeed"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/registry/all"
	"github.com/dezhishen/upkit/internal/settings"
)

// defaultEventBuffer 是事件通道的缓冲深度。
//
// 满了丢事件（见 Sink 的说明），所以宁可给足：进度事件很密集，而界面消费得慢。
const defaultEventBuffer = 1024

// Options 装配控制层。
type Options struct {
	Settings *settings.Settings
	Apps     *apps.File
	Logger   *logging.Manager
	// Host 与 Feed 可为 nil：对应子系统未启用。
	Host *pluginhost.Manager
	Feed *pluginfeed.Store
	// Version 是宿主版本，用于校验订阅里的 min_host_version（dev 不参与比较）。
	Version string
	// HTTPClient 为 nil 时按设置构造（含代理）。
	//
	// 存在的意义是测试能塞一个信任自签证书的客户端：订阅地址强制 https，拿不到
	// 注入点就只能上真实网络。
	HTTPClient *http.Client
	// EventBuffer 为 0 时用 defaultEventBuffer。
	EventBuffer int
}

// Controller 是界面无关的控制层。
//
// 它持有全部领域服务与运行期状态，对外只暴露「意图」方法：前端负责渲染与输入，
// 既不持有服务句柄，也不自己拼多步流程。
//
// 这条边界是刻意划的。视图层顺手改领域状态很难被发现：此前「插件装完没写信任」
// 「来源列表只读清单、看不见自动发现的插件」「更新插件前没停掉它的进程」都属于
// 这类问题 —— 它们不是显示错误，而是流程缺了一步，混在按键处理里就没人看全。
type Controller struct {
	eng  *engine.Engine
	set  *settings.Settings
	afs  *apps.File
	log  *logging.Manager
	host *pluginhost.Manager
	feed *pluginfeed.Store

	version    string
	httpClient *http.Client

	events chan core.Event
}

// New 装配控制层，并顺带把引擎建起来。
//
// 引擎在这里构造而不是由调用方构造：它要的事件接收器就是本层的 Events()，
// 让调用方两头接线只会多出一处可以接错的地方（sink 先给谁、后给谁）。
func New(opts Options) (*Controller, error) {
	buf := opts.EventBuffer
	if buf <= 0 {
		buf = defaultEventBuffer
	}
	c := &Controller{
		set:        opts.Settings,
		afs:        opts.Apps,
		log:        opts.Logger,
		host:       opts.Host,
		feed:       opts.Feed,
		version:    opts.Version,
		httpClient: opts.HTTPClient,
		events:     make(chan core.Event, buf),
	}

	// 注意别把 nil 的 *logging.Manager 直接塞进接口字段：那样接口非 nil，
	// 引擎的 `opts.Log == nil` 判空会失效，之后调用就打到 nil 指针上。
	var logger core.Logger
	if c.log != nil {
		logger = c.log
	}
	var plugins registry.PluginHost
	if c.host != nil {
		plugins = c.host
	}
	var audit func(map[string]any)
	if c.log != nil {
		audit = c.log.Audit
	}

	eng, err := engine.New(engine.Options{
		Settings: c.set,
		Apps:     c.afs,
		Registry: all.Registry(),
		Log:      logger,
		Sink:     c.Sink(),
		Audit:    audit,
		Plugins:  plugins,
	})
	if err != nil {
		return nil, err
	}
	c.eng = eng
	return c, nil
}

// ── 事件流 ────────────────────────────────────────────────────

// Sink 返回给引擎用的事件接收器。
//
// 缓冲区满时丢弃而不是阻塞：事件只是给界面看的进度，拖住引擎等于拖住更新本身。
func (c *Controller) Sink() core.EventSink {
	return core.SinkFunc(func(e core.Event) {
		select {
		case c.events <- e:
		default:
		}
	})
}

// Events 是前端消费的事件流。
func (c *Controller) Events() <-chan core.Event { return c.events }

// ── 应用控制 ──────────────────────────────────────────────────

// Apps 返回当前展开的软件列表（含运行时状态）。
func (c *Controller) Apps() []*engine.App { return c.eng.Apps() }

// Find 按 id 查找一个软件。
func (c *Controller) Find(id string) *engine.App { return c.eng.Find(id) }

// AppEnabled 报告某个软件是否启用（清单里的开关；清单里没有时视为启用）。
func (c *Controller) AppEnabled(id string) bool {
	if c.afs == nil {
		return true
	}
	return c.afs.Enabled(id)
}

// SetAppEnabled 改写某个软件的启用状态并落盘。
func (c *Controller) SetAppEnabled(id string, enabled bool) error {
	if c.afs == nil {
		return fmt.Errorf("软件清单未加载")
	}
	return c.afs.SetEnabled(id, enabled)
}

// Refresh 重新展开软件列表（例如插件来源变化之后）。
func (c *Controller) Refresh(ctx context.Context) ([]*engine.App, error) {
	return c.eng.List(ctx)
}

// Check 检查更新。ids 为空时检查全部，否则只查给定的几个。
func (c *Controller) Check(ctx context.Context, ids []string) ([]*engine.App, error) {
	if len(ids) == 0 {
		return c.eng.Check(ctx)
	}
	for _, id := range ids {
		if _, err := c.eng.CheckOne(ctx, id); err != nil {
			return nil, err
		}
	}
	return c.eng.Apps(), nil
}

// Plan 为单个软件生成更新计划。
func (c *Controller) Plan(ctx context.Context, id string) (*core.Plan, error) {
	return c.eng.Plan(ctx, id)
}

// Apply 执行一批更新。
//
// 并发度取自设置（Engine.ApplyConcurrency），不由前端决定 —— 它是行为参数，
// 不是展示项。
func (c *Controller) Apply(ctx context.Context, ids []string) []engine.JobResult {
	if len(ids) == 0 {
		return nil
	}
	conc := 0
	if c.set != nil {
		conc = c.set.Engine.ApplyConcurrency
	}
	return c.eng.ApplyMany(ctx, ids, conc)
}

// Rollback 回滚到最近一次备份。
func (c *Controller) Rollback(ctx context.Context, id string) error {
	return c.eng.Rollback(ctx, id, "")
}

// Uninstall 卸载某个软件。
func (c *Controller) Uninstall(ctx context.Context, id string, keepData bool) error {
	return c.eng.Uninstall(ctx, id, keepData)
}

// ExportManifest 导出清单快照，返回写入的路径。
func (c *Controller) ExportManifest() (string, error) {
	installed := map[string]string{}
	for _, a := range c.eng.Apps() {
		if a.Status.Version != "" {
			installed[a.Ref.ID] = a.Status.Version
		}
	}
	return manifest.Export("", c.set, c.afs, installed)
}

// ImportManifest 导入清单文件，与当前清单合并后重新展开。
func (c *Controller) ImportManifest(ctx context.Context, path string) (added, updated []string, err error) {
	f, err := manifest.Load(path)
	if err != nil {
		return nil, nil, err
	}
	added, updated = f.MergeApps(c.afs)
	if err := c.afs.Save(); err != nil {
		return nil, nil, err
	}
	if _, err := c.eng.List(ctx); err != nil {
		return nil, nil, err
	}
	return added, updated, nil
}

// SaveSettings 把设置落盘。
func (c *Controller) SaveSettings() error {
	if c.set == nil {
		return fmt.Errorf("设置未加载")
	}
	return c.set.Save()
}

// ── 过渡访问器 ────────────────────────────────────────────────
//
// 下面这几个是给迁移中的前端暂时用的：对应子系统还没收进本层的方法。
// 每收完一个就删一个，别在新增代码里用它们。

// Settings 返回设置（过渡）。
func (c *Controller) Settings() *settings.Settings { return c.set }

// Manifest 返回软件清单（过渡）。
func (c *Controller) Manifest() *apps.File { return c.afs }

// Logger 返回日志管理器（过渡）。
func (c *Controller) Logger() *logging.Manager { return c.log }

// Host 返回插件宿主（过渡）。
func (c *Controller) Host() *pluginhost.Manager { return c.host }

// Feed 返回订阅仓库（过渡）。
func (c *Controller) Feed() *pluginfeed.Store { return c.feed }
