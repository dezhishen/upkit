#!/usr/bin/env bash
# 计算下一个版本号。
#
# 用法: bash scripts/next-version.sh <bump> <stage>
#
#   bump:  major | minor | patch | hotfix
#   stage: stable | rc | beta
#
# 输出: 形如 v1.2.3 或 v1.3.0-rc.1 的版本号（不带换行以外的任何装饰）。
#
# 规则与理由：
#
#   · 一切从「最近的正式版 tag」起算。预发布不算基线 —— 否则 v1.2.0-rc.1 之后
#     再算 patch 会得到 v1.2.0-rc.2（错），而不是 v1.2.1。
#   · hotfix 与 patch 算出来的是同一个位置（补丁位 +1），分开只是为了让发布记录
#     与 changelog 能看出「这是修补已发布版本」还是「这是主线的常规迭代」。
#   · rc / beta 建立在「本次 bump 的结果」之上，并自动递增序号：已经有
#     v1.3.0-rc.1 时，再发一次 rc 得到 v1.3.0-rc.2。
#   · 仓库里一个 tag 都没有时，基线按 v0.0.0 处理：patch → v0.0.1、
#     minor → v0.1.0、major → v1.0.0。

set -euo pipefail

BUMP="${1:-patch}"
STAGE="${2:-stable}"

die() {
  echo "错误: $*" >&2
  exit 1
}

case "$BUMP" in
  major|minor|patch|hotfix) ;;
  *) die "bump 只能是 major / minor / patch / hotfix，收到 '$BUMP'" ;;
esac
case "$STAGE" in
  stable|rc|beta) ;;
  *) die "stage 只能是 stable / rc / beta，收到 '$STAGE'" ;;
esac

# ── 最近一个正式版 tag ────────────────────────────────────────
# 只认 vX.Y.Z 这种三段式，预发布（带 -）与其它形状的 tag 一律忽略。
latest_stable=""
if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
  latest_stable="$(git tag --list 'v*' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sort -V \
    | tail -n 1 || true)"
fi
[[ -n "$latest_stable" ]] || latest_stable="v0.0.0"

base="${latest_stable#v}"
IFS='.' read -r major minor patch <<<"$base"
[[ -n "${major:-}" && -n "${minor:-}" && -n "${patch:-}" ]] || die "无法解析基线版本 '$latest_stable'"

case "$BUMP" in
  major) major=$((major + 1)); minor=0; patch=0 ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  patch|hotfix) patch=$((patch + 1)) ;;
esac

version="v${major}.${minor}.${patch}"

# ── 预发布序号 ────────────────────────────────────────────────
if [[ "$STAGE" != "stable" ]]; then
  last_n=""
  if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
    last_n="$(git tag --list "v${major}.${minor}.${patch}-${STAGE}.*" \
      | sed -E "s/^v[0-9]+\.[0-9]+\.[0-9]+-${STAGE}\.([0-9]+)$/\1/" \
      | grep -E '^[0-9]+$' \
      | sort -n \
      | tail -n 1 || true)"
  fi
  version="${version}-${STAGE}.$(( ${last_n:-0} + 1 ))"
fi

printf '%s\n' "$version"
