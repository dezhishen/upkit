# upkit 插件开发指南

upkit 的软件来源可以做成**进程外插件**：一个插件是一个独立可执行文件，向 upkit
提供一组软件（含版本与下载地址）。下载、校验、解包、落地、版本探测仍然复用 upkit
内置的四条轴，所以写一个插件通常只需要几十行代码。

> 一个插件 = 一个可执行文件 = 一个来源 = 多个软件。

---

## 1. 三十秒上手

```go
package main

import (
	"context"

	"github.com/dezhishen/upkit/pkg/plugin"
)

type myApp struct{ cfg plugin.AppConfig }

// 单一软件的构造器：宿主首次需要这个软件时调用一次。
func newMyApp(cfg plugin.AppConfig) (plugin.App, error) {
	return &myApp{cfg: cfg}, nil
}

// catalog 模式下唯一必需的方法。
func (a *myApp) Versions(ctx context.Context, req plugin.VersionsRequest) ([]plugin.Release, error) {
	return []plugin.Release{{
		Version: "1.2.3",
		Artifacts: []plugin.Artifact{{
			Name: "myapp-1.2.3.zip",
			URL:  "https://example.com/myapp-1.2.3.zip",
			Size: 1 << 20,
		}},
	}}, nil
}

func main() {
	plugin.Serve(plugin.Info{ID: "my-source", Name: "我的源", Version: "1.0.0"},
		plugin.Register("my-app", newMyApp, plugin.WithName("我的软件")),
	)
}
```

就这三件事：**引入一个包、用 `Register` 注册软件并给出构造器、用 `Serve` 交给宿主。**

---

## 2. 构建与部署

交叉编译到 Windows（upkit 宿主是 Windows 时插件也必须是 .exe）：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=1.0.0" -o my-source.exe ./path/to/my-plugin
```

> 发布用的封装脚本 `scripts/build-plugin.sh`（自动补平台后缀、注入版本号）现在在
> [upkit-hub](https://github.com/dezhishen/upkit-hub) 里 —— 官方源的发布流水线用的
> 就是它，本仓库不再保留一份。

部署到 upkit 的 `plugin/` 目录（与 `upkit.exe` 同级）：

```
plugin/
├── my-source.exe
└── my-source.plugin.yaml
```

`my-source.plugin.yaml` 描述这个可执行文件是什么插件：

```yaml
id: my-source            # 必须与 Info.ID 一致，决定插件私有目录名
name: 我的源
mode: catalog            # catalog | full
exec: my-source.exe      # 可省略，默认取 <id>[.exe]
timeout_seconds: 20      # 可省略
```

---

## 3. 信任模型（必读）

**插件等于任意代码执行**，因此 upkit 不会自动运行新发现的插件：

1. 启动时 upkit 扫描 `plugin/` 目录，把未信任的插件记为 `untrusted` 并**跳过启动**：
   「来源」面板里显示 `? 未信任 · 按 t 信任`，日志里也会给出它的 `sha256`；
2. 在「来源」面板选中它按 `t`，确认框会把**可执行文件路径与 sha256** 都摆出来 ——
   信任比的是「这个文件是不是我要的那个」，只给一个 id 没法判断。核对无误后确认，
   upkit 把它记进 `apps.yaml` 并立即加载：

```yaml
sources:
  - id: my-source
    kind: plugin
    trust: "3b1f…"        # 面板确认时写入的 sha256
```

   清单里原本没有这条来源时，按 `t` 会自动补一条最小条目；`exec`、`mode` 之类仍由
   sidecar 提供，不必在两个文件里各写一份。也可以自己写这份 `apps.yaml`，再按 `r`
   重载（`r` 会重读磁盘上的清单），或重启 upkit。
3. 插件文件一旦被替换，哈希变化，需要重新信任 —— 这能挡住"同名文件被偷偷换掉"。

订阅安装的插件不走这一步：包的摘要在安装时已按你授权过的订阅强制校验过，upkit 会把
结果直接记进信任。不想给这个便利（希望每个插件都自己点头一次），就关掉
`设置 → 插件 → 自动加载已授权插件`，那样它们会停在未信任，由你按 `t` 决定。

插件被用户直接双击运行时不会静默挂起：upkit 通过握手令牌识别，go-plugin 会打印
`This binary is a plugin. These are not meant to be executed directly.` 后退出。

---

## 4. 两种模式

| 模式 | 插件要实现 | 谁负责安装 | 适用场景 |
| --- | --- | --- | --- |
| **catalog** | 只有 `App.Versions` | upkit 内置四轴（下载→解包→落地→探测） | 私有镜像、企业内部软件清单、自建版本索引 |
| **full** | 额外实现 `plugin.Method` | 插件自己（`Plan`/`Apply`/`Rollback`/`Uninstall`） | 特殊安装逻辑、私有协议、需要调用外部程序 |

两种模式都已接入宿主：catalog 与 full 都有真实子进程的端到端测试。

### 怎么写一个 full 模式软件

只要让构造器返回的实例实现 `plugin.Method`，并在注册时声明一次：

```go
plugin.Register("local-stub", newLocalStub,
	plugin.WithDefaults(plugin.Defaults{
		Method: plugin.MethodPlugin, // 由插件自己安装
	}),
)
```

```go
type localStub struct{ /* ... */ }

