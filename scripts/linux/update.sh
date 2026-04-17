#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
SERVICE_NAME="${SERVICE_NAME:-deploycp}"
BIN_NAME="${BIN_NAME:-deploycp}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

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

chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}"
set_env_value "${CORE_DIR}/.env" "APP_VERSION" "$(resolved_release_version)"
set_env_value "${CORE_DIR}/.env" "DEPLOYCP_REPO" "${DEPLOYCP_REPO:-saiarlen/deployCP}"
systemctl daemon-reload
"${CORE_DIR}/bin/${BIN_NAME}" bootstrap-host
systemctl start "${SERVICE_NAME}"
"${CORE_DIR}/bin/${BIN_NAME}" reconcile-managed
"${CORE_DIR}/bin/${BIN_NAME}" verify-host || true
systemctl status "${SERVICE_NAME}" --no-pager || true
