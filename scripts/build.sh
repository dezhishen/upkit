#!/usr/bin/env bash
#
# upkit 构建脚本（纯 bash，可在 Linux / macOS / Git Bash / WSL 下运行）。
#
# 用法：
#   bash scripts/build.sh                      # 构建全部默认目标并打包
#   bash scripts/build.sh -v v1.0.0            # 指定版本号
#   bash scripts/build.sh -t "windows/amd64"   # 只构建指定目标
#   bash scripts/build.sh -o out --no-zip      # 自定义输出目录且不打包
#
# 输出：
#   dist/upkit-windows-<arch>[.exe]                原始可执行文件
#   dist/upkit-<version>-<os>-<arch>.zip             Windows 发行包（含文档与示例配置）
#   dist/sha256sums.txt                             所有产物的校验值
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

PACKAGE="./cmd/upkit"
OUT_DIR="dist"
# upkit 只发行 Windows 版本；要用其它平台产物在开发机上冒烟，用 -t 显式指定。
TARGETS="windows/amd64 windows/arm64"
# 单文件体积门禁（MB）。插件子系统引入了 hashicorp/go-plugin，
# 而它硬依赖 grpc + protobuf，因此基线比无插件时高；可用 MAX_EXE_MB 覆盖。
MAX_EXE_MB="${MAX_EXE_MB:-24}"
MAKE_ZIP=1
VERSION=""
LDFLAGS_EXTRA=""
FEED_URL=""

usage() {
  sed -n '2,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

选项:
  -v, --version <ver>   指定版本号（默认取 git describe）
  -t, --targets "<list>"空格分隔的 GOOS/GOARCH 列表
  -o, --out <dir>       输出目录（默认 dist）
      --feed-url <url>  覆盖内置订阅地址（默认指向官方源 upkit-hub 仓库）
      --no-zip          只构建可执行文件，不生成 zip 发行包
  -h, --help            显示本帮助
EOF
}

# -ldflags -X 的变量全路径。故意写全，与 internal/pluginfeed/builtin.go 里的
# builtinFeedURLSymbol 保持一致（pluginfeed 的测试会校对这个字符串）。
FEED_URL_SYMBOL="github.com/dezhishen/upkit/internal/pluginfeed.BuiltinFeedURL"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -v|--version) VERSION="${2:-}"; shift 2 ;;
    -t|--targets) TARGETS="${2:-}"; shift 2 ;;
    -o|--out)     OUT_DIR="${2:-}"; shift 2 ;;
    --feed-url)   FEED_URL="${2:-}"; shift 2 ;;
    --no-zip)     MAKE_ZIP=0; shift ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# ── 版本信息 ──────────────────────────────────────────────────
if [[ -z "$VERSION" ]]; then
  if command -v git >/dev/null 2>&1; then
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
  else
    VERSION="dev"
  fi
fi

COMMIT="none"
if command -v git >/dev/null 2>&1; then
  COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
fi
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE} ${LDFLAGS_EXTRA}"

# 内置订阅地址：留空则用 Go 里的默认值（官方源仓库）。
if [[ -n "$FEED_URL" ]]; then
  LDFLAGS="${LDFLAGS} -X ${FEED_URL_SYMBOL}=${FEED_URL}"
fi

echo "==> upkit 构建"
echo "    版本:   ${VERSION}"
echo "    提交:   ${COMMIT}"
echo "    时间:   ${BUILD_DATE}"
echo "    Go:     $(go version)"
echo "    目标:   ${TARGETS}"
[[ -n "$FEED_URL" ]] && echo "    订阅源: ${FEED_URL}"

if ! command -v go >/dev/null 2>&1; then
  echo "错误: 未找到 go 命令，请先安装 Go 工具链" >&2
  exit 1
fi

rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}"