// 编译期确认方法集完整，别等运行到一半才发现少写了一个。
var _ plugin.Method = (*localStub)(nil)

func (s *localStub) Versions(ctx context.Context, _ plugin.VersionsRequest) ([]plugin.Release, error) {
	return []plugin.Release{{Version: "1.0.0", Tag: "v1.0.0"}}, nil // Artifacts 可以不填
}

// 没装时必须 Installed=false 且 err=nil —— 探测链靠这个约定决定「要不要更新」。
func (s *localStub) Status(ctx context.Context, req plugin.StatusRequest) (plugin.Status, error) {
	// req.InstallPath 就是清单里配置的安装目录，req.DataDir 是你自己的私有目录。
	return plugin.Status{Installed: true, Version: "1.0.0", Path: req.InstallPath}, nil
}

func (s *localStub) Plan(ctx context.Context, req plugin.PlanRequest) (plugin.PlanResult, error) {
	return plugin.PlanResult{
		Action: plugin.ActionInstall, // 留空则由宿主按版本比对决定
		To:     req.To,
		Steps: []plugin.Step{
			{Kind: plugin.StepDownload, Desc: "拉取安装包"},
			{Kind: plugin.StepCopy, Desc: "写入 " + req.InstallPath},
		},
	}, nil
}

func (s *localStub) Apply(ctx context.Context, req plugin.PlanRequest, send plugin.EventSender) (plugin.Result, error) {
	send.Send(plugin.Event{Kind: plugin.EventStarted, Phase: "安装"})
	send.Send(plugin.Event{Kind: plugin.EventProgress, Phase: "安装", Done: 1, Total: 2})
	// ... 执行实际逻辑；定期检查 ctx.Done()，宿主取消时会通知你 ...
	return plugin.Result{Action: plugin.ActionInstall, To: req.To, InstallPath: req.InstallPath}, nil
}

func (s *localStub) Rollback(ctx context.Context, req plugin.RollbackRequest) error { /* 可选 */ }
func (s *localStub) Uninstall(ctx context.Context, req plugin.UninstallRequest) error { /* 可选 */ }
```

由此带来的约定：

- **宿主不再下载、不解包**。你拿到的是工作目录 `WorkDir`、缓存目录 `CacheDir`、
  安装路径 `InstallPath` 和私有目录 `DataDir`；`Release.Artifacts` 里的地址宿主不会
  去取（填了主要用于界面展示）。
- **不实现 `Rollback` / `Uninstall` 也没关系**，调用时会得到 `ErrNotSupported`；只实现
  其中几个方法同样可以，宿主会按能力回退。
- **收尾事件由 SDK 补**（成功补 `finished`、失败补 `failed`），你不必自己发。想在中途
  上报进度就用 `send`，它允许为 nil。
- **长任务要尊重 `ctx`**：宿主超时或用户取消时会调用 `Cancel(jobID)`，你的 `Apply`
  应当据此尽快退出，避免「宿主已经放弃、插件还在写盘」。
- **安装路径不要自己猜**：一律用 `req.InstallPath`，这样用户在界面上改路径才会生效。
- **枚举备份还没有 SDK 能力**，所以 `upkit` 界面上看不到插件管的备份；`Rollback`
  仍然可用（宿主传入备份路径）。

上面这些都能在 `cmd/upkit-plugin-example/full.go` 里找到完整可运行的版本。

---

## 5. 影响安装方式的元信息

`Target` 告诉宿主这个软件期望装在哪，`Defaults` 用来影响四条轴的装配：

```go
plugin.Register("corp-vpn", newCorpVPN,
	plugin.WithName("公司 VPN"),
	plugin.WithProvides("corp-vpn", "example/corp-vpn"), // 软身份：跨来源去重
	plugin.WithTarget(plugin.TargetHint{
		PathTemplate: "${ROOT}/CorpVPN",
		Entrypoints:  []string{"vpn.exe"},
		Processes:    []string{"vpn.exe"},
		Preserve:     []string{"config"},
	}),
	plugin.WithDefaults(plugin.Defaults{
		Method: "portable-inplace",   // 落地方式
		Unpack: "zip",                // 解包方式
		Detect: []string{"state-file", "pe-resource"},
		Install: plugin.InstallDefaults{
			Path:     "${ROOT}/Custom", // 覆盖 Target 里的路径
			Preserve: []string{"config"},
		},
		MethodOptions: plugin.NewOptions("args", "--silent"),
	}),
)
```

`Defaults` 是**结构体**，没有 map：`Method` / `Unpack` / `Detect` / `Pin` /
`TrackRevision` 直接填，`Install` 是 `InstallDefaults`（`Path` / `Entrypoints` /
`Processes` / `Preserve`），四条轴的额外选项用 `SourceOptions` / `UnpackOptions` /
`MethodOptions`（`plugin.NewOptions("键", "值", ...)`）表达。

用户在 `apps.yaml` 里写同 ID 的条目可以覆盖这些默认值；只存在于内存中，不会回写用户清单。

---

## 6. 插件配置界面

用 `WithConfigSchema` 静态声明配置项（纯数据，不需要构造实例）：

```go
plugin.WithConfigSchema(plugin.ConfigSchema{
	Title: "公司 VPN",
	Fields: []plugin.ConfigField{
		{Key: "download_base", Label: "内网镜像地址", Type: plugin.FieldString, Required: true},
		{Key: "channel", Label: "通道", Type: plugin.FieldEnum, Enum: []string{"stable", "beta"}, Default: "stable"},
	},
})
```

用户在 `apps.yaml` 的 `sources[].config` 里填写后，宿主会透传到 `AppConfig.Config`。

**读配置不需要接触 map**：声明一个结构体，用 `DecodeConfig` 一次解出来（SDK 内部把配置
转成 JSON 再解码）：

```go
type corpVPNConfig struct {
	DownloadBase string `json:"download_base"`
	Channel      string `json:"channel"`
}

