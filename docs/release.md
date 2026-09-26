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
- 预览版在 GitHub 上标记为 **pre-release**，因此**不会**成为 `releases/latest`，
  也就不会自动覆盖 `upkit-hub` 仓库里的正式清单 —— 把预览版的清单推过去需要你显式
  去做（见 §4）。

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
   - `dry_run`：勾上就只演练（构建 + 生成清单，不打 tag、不发版）——**第一次用建议先演练一次**
3. 运行结束后去 Releases 页面核对产物与 changelog。

流水线的四个 job：

```
verify（gofmt / vet / test）
  └─ version（算版本 → 打 tag → 算 changelog 起点）
       └─ build（upkit ×2 架构 + 示例插件 ×2 架构 + 生成 feed.yaml）
            └─ publish（gh release create，附带全部产物）
```

`verify` 会在发布前再跑一遍测试：CI 虽然在 `main` 上过了，但 tag 有可能打在别处。

### main 上的发布演练

推送到 `main`（以及开 PR）时，`ci.yml` 会自动跑一次**不推 release 的构建**：

```
test（gofmt / vet / test -race + 覆盖率）
  ├─ build（两个架构的交叉编译快检，matrix 并行）
  └─ smoke（发布演练：真构建全部产物 + 生成 feed.yaml，产物传 artifact）
```

`smoke` 与真发版的 `build` **调用同一个可复用工作流**
（`.github/workflows/build-release.yml`），因此两边不会漂移 ——
「main 上是绿的」就意味着「现在能发版」。它**不碰 tag、不碰 Release**，
产物（含演练版 `feed.yaml`）放在名为 `release-smoke` 的 artifact 里供下载核对，
演练版本号形如 `v0.0.0-dev.<commit sha>`。

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
| `upkit-hub-windows-{amd64,arm64}.exe` | 示例插件 |
| `feed.yaml` | **官方订阅清单**，见下节 |

### 想下载一个 exe 来试，去哪找

CI 的 artifact 分两处，用途不同：

| 来源 | artifact 名 | 内容 | 保留 |
| --- | --- | --- | --- |
| `ci` 的 **build** job | `upkit-windows-amd64` / `upkit-windows-arm64` | 只有主程序 exe | 7 天 |
| `ci` 的 **smoke** job | `release-smoke` | 完整的发布产物（exe + 插件 + `feed.yaml`） | 仓库默认 |
| `release` 的 **publish** | 无 artifact，直接进 [Releases](../../releases) | 同上 | 永久 |

下载路径：仓库 → **Actions** → 选一次运行 → 页面底部 **Artifacts**。

两个 CI job 都排在 `test` 之后：**测试不过就不会有可下载的产物**。所以 artifact
列表为空时，先看同一次运行里 `test` 是不是红的。

日常只是想拿个能跑的 exe 手动点一点，用 **build** 的产物就够了 —— 每次 push 都会
重新构建，artifact 名里就是目标平台。要验完整的发行流程（含插件与订阅清单），
或者要对照 release 内容，才需要 **smoke** 的 `release-smoke`。

这些 exe 带版本信息（`v0.0.0-dev.<短提交>`），`upkit.exe --version` 能看出是哪个
提交构建的。

## 4. feed.yaml 从哪来

`feed.yaml` 由 `scripts/gen-feed.sh` 在构建之后生成（摘要由产物现算，杜绝手工填错），
它有两个去处：

1. **作为本仓库 release 的附件发布** —— 内容与本次发布严格对应，便于追溯；
2. **提交到 `dezhishen/upkit-hub`** —— 内置订阅实际读的是那个仓库里的清单：

```
https://raw.githubusercontent.com/dezhishen/upkit-hub/main/feed.yaml
```

清单与产物分开放是有意的：产物跟着 tag 走（不可变），清单需要能独立修正
（比如某个 sha256 填错了，不必为此重发一个版本）。

清单里的产物地址指向**本仓库**的 release 下载地址
（`https://github.com/dezhishen/upkit/releases/download/<tag>/...`），所以提交到
`upkit-hub` 时不需要动 URL，只要把生成的文件整个拷过去。

脚本在生成前会检查**每个插件是否覆盖了全部受支持平台**：漏一个架构，那个架构的用户
会在「校验订阅」这一步失败，而这本可以在发布前发现。确实有意只发部分架构时，用
`--allow-partial` 显式跳过。

`internal/pluginfeed` 里有测试盯着这条链路：脚本产出的清单必须能被宿主解析、
通过校验、摘要与产物一致。脚本里写死的平台列表也有一致性检查，与代码漂移会报警。

### 换成自己的订阅源

内置地址可在构建时覆盖，不必改代码：

```bash
bash scripts/build.sh --feed-url https://mirror.internal/upkit/feed.yaml
make build-all FEED_URL=https://mirror.internal/upkit/feed.yaml
```

`-X` 打错变量路径不会报错、只会静默保留默认值，所以 `internal/pluginfeed` 里有测试
用真实的 `-ldflags` 构建一次探针来验证覆盖生效。

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

`publish` 是幂等的（`gh release upload --clobber` + `gh release edit`），所以重跑安全。

## 7. 本地能做的

```bash
make next-version BUMP=minor STAGE=rc    # 看看下一个版本号是多少
bash scripts/build.sh --version v1.2.3   # 本地构建
bash scripts/build-plugin.sh --release-name -v 1.2.3 -t windows/amd64 ./cmd/upkit-hub
bash scripts/gen-feed.sh --plugins-dir dist/plugins --version 1.2.3 \
  --base-url https://example.com/dl -o dist/feed.yaml
```

最后这条可以在本地把订阅链路走通：拿生成的 `feed.yaml` 起一个静态服务器，
在 upkit 的「来源」面板里添加它，就能验证安装/更新流程。
