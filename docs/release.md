# 发布手册

一句话：**在 Actions 里点一次 `Release`，选版本位与阶段**，其余交给流水线 ——
算版本、打 tag、构建、生成订阅清单、按 PR/commit 自动写 changelog、上传产物。

---

## 1. 版本号怎么走

两条通道，四个版本位：

| 场景 | bump | stage | 例子（基线 `v1.2.3`） |
| --- | --- | --- | --- |
| 修 bug（主线） | `patch` | `stable` | `v1.2.4` |
| 修补已发布版本 | `hotfix` | `stable` | `v1.2.4` |
| 加功能 | `minor` | `stable` | `v1.3.0` |
| 破坏性改动 | `major` | `stable` | `v2.0.0` |
| 预览以上任意一项 | 同左 | `rc` / `beta` | `v1.3.0-rc.1` |

几条规则值得记住：

- **一切从最近的正式版起算**。`v2.0.0-rc.1` 存在时再发 `patch`，得到的是
  `v1.2.4` 而不是 `v2.0.0-rc.2` —— 预览版不是基线。
- **`hotfix` 与 `patch` 算出来的是同一个位置**。分开只为让发布记录能看出
  「这是修补已发布版本」还是「这是主线常规迭代」。
- **预览版自动递增序号**：已有 `v1.3.0-rc.1`，再发一次 rc 得到 `v1.3.0-rc.2`。
- 预览版在 GitHub 上标记为 **pre-release**，因此**不会**成为 `releases/latest`；
  而内置订阅地址取的正是 upkit-hub 的 `releases/latest`，所以 upkit-hub 发预览版
  不会影响普通用户读到的清单。

版本计算由 `scripts/next-version.sh` 负责，纯 bash、无依赖，可以本地先看一眼：

```bash
bash scripts/next-version.sh minor rc        # 例如输出 v1.3.0-rc.1
make next-version BUMP=minor STAGE=rc         # 等价写法
```

## 2. 怎么发一个版本

1. 确认 `main` 是绿的（CI 的三个 job 全过）。
2. Actions → **Release** → *Run workflow*：
   - `bump`：`patch` / `minor` / `major` / `hotfix`
   - `stage`：`stable` / `rc` / `beta`
   - `dry_run`：勾上就只演练（打 tag 前的完整构建，不发版）——**第一次用建议先演练一次**
3. 运行结束后去 Releases 页面核对产物与 changelog。

发布只产出 upkit 本体。插件制品与订阅清单归 `upkit-hub`，不在本仓库发布（见 §4）。

流水线的四个 job：

```
verify（gofmt / vet / test）
  └─ version（算版本 → 打 tag → 算 changelog 起点）
       └─ build（upkit ×2 架构，并注入内置订阅地址）
            └─ publish（gh release create，附带全部产物）
```

`verify` 会在发布前再跑一遍测试：CI 虽然在 `main` 上过了，但 tag 有可能打在别处。

### 发布门禁：为什么 tag 打了也可能不发版

`verify` 除了重跑 gofmt / vet / test，还会用 GitHub API 确认**这个提交本身跑过
并通过了 `ci.yml`**。两者不是重复劳动：重跑能盖住的只有格式与单元测试，而覆盖率
门禁、交叉编译、发布演练都在 `ci.yml` 里 —— “重跑看起来也绿”不等于“这个提交被
验证过”。

各种情况的判定：

| 该提交的 CI 状态 | 结果 |
| --- | --- |
| `completed success` | 继续发版 |
| `completed failure` / `cancelled` / `skipped` | **拒绝**，tag 已经打了，但 release 不发 |
| 仍在 `in_progress` / `queued` | **拒绝**，提示等 CI 结束 |
| 找不到记录 | **拒绝**，提示先推到分支、等 CI 过、再在该提交上打 tag |

只认 `push` 与手动重跑触发的运行：同一个提交也可能被 PR 触发过 CI，那次失败
不代表分支上的状态有问题。

**为什么不在打 tag 那一刻拦住**：GitHub 的 tag 不能用 Rulesets 要求 status
check（那个能力只对分支生效），GitHub 也不提供服务端 pre-receive hook
（那是 GitLab / 自建才有的）。所以只能在流水线里拦 —— tag 会创建，但 release
不会发出去，需要先修好再重新打一个 tag。

### main 上的发布演练

推送到 `main`（以及开 PR）时，`ci.yml` 会自动跑一次**不推 release 的构建**：

```
test（gofmt / vet / test -race + 覆盖率 + 13 个包的分级门禁）
  ├─ build（两个架构的 exe，产物传 artifact 供下载）
  └─ smoke（发布演练：真构建全部产物 + 生成 feed.yaml，产物传 artifact）
       └─ publish-smoke（发布演练：校验产物齐全与前置条件，不创建 Release）
```

