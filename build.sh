#!/usr/bin/env bash
# Autonomous Pentest Agent cross-platform release builder.
#
# The default mode builds one target and embeds the already-exported frontend.
# `./build.sh --release` builds and packages all supported desktop/server targets.
#
# Environment variables:
#   JCYBER_TARGET_OS=linux             One target OS in single-target mode.
#   JCYBER_TARGET_ARCH=amd64           One target arch in single-target mode.
#   JCYBER_TARGETS=linux/amd64,...     Comma-separated targets for multi-target mode.
#   JCYBER_BUILD_VERSION=v0.3.3        Version embedded in the binary and archive name.
#   JCYBER_OUTPUT=/path/to/jcyber        Explicit binary path in single-target mode.
#   JCYBER_OUTPUT_DIR=dist             Directory for default binary paths.
#   JCYBER_PACKAGE=1                   Create a zip archive for each target.
#   JCYBER_PACKAGE_DIR=dist            Directory for release archives.
#   JCYBER_COMPRESS=off                UPX mode: off, auto, or required.
#   JCYBER_UPX_ARGS="--best --lzma"    Arguments passed to UPX.
#   JCYBER_SKIP_FRONTEND=1             Reuse server/webui/dist (for CI artifact builds).
#   JCYBER_SKIP_NPM_CI=1               Skip npm ci while rebuilding the frontend.
#   JCYBER_GOSUMDB=sum.golang.org      Go checksum database.
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok() { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
用法：
  ./build.sh                         编译当前系统当前架构
  ./build.sh --target linux/amd64   编译一个指定目标
  ./build.sh --release               编译并打包全部支持的目标

选项：
  --release              构建 Linux、macOS、Windows 的 amd64/arm64 目标并生成 zip
  --target OS/ARCH       设置单个目标，例如 windows/amd64
  --upx                  强制使用 UPX 压缩二进制（可能影响部分 Linux 环境兼容性）
  --no-compress          不使用 UPX，仅使用 Go linker 裁剪并压缩 zip
  --help                 显示帮助

多目标列表可通过 JCYBER_TARGETS 覆盖，例如：
  JCYBER_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
EOF
}

RELEASE_TARGETS_DEFAULT="linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64"
JCYBER_RELEASE="${JCYBER_RELEASE:-0}"
JCYBER_COMPRESS="${JCYBER_COMPRESS:-off}"
JCYBER_PACKAGE="${JCYBER_PACKAGE:-0}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --release)
      JCYBER_RELEASE=1
      JCYBER_PACKAGE=1
      shift
      ;;
    --target)
      [ "$#" -ge 2 ] || die "--target 需要 OS/ARCH 参数"
      target_arg="$2"
      case "$target_arg" in
        */*)
          JCYBER_TARGET_OS="${target_arg%%/*}"
          JCYBER_TARGET_ARCH="${target_arg##*/}"
          JCYBER_TARGETS="$target_arg"
          ;;
        *) die "目标必须是 OS/ARCH，例如 linux/amd64" ;;
      esac
      shift 2
      ;;
    --no-compress)
      JCYBER_COMPRESS=0
      shift
      ;;
    --upx)
      JCYBER_COMPRESS=required
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *) die "未知参数：$1（使用 --help 查看用法）" ;;
  esac
done

command -v go >/dev/null 2>&1 || die "未检测到 Go（项目需要 Go 1.26 或更高版本）"

JCYBER_GOSUMDB="${JCYBER_GOSUMDB:-sum.golang.org}"
if [ -z "${JCYBER_BUILD_VERSION:-}" ]; then
  if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    JCYBER_BUILD_VERSION="$(git describe --tags --always --dirty)"
  else
    JCYBER_BUILD_VERSION="dev"
  fi
fi
# Release tags are commonly passed as v0.3.3; keep the binary version consistent.
JCYBER_BUILD_VERSION="${JCYBER_BUILD_VERSION#v}"
JCYBER_OUTPUT_DIR="${JCYBER_OUTPUT_DIR:-dist}"
JCYBER_PACKAGE_DIR="${JCYBER_PACKAGE_DIR:-$JCYBER_OUTPUT_DIR}"
JCYBER_UPX_ARGS="${JCYBER_UPX_ARGS:---best --lzma}"

if [ "${JCYBER_RELEASE}" = "1" ]; then
  JCYBER_TARGETS="${JCYBER_TARGETS:-$RELEASE_TARGETS_DEFAULT}"
else
  JCYBER_TARGET_OS="${JCYBER_TARGET_OS:-$(GOSUMDB="$JCYBER_GOSUMDB" go env GOOS)}"
  JCYBER_TARGET_ARCH="${JCYBER_TARGET_ARCH:-$(GOSUMDB="$JCYBER_GOSUMDB" go env GOARCH)}"
  JCYBER_TARGETS="${JCYBER_TARGETS:-${JCYBER_TARGET_OS}/${JCYBER_TARGET_ARCH}}"
fi

if [ "${JCYBER_SKIP_FRONTEND:-0}" = "1" ]; then
  [ -d server/webui/dist ] || die "JCYBER_SKIP_FRONTEND=1 但 server/webui/dist 不存在"
else
  command -v npm >/dev/null 2>&1 || die "未检测到 npm（前端静态构建需要 Node.js/npm）"
  command -v rsync >/dev/null 2>&1 || die "未检测到 rsync"
  info "构建前端静态资源"
  if [ "${JCYBER_SKIP_NPM_CI:-0}" != "1" ]; then
    (cd web && npm ci)
  fi
  (cd web && npm run build:static)
  info "同步前端资源到 server/webui/dist"
  mkdir -p server/webui/dist
  rsync -a --delete web/out/ server/webui/dist/
