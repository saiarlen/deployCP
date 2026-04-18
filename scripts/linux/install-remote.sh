#!/usr/bin/env bash
set -euo pipefail

REPO="${DEPLOYCP_REPO:-saiarlen/deployCP}"
VERSION="${DEPLOYCP_VERSION:-}"
ACTION="install"

if [[ "${1:-}" == "--update" ]]; then
  ACTION="update"
fi

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need_cmd curl
need_cmd tar
need_cmd uname
need_cmd mktemp

checksum_tool() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum"
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    echo "shasum -a 256"
    return
  fi
  if command -v openssl >/dev/null 2>&1; then
    echo "openssl dgst -sha256"
    return
  fi
  echo ""
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "linux-amd64" ;;
    aarch64|arm64) echo "linux-arm64" ;;
    armv7l|armv7|armhf) echo "linux-armv7" ;;
    *)
      echo "unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

resolve_version() {
  if [[ -n "$VERSION" ]]; then
    echo "$VERSION"
    return
  fi

  local latest
  latest="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  if [[ -z "$latest" ]]; then
    echo "unable to resolve latest release tag from GitHub for ${REPO}" >&2
    exit 1
  fi
  echo "$latest"
}

ARCH_SUFFIX="$(detect_arch)"
VERSION="$(resolve_version)"
export DEPLOYCP_VERSION="$VERSION"
export DEPLOYCP_REPO="$REPO"
ASSET="deploycp-${VERSION}-${ARCH_SUFFIX}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading ${ASSET} from ${DOWNLOAD_URL}"
curl -fL "$DOWNLOAD_URL" -o "${TMP_DIR}/${ASSET}"
echo "Downloading checksum from ${CHECKSUM_URL}"
curl -fL "$CHECKSUM_URL" -o "${TMP_DIR}/${ASSET}.sha256"

CHECKSUM_CMD="$(checksum_tool)"
if [[ -z "$CHECKSUM_CMD" ]]; then
  echo "missing checksum tool: need sha256sum, shasum, or openssl" >&2
  exit 1
fi

echo "Verifying ${ASSET}"
(
  cd "$TMP_DIR"
  case "$CHECKSUM_CMD" in
    "sha256sum")
      sha256sum -c "${ASSET}.sha256"
      ;;
    "shasum -a 256")
      shasum -a 256 -c "${ASSET}.sha256"
      ;;
    "openssl dgst -sha256")
      expected="$(awk '{print $1}' "${ASSET}.sha256")"
      actual="$(openssl dgst -sha256 "${ASSET}" | awk '{print $NF}')"
      if [[ "$expected" != "$actual" ]]; then
        echo "checksum verification failed for ${ASSET}" >&2
        exit 1
      fi
      ;;
  esac
)

tar -xzf "${TMP_DIR}/${ASSET}" -C "$TMP_DIR"

PKG_DIR="${TMP_DIR}/deploycp-${VERSION}-${ARCH_SUFFIX}"
if [[ ! -d "$PKG_DIR" ]]; then
  echo "unexpected package layout: ${PKG_DIR} not found" >&2
  exit 1
fi

cd "$PKG_DIR"
case "$ACTION" in
  install)
    exec bash ./scripts/linux/install.sh
    ;;
  update)
    exec bash ./scripts/linux/update.sh
    ;;
  *)
    echo "unsupported action: ${ACTION}" >&2
    exit 1
    ;;
esac
