#!/usr/bin/env bash
# AssetSpider 多平台打包脚本：本地与 CI 共用。
#
# 用法：
#   ./scripts/package.sh                 # 构建前端并打全部平台包
#   ./scripts/package.sh linux/arm64     # 只打单个平台（CI matrix 用）
#   SKIP_WEB=1 ./scripts/package.sh ...  # 复用已有 web/dist（CI 已单独构建前端）
#
# 环境变量：
#   PLATFORM   单个目标，格式 GOOS/GOARCH，如 linux/amd64；留空表示全部
#   SKIP_WEB   为 1 时跳过 npm 构建前端，要求 web/dist 已存在
#   OUT_DIR    产物输出目录，默认 release/
#   VERSION    写入 VERSION 文件与包名的版本号，默认取 git 短哈希
#
# SQLite 为纯 Go 驱动，CGO_ENABLED=0 可静态交叉编译，产物不依赖目标机 libc。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT_DIR="${OUT_DIR:-$ROOT/release}"
VERSION="${VERSION:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}"
SKIP_WEB="${SKIP_WEB:-0}"
PLATFORM="${PLATFORM:-${1:-}}"

# 全部目标：飞牛 x86-64 与通用 Linux 64 位同为 linux/amd64；
# 飞牛 ARM（RK3588/3399 等 aarch64）为 linux/arm64；另有 32 位 Linux 与 Windows。
ALL_PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "linux/386"
  "windows/amd64"
)

log() { printf '[package] %s\n' "$*"; }
die() { printf '[package] 错误: %s\n' "$*" >&2; exit 1; }

build_web() {
  if [[ "$SKIP_WEB" == "1" ]]; then
    [[ -f web/dist/index.html ]] || die "SKIP_WEB=1 但 web/dist 不存在，请先构建前端"
    log "跳过前端构建，复用已有 web/dist"
    return
  fi
  log "构建 Vue 前端"
  command -v npm >/dev/null || die "未找到 npm，无法构建前端"
  (cd web && npm ci --no-audit --no-fund && npm run build)
}

# 打包单个平台：参数 GOOS/GOARCH。
package_one() {
  local target="$1"
  local goos="${target%/*}"
  local goarch="${target#*/}"
  local suffix="" exe="assetspider" archive_fmt="tar.gz"

  case "$goos/$goarch" in
    windows/amd64) exe="assetspider.exe"; archive_fmt="zip" ;;
    linux/amd64|linux/arm64|linux/386) ;;
    *) die "不支持的目标平台: $target" ;;
  esac

  local name="assetspider-${goos}-${goarch}${suffix}"
  local stage="$OUT_DIR/$name"
  local archive="$OUT_DIR/$name.$archive_fmt"
  # 裸二进制单独命名，供 GitHub Release 直接下载（无需解压整个包）。
  local bare="assetspider-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && bare="${bare}.exe"

  log "编译 $target → $name.$archive_fmt"
  rm -rf "$stage"
  mkdir -p "$stage/conf" "$stage/web"

  # -trimpath 去掉本机路径，-s -w 去符号表与调试信息，减小体积。
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags='-s -w' -o "$stage/$exe" .

  cp -a web/dist "$stage/web/dist"
  cp conf/config.yaml "$stage/conf/config.yaml"
  printf '%s\n' "$VERSION" > "$stage/VERSION"
  cat > "$stage/README.txt" <<EOF
AssetSpider $VERSION ($target)

解压后在本目录直接运行：
  Linux : ./assetspider -config conf/config.yaml
  Windows: assetspider.exe -config conf\\config.yaml

前端已内置于 web/dist，数据库默认生成在 data/assetspider.db。
首次访问 http://<主机>:8080 注册的账号自动成为管理员。
完整配置说明见 conf/config.yaml 注释。
EOF

  # 压缩包内保留一份二进制；同时在 OUT_DIR 根目录单独留一份裸二进制。
  # 裸二进制与 stage 目录可能同名（Linux），先存临时名，等 stage 删除后再改名。
  local tmp_bin="$OUT_DIR/.$bare.tmp"
  cp "$stage/$exe" "$tmp_bin"
  chmod 0755 "$tmp_bin"

  rm -f "$archive"
  if [[ "$archive_fmt" == "zip" ]]; then
    (cd "$OUT_DIR" && zip -qr "$name.zip" "$name")
  else
    tar -czf "$archive" -C "$OUT_DIR" "$name"
  fi
  rm -rf "$stage"
  mv -f "$tmp_bin" "$OUT_DIR/$bare"
  log "完成: $archive ($(du -h "$archive" | cut -f1)) + 裸二进制 $bare"
}

main() {
  mkdir -p "$OUT_DIR"
  build_web

  local targets=()
  if [[ -n "$PLATFORM" ]]; then
    targets=("$PLATFORM")
  else
    targets=("${ALL_PLATFORMS[@]}")
  fi
  for target in "${targets[@]}"; do
    package_one "$target"
  done
  log "全部产物已输出到 $OUT_DIR"
  ls -lh "$OUT_DIR"
}

main "$@"
