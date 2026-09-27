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
| `plugins[].download_hosts` | | schema 2：该插件可能下载东西的域名，宿主会据此**强制校验**（见下节） |
| `plugins[].plugin_hosts` | | schema 2：插件进程自己会访问的域名，仅展示告知 |
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

### 域名声明（schema 2）

上面三级授权里，③ 是「安装时才知道地址、于是当场弹窗」。插件的软件产物（catalog
模式下的第 2 层）连这个都不够：地址是插件**运行时**才给的，用户在添加订阅时根本看不到。

所以 schema 2 允许每个插件条目把它提前声明出来，用户添加订阅（以及声明变化）时一次性
确认，之后不再逐次弹窗：

```yaml
plugins:
  - id: upkit-hub
    download_hosts:        # 宿主强制校验：地址不在这里就拒绛下载
      - github.com
    plugin_hosts:          # 仅告知：插件进程的网络访问，宿主拦不住也无法授权
      - api.github.com
```

约定：

- 只写主机名：小写、不含协议、路径、端口与通配符；写错了在发布前就会被拒（严格解析 +
  发布门禁两边都有校验）。
- `download_hosts` 留空 = **未声明**，回退到第 ③ 级的老规则（跨域逐次确认）；一旦声明，
  插件给出的地址超出集合就会被拒，直到用户重新确认订阅。
- 确认与**声明指纹**挂钩（只覆盖 `download_hosts`）：集合没变就不打扰用户；变了则把
  新增的域名摆出来重新确认。指纹不排序就无效，因此集合要先排序再取哈希。
- 不覆盖重定向目标（GitHub 资产会 302 到 CDN）：那是实现细节，写死只会跟着过期 ——
  跳转后的字节由 **sha256 兜底**。插件进程自己的流量也不在覆盖范围内。
- 第 2 层要真拦得住，还得把插件给的摘要接上（`core.Verifier`）：域名管「往哪去」，
  摘要管「拿到什么」。

> 现状（截至 schema 2 引入时）：字段、校验、指纹算法与第 2 层的 `ExpectedDigest` 已就位；
> **确认流程（添加/更新时的弹窗与记账）与超范围拒绛的接线尚未实现** —— 未声明或未确认
> 时行为与 schema 1 一致。

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

**sidecar 不承载信任**：它和可执行文件在同一个目录、同样可写，拿它当信任依据等于取消了
"首次启动确认 sha256"这道门。信任只记在 `apps.yaml` 的 `sources[].trust` 里，而订阅安装
会把已经校验过的摘要直接写进去（因为下载本身已经过三级授权 + 摘要强制校验），装完即可用。
关掉 `设置 → 插件 → 自动加载已授权插件` 则留回逐个确认。

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
地址：https://github.com/dezhishen/upkit-hub/releases/latest/download/feed.yaml
```

- 在「来源」面板里按 **o** 一键添加（省掉手输地址）；
- **内置不绕过任何授权**：订阅功能仍默认关闭，域名仍需单独确认；
- 清单与插件产物都由 `dezhishen/upkit-hub` 托管，**主仓库只做平台**。先分清两件事：
  - 编进二进制的是**地址**（一个字符串），不是清单内容。按 `o` 时它被预填到输入框，
    确认后由 upkit 在运行时拉取；
  - 清单不放在主仓库，是因为它按平台写死了插件产物的地址与 sha256，必须与某一次插件
    发布严格对应；跟着主仓库的代码提交走会被顺手改掉，让已发布版本的行为跟着漂移。
- 地址取 release 直链而不是 raw 分支链接：release 附件不可变，upkit-hub 主分支上任何
  半成品提交都不会影响已发布版本。代价是 upkit-hub 每更新一次清单就要发一个 release。
- 它列出的插件是 `upkit-hub` —— 一个 catalog 模式的插件，演示一条订阅如何分发多个
  软件（fzf / Ungoogled Chromium / 7-Zip 的版本查询）。本仓库里留了它的可运行源码
  （`cmd/upkit-hub/`）供测试与开发参考，但发布流水线不再构建或发布它。

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
多架构、相对路径引用产物，带逐条约定说明。

它同时是 `internal/pluginfeed` 的**测试夹具** —— 这份示例写错了测试会直接变红
（摘要是否真实、每个平台是否都能通过校验、相对路径是否真的装得上，都有断言）。
改动它之后请跑 `go test ./internal/pluginfeed/`。

### 发布官方源

官方清单与插件产物都由 `dezhishen/upkit-hub` 托管，**主仓库不发布它们** ——
脚本与流水线也都放在那边（本仓库只做平台）：

```bash
# 在 upkit-hub 仓库里，一条命令构建两个架构并生成清单：
make release-local BASE_URL=https://github.com/dezhishen/upkit-hub/releases/download/v1.2.3

# 等价的手工步骤：
bash scripts/build-plugin.sh --release-name -v 1.2.3 -t windows/amd64 ./cmd/upkit-hub
bash scripts/gen-feed.sh --plugins-dir dist/plugins --version 1.2.3 \
  --base-url https://github.com/dezhishen/upkit-hub/releases/download/v1.2.3 -o feed.yaml
```

打 tag 即发布：那边的工作流会把两个架构的产物连同 `feed.yaml` 一起挂到 Release 上
（附件名必须精确，`feed.yaml` 不能改名）。清单与产物同一次发布、同一个 tag，因此
不存在「清单比产物新」的窗口。

## 8. 边界与未尽事项

- 插件自己管理的**备份无法枚举**：SDK 还没有这个能力，所以界面上看不到它们的列表
  （回滚仍然可用，由调用方给出备份路径）
- **不做代码签名**：信任模型是「首次启动确认 sha256」，不是签名校验
- 订阅里出现了其它平台的包不会报错——它可能同时服务别的工具，upkit 只挑 Windows 那份