两个演练 job 与真发版的对应 job **分别调用同一个可复用工作流**
（`build-release.yml` 与 `publish-release.yml`），因此两边不会漂移 ——
「main 上是绿的」就意味着「现在能发版」。它们**不碰 tag、不碰 Release**，
产物（含演练版 `feed.yaml`）放在名为 `release-smoke` 的 artifact 里供下载核对，
演练版本号形如 `v0.0.0-dev.<commit sha>`。

发布这一步与构建一样容易出与环境无关的事故（漏了 `actions/checkout`、产物路径
写错、`gh` 参数拼错），所以也放进演练跑一遍。`publish-smoke` 会真实地取产物、
逐个校验存在性、拼出 `gh release` 的完整参数，只差最后一步不执行。

这样插件构建、清单生成这些环节在每次提交后就验过了，不会拖到发版那一刻才暴露。

### 手工打 tag 的路径

先 `git tag -a v1.2.4 -m "发布 v1.2.4" && git push origin v1.2.4` 也会触发同一个
工作流（`push: tags: v*`），版本号直接取 tag 名。适合需要自定义 tag 或重跑发布时。

## 3. 发布产物

| 文件 | 说明 |
| --- | --- |
| `upkit-windows-amd64.exe` | 主程序（amd64） |
| `upkit-windows-arm64.exe` | 主程序（arm64） |
| `sha256sums.txt` | 以上产物的校验值 |

插件制品与订阅清单不在本仓库发布，见 §4。

### 想下载一个 exe 来试，去哪找

CI 的 artifact 分两处，用途不同：

| 来源 | artifact 名 | 内容 | 保留 |
| --- | --- | --- | --- |
| `ci` 的 **build** job | `upkit-windows-amd64` / `upkit-windows-arm64` | 只有主程序 exe | 7 天 |
| `ci` 的 **smoke** job | `release-smoke` | 完整的发布产物（两个架构的 exe + `sha256sums.txt`） | 仓库默认 |
| `release` 的 **publish** | 无 artifact，直接进 [Releases](../../releases) | 同上 | 永久 |

下载路径：仓库 → **Actions** → 选一次运行 → 页面底部 **Artifacts**。

两个 CI job 都排在 `test` 之后：**测试不过就不会有可下载的产物**。所以 artifact
列表为空时，先看同一次运行里 `test` 是不是红的。

日常只是想拿个能跑的 exe 手动点一点，用 **build** 的产物就够了 —— 每次 push 都会
重新构建，artifact 名里就是目标平台。要对照 release 内容、或者验一遍注入订阅地址后
的完整构建，才需要 **smoke** 的 `release-smoke` —— 两者内容相同，只是 smoke 走的是
与真发版完全一致的那条流水线。

这些 exe 带版本信息（`v0.0.0-dev.<短提交>`），`upkit.exe --version` 能看出是哪个
提交构建的。

## 4. 订阅清单与内置订阅地址

**upkit 不产清单。** 本仓库只做平台，外加把一个订阅地址编进二进制。

| 仓库 | 负责 |
| --- | --- |
| `upkit` | 平台本体；内置一个订阅地址 |
| `upkit-hub` | 官方插件库：插件制品 + `feed.yaml` |

清单里按平台写死了插件产物的地址与 sha256，必须与某一次插件发布严格对应。让它跟着
upkit 的发布节奏走，就会陷入「更新插件得先重发 upkit」的循环；分开放则两边各自演进。

### 内置地址

默认值在 `internal/pluginfeed/builtin.go`，指向 upkit-hub 的 release 直链：

```
https://github.com/dezhishen/upkit-hub/releases/latest/download/feed.yaml
```

发布流水线会显式注入同一个值（`build-release.yml` 的 `feed-url` 输入 →
`scripts/build.sh --feed-url`）。地址以 `-ldflags -X` 编进二进制，所以**换源要重发
upkit**；注入的意义是把「用哪个源」这个决定放在流水线上，改镜像或内网源时不必动代码。

取 release 直链而不是 raw 分支链接：

- release 附件不可变，upkit-hub 主分支上任何半成品提交都不会影响已发布版本；
- raw 链接紧跟分支，改一行即刻对所有 upkit 生效，还带 CDN 缓存。

代价是 upkit-hub 每更新一次清单就要发一个 release。

### 换成自己的订阅源

```bash
bash scripts/build.sh --feed-url https://mirror.internal/upkit/feed.yaml
make build-all FEED_URL=https://mirror.internal/upkit/feed.yaml
```

`-X` 打错变量路径不会报错、只会静默保留默认值，所以 `internal/pluginfeed` 里有测试
用真实的 `-ldflags` 构建一次探针来验证覆盖生效。

### 本仓库里保留的插件工具

下面几样仍在源码树里，但**发布流水线不再构建或发布它们**：

