#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
VERSION="${VERSION:-$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo dev)}"

mkdir -p "$DIST_DIR"

target_cc() {
  local goos="$1"
  local goarch="$2"
  local goarm="${3:-}"

  if [[ "$goos" != "linux" ]]; then
    echo "gcc"
    return
  fi

  case "$goarch" in
    amd64) echo "gcc" ;;
    arm64) echo "aarch64-linux-gnu-gcc" ;;
    arm)
      if [[ "$goarm" == "7" ]]; then
        echo "arm-linux-gnueabihf-gcc"
      else
        echo "arm-linux-gnueabi-gcc"
      fi
      ;;
    *)
      echo "gcc"
      ;;
  esac
}

build_target() {
  local goos="$1"
  local goarch="$2"
  local goarm="${3:-}"
  local suffix="${goos}-${goarch}"
  local cc="${CC:-$(target_cc "$goos" "$goarch" "$goarm")}"
  if [[ -n "$goarm" ]]; then
    suffix="${suffix}v${goarm}"
  fi
  local out_dir="${DIST_DIR}/deploycp-${VERSION}-${suffix}"
  mkdir -p "$out_dir"
  if ! command -v "$cc" >/dev/null 2>&1; then
    echo "missing C compiler for ${goos}/${goarch}${goarm:+ v${goarm}}: ${cc}" >&2
    echo "install the required cross toolchain or set CC explicitly" >&2
    exit 1
  fi
  (
    cd "$ROOT_DIR"
    export CGO_ENABLED=1
    export GOOS="$goos"
    export GOARCH="$goarch"
    export CC="$cc"
    if [[ -n "$goarm" ]]; then export GOARM="$goarm"; fi
    go build -o "${out_dir}/deploycp" ./main.go
  )
  cp "${ROOT_DIR}/.env.example" "$out_dir/"
  mkdir -p "${out_dir}/scripts/linux" "${out_dir}/docs" "${out_dir}/frontend"
  cp "${ROOT_DIR}/scripts/linux/"*.sh "${out_dir}/scripts/linux/"
  cp "${ROOT_DIR}/readme.md" "$out_dir/"
  cp -R "${ROOT_DIR}/docs/." "${out_dir}/docs/"
  cp -R "${ROOT_DIR}/frontend/." "${out_dir}/frontend/"
  (
    cd "$DIST_DIR"
    tar -czf "deploycp-${VERSION}-${suffix}.tar.gz" "deploycp-${VERSION}-${suffix}"
    shasum -a 256 "deploycp-${VERSION}-${suffix}.tar.gz" > "deploycp-${VERSION}-${suffix}.tar.gz.sha256"
  )
}

build_target linux amd64
build_target linux arm64
build_target linux arm 7
