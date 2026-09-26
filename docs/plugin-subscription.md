# 插件订阅

插件订阅把「找插件、装插件」从手工拷贝变成可分发：给一个订阅地址，upkit 拉取一份
清单，按当前平台挑包、校验摘要、落盘到 `plugin/` 目录。装完之后的一切（发现、信任、
启动、调用）与手工放置的插件**走完全相同的链路**。

---

## 1. 订阅格式

`.json` / `.yaml` / `.yml` 三种扩展名都支持。JSON 是 YAML 的子集，所以两种格式共用
同一个解析器与同一份 schema 定义 —— 扩展名只影响报错措辞（例如 `.json` 里写了注释，
会提示你改用 `.yaml`）。

示例见 [`configs/plugin-subscription.example.yaml`](../configs/plugin-subscription.example.yaml)。

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `schema` | ✅ | 订阅格式版本，当前为 `1`；比宿主新会被拒绝 |
| `name` | | 展示名 |
| `updated_at` | | 清单生成时间 |
| `plugins[].id` | ✅ | 插件 id，`^[a-z0-9][a-z0-9._-]{0,63}$`，且不能是 Windows 保留名 |
| `plugins[].version` | ✅ | 用于更新检测与降级保护 |
| `plugins[].mode` | | `catalog`（默认）/ `full` |
| `plugins[].min_host_version` | | 宿主更旧时该条目直接不可用 |
| `plugins[].packages` | ✅ | 平台 → 包的映射，键用 `windows/amd64` 或 `windows/arm64` |
| `packages[].url` | ✅ | 包地址，相对或绝对 |
| `packages[].sha256` | ✅ | **强制**，64 位十六进制（允许 `sha256:` 前缀） |
| `packages[].size` | | 仅用于进度显示 |

解析是**严格模式**：字段名写错会直接报错。订阅等于远程代码执行授权，静默忽略拼写
错误会让人误以为某个限制生效了。

## 2. 地址解析

| 写法 | 解析为 | 需要额外授权 |
| --- | --- | --- |
| `https://cdn.example.com/x.exe` | 原样 | 与订阅不同源时**需要** |
| `/dist/x.exe` | 相对订阅的 origin | 否（同源） |
| `./dist/x.exe`、`dist/x.exe` | 相对订阅地址 | 否（同源） |

**相对路径强制同源**：解析后一旦换域（包括 `//host/x` 这种协议相对写法）直接拒绝。
否则一个被篡改的订阅就能把下载指向任意域名。

## 3. 授权模型

订阅功能默认**关闭**，且需要三级授权，全部只能通过界面产生 ——
`config/subscriptions.yaml` 由程序独占读写，手工编辑它等价于跳过授权确认。

| 级别 | 触发时机 | 记住的方式 |
| --- | --- | --- |
| ① 功能开关 | 首次进入订阅功能 | `feature_authorized` |
| ② 订阅域名 | 添加订阅时确认"信任该域名的插件下载" | `subscriptions[].host` |
| ③ 下载域名 | 包地址是**跨域绝对地址**时单独确认 | `package_hosts[]`（按域名记住） |

任何一级没通过，都不会发生下载：`Install` 的顺序是**先判授权 → 再判降级 → 才下载**。

## 4. 安装与落盘

```
订阅 JSON/YAML
  └─ 按平台选包（windows/amd64 或 windows/arm64）
       └─ 下载 → sha256 校验（不通过即丢弃）
            ├─ 缓存到 cache/plugins/<id>-<version>-<平台>
            └─ 落盘 plugin/<id>[.exe] + plugin/<id>.plugin.yaml
```

订阅会**自动生成 sidecar**（`<id>.plugin.yaml`），内容与手工放置的完全同构，额外记录：

```yaml
version: 1.2.0
sha256: 9f2c…
subscription: https://example.com/plugins.json
```

这样"哪些插件是订阅装的、装的是哪个版本"可以从文件本身还原，便于更新检测与审计。

## 5. 更新与降级

- `Entry.Action()` 会给出 `install` / `update` / `current` / `downgrade` 四种判定；
- **降级默认拒绝**（用有已知漏洞的旧版本覆盖新版本是典型的供应链手法），需要显式
  允许才会执行；
- 摘要不匹配一律拒绝，命中缓存也要重新校验。

### 界面里怎么用

