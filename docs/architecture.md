# upkit 架构

当前实现的结构与关键取舍。只描述现在是什么样，不记录演进过程。

---

## 1. 分层与依赖方向

依赖是单向的，且没有任何环：

```
cmd/upkit ──► control ──► engine ──────► registry ──────► 适配器
                 │           │               │
                 │           └───────────────┴──► core（零内部依赖）
                 └──► pluginhost / pluginfeed / apps / settings / logging

tui（界面）────► control（只再用 core 那边的值类型）
```

- **`internal/core` 只 import 标准库**。它定义领域模型（`AppRef` / `Release` /
  `Status` / `Plan`）与四条轴的接口。engine 与 TUI 只认识这些接口，不认识 GitHub、
  zip 或 msiexec。
- **`internal/registry` 是唯一「知道有哪些具体适配器」的地方**，`registry/all`
  子包里每个适配器一行 `RegisterXxx`。engine、TUI、cmd 都不出现在那里。
- 因此：**新增一种来源 / 解包器 / 安装方式 / 探测器 = 新增一个包 + 一行注册**。

`core` 零依赖这条约束是整套设计的地基：它让适配器之间无法互相引用，也让
「谁依赖谁」永远一眼可判。

### 界面层不持有服务

`internal/tui` 只做两件事：把状态画出来、把输入转成意图。它不持有任何领域服务句柄，
多步流程也只有 `internal/control` 里一份 —— 比如安装插件 = 下载校验 → 覆盖前停掉旧
进程 → 覆盖 → 把校验过的摘要记进信任 → 重建来源，五步写在一个方法里。

这条边界是踩出来的：视图层顺手改领域状态很难被发现，因为缺的往往不是显示而是流程里
的一步。此前「插件装完没写信任」「更新前没停掉正在运行的插件」「启停只改了文件、没改
行为」都属于这类，混在按键处理与 `tea.Cmd` 闭包里就没人看全。

约束由 `scripts/check-layering.sh` 钉住（已接进 `make check` 与 CI）：界面可以依赖
值类型（`core` 的模型、`engine.App`、`pluginhost.State`、`pluginfeed.Entry`）与
`control`，不得出现 `*engine.Engine` 之类的服务句柄。设置是有意留的例外 —— 它的字段
由界面直接编辑，是纯内存结构、没有 IO，只有落盘走控制层。

换 GUI 时只需要换掉 `internal/tui`：除 `cmd/upkit` 外没有任何地方 import 它。

## 2. 四条轴

一次更新被拆成四个独立问题，每个问题一条轴：

| 轴 | 回答的问题 | 接口 | 内置实现 |
| --- | --- | --- | --- |
| 来源 | 从哪拿、有哪些版本、下载什么 | `core.SourceResolver` | `githubrelease`、`plugin:<来源ID>` |
| 解包 | 下载物怎么变成文件树 | `core.Unpacker` | `zip`、`tar.gz`、`raw` |
| 安装方式 | 怎么装到这台机器上 | `core.InstallMethod` | `portable-inplace`、`exe-installer`、`msiexec`、`plugin` |
| 探测 | 本机装的是哪个版本、装在哪 | `core.Detector` | `state-file`、`pe-resource`、`dir-name`、`cli-version`、`plugin` |

清单里每个软件声明这四条轴怎么组合（`apps.yaml` 的 `source` / `unpack` / `method` /
`detect` 段），省略时按 `method` 推断默认值。

**探测链**是一条轴上的多个实现按顺序尝试：`detect: [state-file, pe-resource]` 表示先读
状态文件，没有就再去读 PE 资源版本。探测器必须做到「没装时返回 `Installed=false`
且 `err=nil`」—— 报错会被当成探测失败并跳到下一环，而不是中断检查。

### 安装方式的「能力位」

`core.Caps` 让 engine 不必认识具体方式：

```go
type Caps struct {
    NeedsUnpack    bool   // 置 false 表示产物本身就是可用的
    CustomPath     bool
    Silent         bool
    Rollbackable   bool
    PreUninstall   bool
    NeedsElevation bool
    SelfContained  bool   // 自带取包与落地，宿主不下载也不解包
}
```

`SelfContained` 是给插件用的：为 true 时 engine 跳过磁盘预检、下载、解包，只准备一个
工作目录并把参数交出去。**engine 里没有任何一句 `if 这是插件`。**

## 3. 一次更新的完整流程

```
检查（Check）
  ├─ 探测链 → 本机 Status
  ├─ 来源轴 → 上游 Release
  └─ decide() → Action（install / update / reinstall / noop）+ 说明
        │
        ▼
计划（Plan）—— 纯计算，不联网、不写盘
  └─ 安装方式.Plan(req) → []Step（可展示、可 dry-run）
        │
        ▼
执行（Apply）
  ├─ 1. 磁盘空间预检
  ├─ 2. 取产物（命中缓存则跳过下载）
  ├─ 3. 校验摘要（上游 digest → .sha256 附件 → 缓存复核）
  ├─ 4. 解包（Caps.NeedsUnpack）
  ├─ 5. 结束占用进程（guard）
  ├─ 6. 用户确认（behavior.confirm_before_apply）
  ├─ 7. 安装方式.Execute(...) —— 自带回滚
  ├─ 8. 记录状态文件 + 审计 + 清理缓存 + 可选启动
  └─ 9. 上报 finished 事件
```