# ── 逐目标构建 ────────────────────────────────────────────────
built_files=()
for target in $TARGETS; do
  GOOS_NAME="${target%%/*}"
  GOARCH_NAME="${target##*/}"
  if [[ -z "$GOOS_NAME" || -z "$GOARCH_NAME" || "$GOOS_NAME" == "$GOARCH_NAME" ]]; then
    echo "错误: 目标格式应为 GOOS/GOARCH，收到 '$target'" >&2
    exit 2
  fi

  bin_name="upkit-${GOOS_NAME}-${GOARCH_NAME}"
  if [[ "$GOOS_NAME" == "windows" ]]; then
    bin_name="${bin_name}.exe"
  fi
  out_path="${OUT_DIR}/${bin_name}"

  echo "==> 构建 ${GOOS_NAME}/${GOARCH_NAME} -> ${out_path}"
  CGO_ENABLED=0 GOOS="$GOOS_NAME" GOARCH="$GOARCH_NAME" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out_path" "$PACKAGE"

  # 体积门禁：二进制突然变大通常是引入了意料之外的大依赖。
  size_mb=$(( $(wc -c < "$out_path" | tr -d ' ') / 1048576 ))
  printf '    体积:   %s MB（上限 %s MB）\n' "$size_mb" "$MAX_EXE_MB"
  if (( size_mb > MAX_EXE_MB )); then
    echo "错误: ${out_path} 体积 ${size_mb}MB 超过门禁 ${MAX_EXE_MB}MB" >&2
    echo "      确认合理时可用 MAX_EXE_MB=<新上限> 重新运行覆盖" >&2
    exit 1
  fi
  built_files+=("$out_path")
done

# ── 打包 Windows 发行包 ───────────────────────────────────────
package_windows() {
  local bin="$1"
  local arch="$2"
  local pkg_name="upkit-${VERSION}-windows-${arch}"
  local stage="${OUT_DIR}/${pkg_name}"

  mkdir -p "$stage"
  cp "$bin" "$stage/upkit.exe"
  cp README.md LICENSE "$stage/" 2>/dev/null || true
  mkdir -p "$stage/configs"
  cp configs/settings.example.yaml configs/apps.example.yaml "$stage/configs/" 2>/dev/null || true
  mkdir -p "$stage/scripts"
  cp scripts/*.sh "$stage/scripts/" 2>/dev/null || true

  if command -v zip >/dev/null 2>&1; then
    (cd "${OUT_DIR}" && zip -qr "${pkg_name}.zip" "${pkg_name}")
  else
    echo "    提示: 未找到 zip 命令，使用 tar.gz 代替"
    (cd "${OUT_DIR}" && tar -czf "${pkg_name}.tar.gz" "${pkg_name}")
  fi
  rm -rf "$stage"
}

if [[ "$MAKE_ZIP" -eq 1 ]]; then
  for f in "${built_files[@]}"; do
    case "$f" in
      *windows-*.exe)
        arch="$(basename "$f" .exe)"
        arch="${arch##*-}"
        echo "==> 打包 $(basename "$f")"
        package_windows "$f" "$arch"
        ;;
    esac
  done
fi

# ── 校验值 ────────────────────────────────────────────────────
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${OUT_DIR}" && find . -maxdepth 1 -type f ! -name 'sha256sums.txt' -printf '%P\n' \
    | sort | xargs -r sha256sum > sha256sums.txt)
elif command -v shasum >/dev/null 2>&1; then
  (cd "${OUT_DIR}" && find . -maxdepth 1 -type f ! -name 'sha256sums.txt' -print \
    | sed 's|^\./||' | sort | xargs -r shasum -a 256 > sha256sums.txt)
fi

echo
echo "==> 构建完成，产物位于 ${OUT_DIR}/"
ls -1 "${OUT_DIR}" 2>/dev/null | sed 's/^/    /'
echo
echo "提示: 直接运行 'bash scripts/build.sh --no-zip -t windows/amd64' 可获得单文件 dist/upkit-windows-amd64.exe"