func newCorpVPN(cfg plugin.AppConfig) (plugin.App, error) {
	var c corpVPNConfig
	if err := plugin.DecodeConfig(cfg.Config, &c); err != nil {
		return nil, err
	}
	if c.DownloadBase == "" {
		return nil, fmt.Errorf("%w: 请先配置 download_base", plugin.ErrBadConfig)
	}
	...
}
```

需要单个值时也可以用带类型的读取方法：`cfg.Config.String("download_base", "")`、
`cfg.Config.Int("timeout", 30)`、`cfg.Config.Bool("verbose", false)`。
需要校验或热应用时，让任一 `App` 实现 `plugin.Configurable`（参数同样是 `plugin.Config`）。

---

## 7. 日志与错误

- 用 `cfg.Log`（或 `plugin.Logger`）打日志：它写 stderr，由宿主接管后套用级别、
  轮转与脱敏，与 upkit 自身日志同一条流（前缀 `plugin/<插件ID>`）。
- 返回错误时用哨兵值包装，宿主据此区别处理而不是笼统判失败：

```go
return nil, fmt.Errorf("%w: 缺少 download_base", plugin.ErrBadConfig)
```

`ErrBadConfig`（配置问题）、`ErrNotFound`、`ErrRateLimited`（提示配令牌）、
`ErrNotSupported`（能力未实现，宿主会回退）会跨进程保留语义，`errors.Is` 依然可用。

---

## 8. 生命周期与隔离

- 每个启用的插件一个常驻子进程，由 upkit 托管；upkit 退出时一并结束。
- 单个插件启动失败、崩溃或超时**不影响**其它插件与内置能力，失败原因记录在来源状态里。
- 插件私有目录固定为 `<根目录>/data/plugins/<插件ID>/`（缓存与状态）与
  `<根目录>/log/plugins/<插件ID>/`（日志），通过 `AppConfig.DataDir` / `LogDir` 传入；
  **配置文件由宿主独占读写**，插件只在 `DataDir` 下写自己的缓存。
- 传输用 `net/rpc` + gob 信封（载荷 JSON），不需要 `protoc`，也不需要生成任何代码。
- 协议版本写在 `plugin.APIVersion`：不匹配时宿主会拒绝连接并提示升级插件或主程序。

---

## 9. 调试

```bash
# 单独构建，确认能编译（开发机上交叉编译到 Windows）
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o dist/my-plugin.exe ./cmd/my-plugin

# 当前平台的产物（仅开发机冒烟用）
go build -o dist/my-plugin ./cmd/upkit-plugin-example
```

把产物放进 upkit 的 `plugin/` 目录，再配一个 `my-plugin.plugin.yaml`（sidecar 描述这个可执行文件
是什么插件，**不承载信任**），然后在「来源」面板按 `t` 把它的 sha256 记进信任，才能被宿主
加载 —— 见上面第 2、3 节。

插件进程的 stdout/stderr 会带 `plugin=...` 前缀写进 upkit 日志（`log/upkit-*.jsonl`），
排查启动失败时先看这里。

---

## 10. 参考实现

- `cmd/upkit-plugin-example/`：约 100 行的 catalog 插件示例（两个软件、一个需要配置、
  一个不需要），演示注册、构造器、元信息、配置 schema。
- `pkg/plugin/`：SDK 全部源码，插件作者只需要 import 它一个包。
- 端到端测试见 `pkg/plugin/client_e2e_test.go` 与 `internal/pluginhost/host_test.go`：
  真的编译出插件、真的以子进程方式启动、真的跨进程调用。