事件（`core.Event`）是前端解耦的唯一通道：TUI、日志、审计共用同一份语义，engine 只
`Emit`，不关心谁来消费。

**自包含方式（插件）在步骤 1~4 上直接跳过**，从「结束占用进程」开始走，其余步骤
（确认、记录、审计、启动）完全一致 —— 这样插件装的软件与内置方式装的软件在状态、
审计、界面上表现相同。

## 4. 冲突归一化

`engine/conflicts.go` 在检查之前把所有软件过一遍，拦下三种情况：

| 类型 | 判据 |
| --- | --- |
| `same_id` | 清单里 `id` 重复 |
| `same_target` | 规范化路径后安装目录相同 |
| `fuzzy` | 名称 + 入口文件都相同，疑似重复 |

被拦下的条目不执行，但**保留在列表里置灰并说明被谁取代** —— 直接消失会让人以为
「我明明启用了却没更新」。清单里可用 `equivalents` 声明等价组、用 `not_equivalent`
声明「确认不是同一个软件」来解除提示。

之所以一律**硬阻断**而不是弹个警告继续：两个来源往同一个目录覆盖文件，结果不可预测，
而用户通常到软件坏了才发现。

## 5. 插件子系统

插件是**独立进程**，用 `hashicorp/go-plugin` 的 net/rpc 通道通信。三个包各司其职：

| 包 | 职责 |
| --- | --- |
| `pkg/plugin` | 插件作者唯一 import 的包：注册、`Serve`、类型定义、日志转发 |
| `internal/pluginhost` | 宿主侧：发现、信任、启动、把插件类型翻译成宿主领域类型 |
| `internal/pluginfeed` | 订阅：拉取清单、三级授权、下载校验、落盘 sidecar |

关键设计：**插件是「一种安装方式」，不是一处特殊逻辑**。所以它接进的是四条轴中的两条
（`method/plugin`、`detect/plugin`），而 engine 只多了一个 `Caps.SelfContained`。

```
插件声明接管安装
  └─ pluginhost.Manager.takeover()  两条途径：插件级 Capabilities，或单软件 Defaults.Method
       └─ 返回实现 core.InstallMethod 的适配器
            └─ method/plugin 把它交给 registry
                 └─ engine 与内置方式一视同仁
```

`registry.PluginHost` 是**窄接口**（几个方法），定义在 registry 包而不是 pluginhost 包
—— 这样适配器只依赖这个接口，避免「适配器 ←→ 宿主」的循环依赖。

### 跨进程的约定

- **事件是插件缓冲、宿主轮询**，不是插件回调宿主：这样插件的 `Apply` 跑多久都不会因为
  上报而阻塞，宿主也不必向插件暴露回调地址。
- **收尾事件由 SDK 补**（`finished` / `failed`），不指望插件作者记得发 —— 少发一次就会
  让界面进度条永远停在 99%。
- **宿主取消要显式通知**插件（`Cancel(jobID)`）：插件进程不会因为宿主放弃而自动停下，
  它可能正在替换文件。
- **错误分类跨进程保留**：`errors.Is(err, plugin.ErrNotFound)` 在宿主侧依然成立。

## 6. 错误语义

`internal/core/errors.go` 定义一组哨兵值（`ErrNotFound` / `ErrNetwork` / `ErrChecksum` /
`ErrBlocked` / `ErrUnsupported` / `ErrConflict` …）。适配器**必须**用 `%w` 包装它们，
上层据此决定重试、提示还是回滚：

```go
if core.Retryable(err) {   // ErrNetwork / ErrRateLimited
    // 退避重试
}
```

## 7. 并发模型

- **检查**并发跑来源与探测，受 `engine.download_concurrency` 约束（主要是网络等待）
- **安装**默认串行，`engine.apply_concurrency` 可放开 —— 同时替换多个软件会争抢磁盘与
  进程占用判定
- 所有长任务都接受 `context.Context`，取消沿调用链一路传到下载器与适配器
- 事件计数与日志写入都有锁保护；适配器实现的**并发安全由各适配器自己负责**
  （`core` 接口的注释里写明了这条要求）

## 8. 状态与备份

- **状态文件**（`<软件 id>` 派生，见 `internal/version`）记录上次装了什么版本，是
  `state-file` 探测器的依据，也是 `track_revision` 判定的来源
- **备份**在 `backup/<软件 id>/backup_<时间戳>_<版本>`，安装方式自带回滚；保留份数由
  `storage.backup_keep` 控制
- **缓存**按产物摘要命名，命中即跳过下载；换版本不冲突，由 `storage.cache_keep` 清理

## 9. 为什么不做这些

| 不做 | 理由 |
| --- | --- |
| headless / 计划任务模式 | 这是一个给人用的工具。替换软件目录是个破坏性动作，需要人看着 |
| 多实例 | 一个软件一份配置。备份目录与状态文件由软件 id 派生，多实例必然互相踩 |
| OS 级多层配置 | 只做「应用自身」的事：不写注册表策略、不做系统级分发 |
| 系统级安装位置 | 便携布局需要目录可写；不可写时回退到 `%APPDATA%`，而不是去要管理员权限 |
| Linux / macOS 支持 | 只用 Windows。仓库里 `*_other.go` / `*_unix.go` 这类文件只为「让开发机（Linux）能编译与跑测试」而存在，不是多平台支持 |