fi

compress_binary() {
  binary="$1"
  goos="$2"
  case "$JCYBER_COMPRESS" in
    0|off|false|none)
      info "跳过 UPX：$binary"
      return 0
      ;;
    auto|required|true|1) ;;
    *) die "JCYBER_COMPRESS 必须是 off、auto 或 required" ;;
  esac

  if ! command -v upx >/dev/null 2>&1; then
    if [ "$JCYBER_COMPRESS" = "required" ]; then
      die "JCYBER_COMPRESS=required 但未检测到 upx"
    fi
    warn "未检测到 upx，保留 linker 压缩结果：$binary"
    return 0
  fi

  before=$(wc -c < "$binary" | tr -d ' ')
  upx_args="$JCYBER_UPX_ARGS"
  [ "$goos" = "darwin" ] && upx_args="$upx_args --force-macos"
  # shellcheck disable=SC2086
  if ! upx $upx_args -- "$binary"; then
    if [ "$JCYBER_COMPRESS" = "required" ]; then
      die "UPX 压缩失败：$binary"
    fi
    warn "UPX 不支持该目标格式，保留未压缩二进制：$binary"
    return 0
  fi
  after=$(wc -c < "$binary" | tr -d ' ')
  ok "UPX 压缩完成：$binary (${before} -> ${after} bytes)"
}

package_binary() {
  binary="$1"
  goos="$2"
  goarch="$3"
  package_name="jcyber-${JCYBER_BUILD_VERSION}-${goos}-${goarch}"
  package_root="${JCYBER_PACKAGE_DIR}/${package_name}"
  archive="${JCYBER_PACKAGE_DIR}/${package_name}.zip"

  command -v zip >/dev/null 2>&1 || die "打包需要 zip"
  rm -rf "$package_root" "$archive"
  mkdir -p "$package_root"
  cp "$binary" "$package_root/"
  # 守护启动脚本是正式入口：页面上的一键更新要靠它在进程退出后重新拉起，
  # 直接跑 jcyber 本体的话更新完就再也起不来了。按目标系统只带对应的那一份。
  if [ "$goos" = "windows" ]; then
    cp start.bat "$package_root/"
  else
    cp start.sh "$package_root/"
    chmod +x "$package_root/start.sh"
  fi
  cp -R skills "$package_root/"
  cp config.example.json "$package_root/"
  if [ -f README.md ]; then cp README.md "$package_root/"; fi
  (cd "$JCYBER_PACKAGE_DIR" && zip -q -r -9 "$(basename "$archive")" "$(basename "$package_root")")
  rm -rf "$package_root"
  ok "Release 压缩包：$archive"
}

build_target() {
  target="$1"
  case "$target" in
    */*) ;;
    *) die "无效目标：$target（必须是 OS/ARCH）" ;;
  esac
  goos="${target%%/*}"
  goarch="${target##*/}"
  case "$goos" in
    linux|darwin|windows) ;;
    *) die "不支持的系统：$goos（支持 linux、darwin、windows）" ;;
  esac

  binary_name="jcyber"
  [ "$goos" = "windows" ] && binary_name="jcyber.exe"
  if [ -n "${JCYBER_OUTPUT:-}" ] && [ "$JCYBER_RELEASE" != "1" ]; then
    output="$JCYBER_OUTPUT"
  else
    output="${JCYBER_OUTPUT_DIR}/jcyber-${goos}-${goarch}/${binary_name}"
  fi
  mkdir -p "$(dirname "$output")"

  info "编译 ${goos}/${goarch}，版本 ${JCYBER_BUILD_VERSION}"
  GOSUMDB="$JCYBER_GOSUMDB" \
  CGO_ENABLED=0 \
  GOOS="$goos" \
  GOARCH="$goarch" \
  go build \
    -tags embedui \
    -trimpath \
    -ldflags "-s -w -buildid= -X main.version=${JCYBER_BUILD_VERSION}" \
    -o "$output" \
    ./cmd/jcyber

  compress_binary "$output" "$goos"
  if command -v file >/dev/null 2>&1; then file "$output"; fi
  if [ "$JCYBER_PACKAGE" = "1" ]; then package_binary "$output" "$goos" "$goarch"; fi
  ok "编译完成：$output"
}

write_checksums() {
  [ "$JCYBER_PACKAGE" = "1" ] || return 0
  checksum_file="$JCYBER_PACKAGE_DIR/SHA256SUMS"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$JCYBER_PACKAGE_DIR" && for archive in *.zip; do sha256sum "$archive"; done > "$(basename "$checksum_file")")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$JCYBER_PACKAGE_DIR" && for archive in *.zip; do shasum -a 256 "$archive"; done > "$(basename "$checksum_file")")
  else
    warn "未检测到 sha256sum 或 shasum，跳过 SHA256SUMS"
    return 0
  fi
  ok "校验文件：$checksum_file"
}

mkdir -p "$JCYBER_OUTPUT_DIR"
if [ "$JCYBER_PACKAGE" = "1" ]; then mkdir -p "$JCYBER_PACKAGE_DIR"; fi

old_ifs="$IFS"
IFS=','
read -r -a targets <<< "$JCYBER_TARGETS"
IFS="$old_ifs"
[ "${#targets[@]}" -gt 0 ] || die "JCYBER_TARGETS 不能为空"
for target in "${targets[@]}"; do
  target="${target//[[:space:]]/}"
  [ -n "$target" ] || continue
  build_target "$target"
done

if [ "$JCYBER_PACKAGE" = "1" ]; then
  write_checksums
  info "Release 包已生成于：$JCYBER_PACKAGE_DIR"
fi
