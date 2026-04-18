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

systemd_unit_exists() {
  local name="$1"
  systemctl list-unit-files "${name}.service" --no-legend 2>/dev/null | grep -q "${name}.service"
}

first_service_name() {
  local fallback="$1"
  shift
  local candidate
  for candidate in "$@"; do
    if systemd_unit_exists "$candidate"; then
      echo "$candidate"
      return
    fi
  done
  echo "$fallback"
}

ensure_default_envs() {
  if [[ ! -f "$ENV_FILE" ]]; then
    return
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_TARGET_DIR")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_TARGET_DIR" "${DATA_DIR}/backups"
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_RETENTION_DAYS")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_RETENTION_DAYS" "14"
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_INCLUDE_SITE_CONTENT")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_INCLUDE_SITE_CONTENT" "true"
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_INCLUDE_PLATFORM_LOGS")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_INCLUDE_PLATFORM_LOGS" "false"
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_PRE_HOOK")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_PRE_HOOK" ""
  fi
  if [[ -z "$(read_env_value "$ENV_FILE" "BACKUP_POST_HOOK")" ]]; then
    set_env_value "$ENV_FILE" "BACKUP_POST_HOOK" ""
  fi
}

ensure_fail2ban() {
  local fail2ban_service
  fail2ban_service="$(first_service_name fail2ban fail2ban fail2ban-server)"
  mkdir -p /etc/fail2ban/jail.d
  cat >/etc/fail2ban/jail.d/deploycp.local <<EOF
[DEFAULT]
ignoreip = 127.0.0.1/8 ::1
bantime = 1h
findtime = 10m
maxretry = 6

[sshd]
enabled = true
backend = systemd
EOF
  if systemd_unit_exists "$fail2ban_service"; then
    systemctl enable "$fail2ban_service" >/dev/null 2>&1 || true
    systemctl restart "$fail2ban_service" >/dev/null 2>&1 || true
  fi
}

ensure_logrotate() {
  cat >/etc/logrotate.d/deploycp <<EOF
${CORE_DIR}/storage/logs/*.log
${CORE_DIR}/storage/logs/*/*.log
${DATA_DIR}/logs/*/*/*.log
{
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
    su ${APP_USER} ${APP_USER}
    create 0640 ${APP_USER} ${APP_USER}
}
EOF
}

ensure_backup_job() {
  local backup_script="${CORE_DIR}/scripts/linux/backup.sh"
  local backup_target
  backup_target="$(read_env_value "$ENV_FILE" "BACKUP_TARGET_DIR")"
  if [[ -z "$backup_target" ]]; then
    backup_target="${DATA_DIR}/backups"
  fi
  mkdir -p "$backup_target"
  chown -R "${APP_USER}:${APP_USER}" "$backup_target"
  chmod 0755 "$backup_target"

  if [[ -x "$backup_script" ]]; then
    cat >/etc/cron.d/deploycp-backup <<EOF
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
17 3 * * * root APP_USER=${APP_USER} APP_HOME=${APP_HOME} CORE_DIR=${CORE_DIR} DATA_DIR=${DATA_DIR} ${backup_script} >> ${CORE_DIR}/storage/logs/backup.log 2>&1
EOF
    chmod 0644 /etc/cron.d/deploycp-backup
  fi
}

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

ensure_default_envs
mkdir -p "${CORE_DIR}/storage/logs" "${DATA_DIR}/backups"
chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/storage/logs" "${DATA_DIR}/backups"
ensure_fail2ban
ensure_logrotate
ensure_backup_job
echo "host hardening applied"
