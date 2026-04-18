#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
DATA_DIR="${DATA_DIR:-${APP_HOME}/platforms}"
ENV_FILE="${ENV_FILE:-${CORE_DIR}/.env}"

read_env_value() {
  local file="$1"
  local key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

env_or_default() {
  local key="$1"
  local fallback="$2"
  local value
  value="$(read_env_value "$ENV_FILE" "$key")"
  if [[ -n "$value" ]]; then
    printf '%s\n' "$value"
    return
  fi
  printf '%s\n' "$fallback"
}

normalize_bool() {
  case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on) echo "true" ;;
    *) echo "false" ;;
  esac
}

run_hook() {
  local hook_path="$1"
  if [[ -z "$hook_path" ]]; then
    return 0
  fi
  if [[ ! -x "$hook_path" ]]; then
    echo "backup hook is not executable: $hook_path" >&2
    return 1
  fi
  "$hook_path"
}

TARGET_DIR="$(env_or_default "BACKUP_TARGET_DIR" "${DATA_DIR}/backups")"
RETENTION_DAYS="$(env_or_default "BACKUP_RETENTION_DAYS" "14")"
INCLUDE_SITE_CONTENT="$(normalize_bool "$(env_or_default "BACKUP_INCLUDE_SITE_CONTENT" "true")")"
INCLUDE_PLATFORM_LOGS="$(normalize_bool "$(env_or_default "BACKUP_INCLUDE_PLATFORM_LOGS" "false")")"
PRE_HOOK="$(env_or_default "BACKUP_PRE_HOOK" "")"
POST_HOOK="$(env_or_default "BACKUP_POST_HOOK" "")"

mkdir -p "$TARGET_DIR"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ARCHIVE_PATH="${TARGET_DIR}/deploycp-backup-${TIMESTAMP}.tar.gz"

INCLUDE_LIST=()

add_path() {
  local candidate="$1"
  if [[ -e "$candidate" ]]; then
    INCLUDE_LIST+=("$candidate")
  fi
}

add_path "${CORE_DIR}/.env"
add_path "${CORE_DIR}/storage/db"
add_path "${CORE_DIR}/storage/generated"
add_path "${CORE_DIR}/storage/ssl"
add_path "/etc/systemd/system/deploycp.service"
add_path "/etc/nginx/sites-available"
add_path "/etc/nginx/sites-enabled"
add_path "/etc/cron.d/deploycp-backup"
add_path "/etc/logrotate.d/deploycp"
add_path "/etc/fail2ban/jail.d/deploycp.local"

if [[ "$INCLUDE_SITE_CONTENT" == "true" ]]; then
  add_path "${DATA_DIR}/sites"
fi
if [[ "$INCLUDE_PLATFORM_LOGS" == "true" ]]; then
  add_path "${DATA_DIR}/logs"
fi

if [[ "${#INCLUDE_LIST[@]}" -eq 0 ]]; then
  echo "nothing to back up" >&2
  exit 1
fi

export DEPLOYCP_BACKUP_TARGET="$ARCHIVE_PATH"
export DEPLOYCP_BACKUP_TARGET_DIR="$TARGET_DIR"
run_hook "$PRE_HOOK"

tar -czf "$ARCHIVE_PATH" "${INCLUDE_LIST[@]}"

if [[ "$RETENTION_DAYS" =~ ^[0-9]+$ ]] && [[ "$RETENTION_DAYS" -gt 0 ]]; then
  find "$TARGET_DIR" -maxdepth 1 -type f -name 'deploycp-backup-*.tar.gz' -mtime "+${RETENTION_DAYS}" -delete
fi

run_hook "$POST_HOOK"
echo "$ARCHIVE_PATH"