| 路径 | 用途 |
| --- | --- |
| `cmd/upkit-hub/` | 官方插件的可运行样例，供测试与开发参考 |
| `cmd/upkit-plugin-example/` | 最小起步样例，`docs/plugin-dev.md` 的实操对象 |
| `scripts/build-plugin.sh` | 第三方插件的构建入口 |
| `scripts/gen-feed.sh` | 清单生成器，待 upkit-hub 建好后搬过去 |

`gen-feed.sh` 不依赖本仓库的任何东西 —— 只吃一个插件产物目录与 `--base-url`，所以
搬到 upkit-hub 后把 `--base-url` 指向 upkit-hub 自己的 release 即可。生成前它会检查
**每个插件是否覆盖了全部受支持平台**：漏一个架构，那个架构的用户会在「校验订阅」
这一步失败，而这本可以在发布前发现；确实有意只发部分架构时用 `--allow-partial` 跳过。

`internal/pluginfeed` 里有测试盯着这条链路：脚本产出的清单必须能被宿主解析、通过
校验、摘要与产物一致；脚本里写死的平台列表也有一致性检查，与代码漂移会报警。这几个
测试应当跟着脚本一起搬走。


## 5. changelog 怎么来的

由 GitHub 依据 **PR 与 commit** 自动生成（`gh release create --generate-notes`），
分类规则在 `.github/release.yml` —— 按 PR 上的标签归类：

```
⚠️ 破坏性变更   breaking, breaking-change
✨ 新功能       feature, enhancement
🐛 问题修复     fix, bug, bugfix
⚡ 性能与重构   performance, refactor
📚 文档与示例   documentation, docs
🧰 构建与依赖   build, ci, dependencies
🔧 其它变更     （兜底）
```

所以**给 PR 打标签，changelog 就自动成型**，不需要维护 `CHANGELOG.md`，也不需要
额外工具。直接推到 `main` 的 commit 会落到「其它变更」，不会丢。

changelog 的起点始终是**最近一个正式版**：预览版据此列出自上个正式版以来的全部变更；
正式版给出的也是相对上个正式版的完整变化，而不是只有 rc 之后新增的那几条。

想让某次改动不出现在 changelog 里，给 PR 打 `skip-changelog` 标签
（dependabot 与 github-actions 的提交默认已排除）。

## 6. 出错了怎么办

| 情况 | 处理 |
| --- | --- |
| 版本算错了 / tag 打歪了 | 删掉 tag 重发：`git push --delete origin v1.2.4 && git tag -d v1.2.4` |
| tag 已存在导致流水线报错 | 说明该版本已发过；换一个版本位，或先删 tag |
| 构建失败 | 修好推到 `main`，再跑一次；tag 若已打上，先删掉它 |
| Release 建好了但产物不全 | 重跑工作流：`publish` 检测到 Release 已存在时会**覆盖附件并重新生成 notes**，不会报错 |
| 想先看看效果 | 勾 `dry_run` 演练：走完 verify → version → build，但不打 tag、不发版 |
| tag 已打出，但 `publish` 失败 | 见下节「流水线本身有 bug」 |

`publish` 是幂等的（`gh release upload --clobber` + `gh release edit`），所以重跑安全。

### 流水线本身有 bug 时怎么救

`publish` 失败往往不是代码的问题，而是工作流自身写错了。这种情况下**「Re-run
failed jobs」是没用的** —— 重跑用的是那次运行当时的 workflow 文件，改在 `main`
上的修复不会被带上。

正确做法是把 tag 移到修复提交上，靠 `push: tags` 重新触发一次：

```bash
# 1. 修好工作流，推到 main，等 CI（含发布演练）变绿
git push origin main

# 2. 把 tag 移到这个提交上并重推
git push --delete origin v0.0.1
git tag -fa v0.0.1 -m "发布 v0.0.1"
git push origin v0.0.1
```

两个前提：

- **修复提交必须先通过 `ci.yml`**，否则 `verify` 的门禁会拦下（它要求发布依据
  的那个提交跑过 CI）。
- **tag 推的是哪个提交，就用哪份工作流定义** —— 所以修复必须落在 tag 指向的
  提交上，不能只留在 `main` 后面。

若该版本已经发出去了（Release 已存在），就不能再挪 tag 覆盖别人的下载，
应当换一个版本位重发。

## 7. 本地能做的

```bash
make next-version BUMP=minor STAGE=rc    # 看看下一个版本号是多少
bash scripts/build.sh --version v1.2.3   # 本地构建（可加 --feed-url 换订阅源）
```

把订阅链路整个走一遍（这部分将来归 upkit-hub，现在仍可在本仓库验证）：

```bash
bash scripts/build-plugin.sh --release-name -v 1.2.3 -t windows/amd64 ./cmd/upkit-hub
bash scripts/gen-feed.sh --plugins-dir dist/plugins --version 1.2.3 \
  --base-url https://example.com/dl -o dist/feed.yaml
```

拿生成的 `feed.yaml` 起一个静态服务器，在 upkit 的「来源」面板里添加它，
就能验证安装/更新流程。