「来源」面板 → 选中一条订阅 → **enter / c** 进入订阅详情：

```
订阅：https://example.com/plugins.yaml

  企业工具    v1.2.0  可安装
  家用镜像    0.9.0 → v1.3.0  可更新
  内部工具    v0.4.0  已是最新
  老版本      v3.0.0 → 1.0.0  降级（将拒绝）

  ████████░░░░░░░░  50%  13.5 MB / 27.1 MB

i/enter 安装或更新   r 重新拉取   esc 返回
```

- 列表会把本机已装版本（读 `plugin/<id>.plugin.yaml`）与订阅版本对上，直接给出结论；
- 可安装/可更新才能触发下载，**降级与已是最新都只提示、不动手**；
- 跨域下载会先弹一次域名授权确认 —— 这一步在界面上完成，不经手配置文件；
- 下载进度实时显示；**结束哨兵**机制保证 UI 不会因为等待进度而卡住；
- 安装成功后自动重新加载插件来源，新插件立即可用。

## 6. 限制

| 项 | 值 |
| --- | --- |
| 订阅文件大小 | ≤ 4 MiB |
| 单个插件包 | ≤ 256 MiB |
| 支持的协议 | 仅 http / https |
| 平台键 | 必须是 `windows/amd64` 或 `windows/arm64`，且包含宿主当前架构 |

## 7. 内置官方源

upkit 自带一条官方订阅：

```
名称：upkit 官方源
地址：https://raw.githubusercontent.com/dezhishen/upkit-hub/main/feed.yaml
```

- 在「来源」面板里按 **o** 一键添加（省掉手输地址）；
- **内置不绕过任何授权**：订阅功能仍默认关闭，域名仍需单独确认；
- 清单由专门的仓库 `dezhishen/upkit-hub` 托管，**不放在主仓库里**。放主仓库的问题很
  实在：清单里写死的产物地址与 sha256 必须与某一次发布严格对应，而它会和代码提交
  搅在一起被顺手改掉，让已发布版本的行为跟着漂移。
- 它列出的插件是 `upkit-hub`（`cmd/upkit-hub/`）—— 一个 catalog 模式的**示例插件**，
  演示一条订阅如何分发多个软件（fzf / Ungoogled Chromium / 7-Zip 的版本查询）。

### 换成自己的源

自建分发、内网镜像或测试时，可在构建时覆盖这个地址（无需改代码）：

```bash
bash scripts/build.sh --feed-url https://mirror.internal/upkit/feed.yaml
make build-all FEED_URL=https://mirror.internal/upkit/feed.yaml

# 等价于直接给编译器：
go build -ldflags "-X github.com/dezhishen/upkit/internal/pluginfeed.BuiltinFeedURL=https://mirror.internal/upkit/feed.yaml" ./cmd/upkit
```

> `-X` 写错变量路径时**不会报错**，只会静默保留默认值。`internal/pluginfeed` 里有
> 测试会真的构建一次探针来验证覆盖生效，并校对构建脚本里的符号路径。

### 写自己的订阅

`internal/pluginfeed/testdata/demo-feed.yaml` 是一份可以直接照抄的模板：多插件、
多插件、多架构、相对路径引用产物，带逐条约定说明。

它同时是 `internal/pluginfeed` 的**测试夹具** —— 这份示例写错了测试会直接变红
（摘要是否真实、每个平台是否都能通过校验、相对路径是否真的装得上，都有断言）。
改动它之后请跑 `go test ./internal/pluginfeed/`。

### 发布官方源

```bash
# 1. 构建插件产物
bash scripts/build-plugin.sh -t windows/amd64 ./cmd/upkit-hub

# 2. 把产物与 feed.yaml 一起上传到 GitHub Release
#    产物文件名保持 upkit-hub-<os>-<arch>[.exe]，feed.yaml 里填真实 sha256
```

清单与产物同一次发布、同一个 tag，因此不存在「清单比产物新」的窗口。

## 8. 边界与未尽事项

- 插件自己管理的**备份无法枚举**：SDK 还没有这个能力，所以界面上看不到它们的列表
  （回滚仍然可用，由调用方给出备份路径）
- **不做代码签名**：信任模型是「首次启动确认 sha256」，不是签名校验
- 订阅里出现了其它平台的包不会报错——它可能同时服务别的工具，upkit 只挑 Windows 那份
