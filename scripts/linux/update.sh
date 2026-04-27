#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
SERVICE_NAME="${SERVICE_NAME:-deploycp}"
BIN_NAME="${BIN_NAME:-deploycp}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

detect_pkg_manager() {
  if command -v apt-get >/dev/null 2>&1; then echo apt; return; fi
  if command -v dnf >/dev/null 2>&1; then echo dnf; return; fi
  if command -v yum >/dev/null 2>&1; then echo yum; return; fi
  if command -v zypper >/dev/null 2>&1; then echo zypper; return; fi
  if command -v pacman >/dev/null 2>&1; then echo pacman; return; fi
  echo ""
}

package_available() {
  local manager="$1"
  local pkg="$2"
  case "$manager" in
    apt) apt-cache show "$pkg" >/dev/null 2>&1 ;;
    dnf) dnf info "$pkg" >/dev/null 2>&1 ;;
    yum) yum info "$pkg" >/dev/null 2>&1 ;;
    zypper) zypper --non-interactive info "$pkg" >/dev/null 2>&1 ;;
    pacman) pacman -Si "$pkg" >/dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

install_named_packages() {
  local manager="$1"
  shift
  [[ $# -gt 0 ]] || return 0
  case "$manager" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get install -y "$@"
      ;;
    dnf) dnf install -y "$@" ;;
    yum) yum install -y "$@" ;;
    zypper) zypper --non-interactive install "$@" ;;
    pacman) pacman -Sy --noconfirm "$@" ;;
  esac
}

install_optional_packages() {
  local manager="$1"
  shift
  local pkg available=()
  for pkg in "$@"; do
    if package_available "$manager" "$pkg"; then
      available+=("$pkg")
    fi
  done
  [[ ${#available[@]} -gt 0 ]] || return 0
  install_named_packages "$manager" "${available[@]}"
}

install_first_available_package() {
  local manager="$1"
  shift
  local pkg
  for pkg in "$@"; do
    if package_available "$manager" "$pkg"; then
      install_named_packages "$manager" "$pkg"
      return 0
    fi
  done
  return 0
}

install_db_ui_helper_packages() {
  local manager="$1"
  install_first_available_package "$manager" php-cli php8-cli php php8
  install_optional_packages "$manager" adminer pgweb
}

resolved_release_version() {
  local candidate="${DEPLOYCP_VERSION:-}"
  if [[ -n "$candidate" ]]; then
    echo "$candidate"
    return
  fi
  candidate="$(basename "$PACKAGE_ROOT")"
  if [[ "$candidate" =~ ^deploycp-(v[^-]+)-linux- ]]; then
    echo "${BASH_REMATCH[1]}"
    return
  fi
  echo ""
}

set_env_value() {
  local file="$1"
  local key="$2"
  local value="$3"
  if [[ ! -f "$file" ]]; then
    return
  fi
  if grep -q "^${key}=" "$file"; then
    sed -i.bak "s|^${key}=.*|${key}=${value}|" "$file"
    rm -f "${file}.bak"
  else
    printf '%s=%s\n' "$key" "$value" >>"$file"
  fi
}

stage_release_binary() {
  local candidate=""
  local target="${CORE_DIR}/bin/${BIN_NAME}"
  local tmp_target="${target}.new"
  for candidate in "${PACKAGE_ROOT}/${BIN_NAME}" "$(pwd)/${BIN_NAME}"; do
    if [[ -x "$candidate" && "$candidate" != "$target" ]]; then
      mkdir -p "$(dirname "$target")"
      cp "$candidate" "$tmp_target"
      chmod 0755 "$tmp_target"
      chown "${APP_USER}:${APP_USER}" "$tmp_target"
      mv -f "$tmp_target" "$target"
      return 0
    fi
  done
  return 1
}

stage_release_assets() {
  local candidate=""

  for candidate in "${PACKAGE_ROOT}/frontend" "$(pwd)/frontend"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/frontend" ]]; then
      mkdir -p "${CORE_DIR}/frontend"
      cp -R "${candidate}/." "${CORE_DIR}/frontend/"
      chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/frontend"
      break
    fi
  done

  for candidate in "${PACKAGE_ROOT}/docs" "$(pwd)/docs"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/docs" ]]; then
      mkdir -p "${CORE_DIR}/docs"
      cp -R "${candidate}/." "${CORE_DIR}/docs/"
      chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/docs"
      break
    fi
  done

  for candidate in "${PACKAGE_ROOT}/scripts/linux" "$(pwd)/scripts/linux"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/scripts/linux" ]]; then
      mkdir -p "${CORE_DIR}/scripts/linux"
      cp -R "${candidate}/." "${CORE_DIR}/scripts/linux/"
      find "${CORE_DIR}/scripts/linux" -type f -name '*.sh' -exec chmod 0755 {} +
      chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/scripts"
      break
    fi
  done
}

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

systemctl stop "${SERVICE_NAME}" || true

if ! stage_release_binary; then
  echo "release binary not found in update package" >&2
  exit 1
fi
stage_release_assets

if [[ ! -x "${CORE_DIR}/bin/${BIN_NAME}" ]]; then
  echo "binary not found at ${CORE_DIR}/bin/${BIN_NAME}" >&2
  exit 1
fi

pkg_manager="$(detect_pkg_manager)"
install_db_ui_helper_packages "$pkg_manager"
chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}"
set_env_value "${CORE_DIR}/.env" "APP_VERSION" "$(resolved_release_version)"
set_env_value "${CORE_DIR}/.env" "DEPLOYCP_REPO" "${DEPLOYCP_REPO:-saiarlen/deployCP}"
set_env_value "${CORE_DIR}/.env" "ADMINER_URL" "http://127.0.0.1:8081"
set_env_value "${CORE_DIR}/.env" "POSTGRES_GUI_URL" "http://127.0.0.1:8082"
if [[ -x "${CORE_DIR}/scripts/linux/harden-host.sh" ]]; then
  bash "${CORE_DIR}/scripts/linux/harden-host.sh"
fi
systemctl daemon-reload
(
  cd "${CORE_DIR}"
  DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" "${CORE_DIR}/bin/${BIN_NAME}" bootstrap-host
)
systemctl start "${SERVICE_NAME}"
(
  cd "${CORE_DIR}"
  DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" "${CORE_DIR}/bin/${BIN_NAME}" reconcile-managed
)
(
  cd "${CORE_DIR}"
  DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" "${CORE_DIR}/bin/${BIN_NAME}" verify-host
) || true
systemctl status "${SERVICE_NAME}" --no-pager || true
