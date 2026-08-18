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

package_installed() {
  local manager="$1"
  local pkg="$2"
  case "$manager" in
    apt) dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q 'install ok installed' ;;
    dnf|yum|zypper) rpm -q "$pkg" >/dev/null 2>&1 ;;
    pacman) pacman -Q "$pkg" >/dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

apt_get_install() {
  DEBIAN_FRONTEND=noninteractive PYTHONWARNINGS=ignore::SyntaxWarning apt-get "$@"
}

install_named_packages() {
  local manager="$1"
  shift
  [[ $# -gt 0 ]] || return 0
  case "$manager" in
    apt)
      apt_get_install install -y "$@"
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
    if package_available "$manager" "$pkg" && ! package_installed "$manager" "$pkg"; then
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
    if package_installed "$manager" "$pkg"; then
      return 0
    fi
  done
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
  case "$manager" in
    apt)
      install_first_available_package "$manager" php-cli php8.4-cli php8.3-cli php8.2-cli php8.1-cli php8-cli php
      install_first_available_package "$manager" php-mysql php8.4-mysql php8.3-mysql php8.2-mysql php8.1-mysql
      install_first_available_package "$manager" php-pgsql php8.4-pgsql php8.3-pgsql php8.2-pgsql php8.1-pgsql
      install_first_available_package "$manager" php-sqlite3 php8.4-sqlite3 php8.3-sqlite3 php8.2-sqlite3 php8.1-sqlite3
      ;;
    dnf|yum)
      install_first_available_package "$manager" php-cli php
      install_first_available_package "$manager" php-mysqlnd php-mysql
      install_first_available_package "$manager" php-pgsql
      install_first_available_package "$manager" php-sqlite3 php-pdo
      ;;
    zypper)
      install_first_available_package "$manager" php8-cli php-cli php8 php
      install_first_available_package "$manager" php8-mysql php-mysql
      install_first_available_package "$manager" php8-pgsql php-pgsql
      install_first_available_package "$manager" php8-sqlite php-sqlite3
      ;;
    pacman)
      install_first_available_package "$manager" php
      install_optional_packages "$manager" php-pgsql php-sqlite
      ;;
    *)
      install_first_available_package "$manager" php-cli php8-cli php php8
      install_optional_packages "$manager" php-mysql php-pgsql php-sqlite3
      ;;
  esac
}

install_backup_tool_packages() {
  local manager="$1"
  case "$manager" in
    apt)
      install_first_available_package "$manager" mariadb-client default-mysql-client mysql-client
      install_first_available_package "$manager" postgresql-client
      ;;
    dnf|yum)
      install_first_available_package "$manager" mariadb postgresql
      ;;
    zypper)
      install_first_available_package "$manager" mariadb-client mariadb
      install_first_available_package "$manager" postgresql
      ;;
    pacman)
      install_optional_packages "$manager" mariadb postgresql
      ;;
    *)
      install_optional_packages "$manager" mariadb-client default-mysql-client mysql-client postgresql-client mariadb postgresql
      ;;
  esac
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
      if ! "$tmp_target" --self-test >/dev/null 2>&1; then
        rm -f "$tmp_target"
        echo "release binary self-test failed" >&2
        return 1
      fi
      return 0
    fi
  done
  return 1
}

read_env_value() {
  local file="$1"
  local key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

ROLLBACK_DIR=""
ROLLBACK_DATABASE=""
ROLLBACK_DATABASE_OWNER=""
ROLLBACK_DATABASE_MODE=""
PANEL_WAS_ACTIVE=0
UPDATE_COMMITTED=0
ROLLBACK_READY=0
ROLLBACK_SYSTEM_PATHS=(
  /etc/ssh/sshd_config
  /etc/ssh/sshd_config.d/99-deploycp.conf
  /etc/shells
  /etc/sudoers.d/deploycp-transfer
  /etc/deploycp/restricted-shell.json
  /usr/local/bin/deploycp-rshell
  /usr/local/bin/deploycp
  /usr/local/libexec/deploycp-transfer
  /usr/local/libexec/deploycp-shell.rc
  /etc/fail2ban/jail.d/deploycp.local
  /etc/logrotate.d/deploycp
  /etc/cron.d/deploycp-backup
)
ROLLBACK_TREE_PATHS=()

prepare_rollback_tree_paths() {
  local nginx_available nginx_enabled core_root
  nginx_available="$(read_env_value "${CORE_DIR}/.env" "NGINX_AVAILABLE_DIR")"
  nginx_enabled="$(read_env_value "${CORE_DIR}/.env" "NGINX_ENABLED_DIR")"
  nginx_available="${nginx_available%\"}"
  nginx_available="${nginx_available#\"}"
  nginx_enabled="${nginx_enabled%\"}"
  nginx_enabled="${nginx_enabled#\"}"
  [[ -n "$nginx_available" ]] || nginx_available="/etc/nginx/sites-available"
  [[ -n "$nginx_enabled" ]] || nginx_enabled="/etc/nginx/sites-enabled"
  core_root="$(realpath -m -- "$CORE_DIR")"
  nginx_available="$(realpath -m -- "$nginx_available")"
  nginx_enabled="$(realpath -m -- "$nginx_enabled")"
  ROLLBACK_TREE_PATHS=(
    "${core_root}/frontend"
    "${core_root}/docs"
    "${core_root}/assets"
    "${core_root}/scripts"
    "${core_root}/.env"
    "$nginx_available"
    "$nginx_enabled"
  )
}

safe_rollback_tree_path() {
  local tree_path="$1"
  local core_root normalized
  core_root="$(realpath -m -- "$CORE_DIR")"
  normalized="$(realpath -m -- "$tree_path")"
  [[ "$tree_path" == "$normalized" && "$normalized" == /* && "$normalized" != "/" && "$normalized" != "/etc" && "$normalized" != "$core_root" ]]
}

prepare_update_rollback() {
  local target="${CORE_DIR}/bin/${BIN_NAME}"
  local configured_db=""
  ROLLBACK_DIR="$(mktemp -d /tmp/deploycp-update.XXXXXX)"
  if [[ -f "$target" ]]; then
    cp -p "$target" "${ROLLBACK_DIR}/${BIN_NAME}"
  fi
  mkdir -p "${ROLLBACK_DIR}/system"
  local system_path=""
  for system_path in "${ROLLBACK_SYSTEM_PATHS[@]}"; do
    if [[ -e "$system_path" || -L "$system_path" ]]; then
      mkdir -p "${ROLLBACK_DIR}/system$(dirname "$system_path")"
      cp -a "$system_path" "${ROLLBACK_DIR}/system${system_path}"
    fi
  done
  prepare_rollback_tree_paths
  mkdir -p "${ROLLBACK_DIR}/trees"
  local tree_path tree_index=0
  for tree_path in "${ROLLBACK_TREE_PATHS[@]}"; do
    if ! safe_rollback_tree_path "$tree_path"; then
      echo "refusing unsafe rollback path: $tree_path" >&2
      return 1
    fi
    if [[ -e "$tree_path" || -L "$tree_path" ]]; then
      mkdir -p "${ROLLBACK_DIR}/trees/${tree_index}"
      cp -a "$tree_path" "${ROLLBACK_DIR}/trees/${tree_index}/item"
    fi
    tree_index=$((tree_index + 1))
  done
  configured_db="$(read_env_value "${CORE_DIR}/.env" "SQLITE_PATH")"
  configured_db="${configured_db%\"}"
  configured_db="${configured_db#\"}"
  if [[ -z "$configured_db" ]]; then
    configured_db="${CORE_DIR}/storage/db/deploycp.sqlite"
  elif [[ "$configured_db" != /* ]]; then
    configured_db="${CORE_DIR}/${configured_db#./}"
  fi
  if [[ -f "$configured_db" && "$configured_db" != *"'"* ]]; then
    ROLLBACK_DATABASE="$configured_db"
    ROLLBACK_DATABASE_OWNER="$(stat -c '%u:%g' "$configured_db")"
    ROLLBACK_DATABASE_MODE="$(stat -c '%a' "$configured_db")"
    sqlite3 "$configured_db" ".backup '${ROLLBACK_DIR}/deploycp.sqlite'"
  fi
  if systemctl is-active --quiet "${SERVICE_NAME}"; then
    PANEL_WAS_ACTIVE=1
  fi
  ROLLBACK_READY=1
}

finish_update() {
  local status=$?
  trap - EXIT
  if [[ "$UPDATE_COMMITTED" -ne 1 && "$ROLLBACK_READY" -eq 1 ]]; then
    echo "update failed; restoring previous DeployCP release" >&2
    systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
    if [[ -n "$ROLLBACK_DIR" && -f "${ROLLBACK_DIR}/${BIN_NAME}" ]]; then
      cp -p "${ROLLBACK_DIR}/${BIN_NAME}" "${CORE_DIR}/bin/${BIN_NAME}"
    fi
    if [[ -n "$ROLLBACK_DATABASE" && -f "${ROLLBACK_DIR}/deploycp.sqlite" ]]; then
      rm -f "${ROLLBACK_DATABASE}-wal" "${ROLLBACK_DATABASE}-shm"
      cp -p "${ROLLBACK_DIR}/deploycp.sqlite" "$ROLLBACK_DATABASE"
      if [[ -n "$ROLLBACK_DATABASE_OWNER" ]]; then
        chown "$ROLLBACK_DATABASE_OWNER" "$ROLLBACK_DATABASE"
      fi
      if [[ -n "$ROLLBACK_DATABASE_MODE" ]]; then
        chmod "$ROLLBACK_DATABASE_MODE" "$ROLLBACK_DATABASE"
      fi
    fi
    local tree_path tree_index=0
    for tree_path in "${ROLLBACK_TREE_PATHS[@]}"; do
      if safe_rollback_tree_path "$tree_path"; then
        rm -rf -- "$tree_path"
        if [[ -e "${ROLLBACK_DIR}/trees/${tree_index}/item" || -L "${ROLLBACK_DIR}/trees/${tree_index}/item" ]]; then
          mkdir -p "$(dirname "$tree_path")"
          cp -a "${ROLLBACK_DIR}/trees/${tree_index}/item" "$tree_path"
        fi
      fi
      tree_index=$((tree_index + 1))
    done
    local system_path=""
    for system_path in "${ROLLBACK_SYSTEM_PATHS[@]}"; do
      rm -f "$system_path"
      if [[ -e "${ROLLBACK_DIR}/system${system_path}" || -L "${ROLLBACK_DIR}/system${system_path}" ]]; then
        mkdir -p "$(dirname "$system_path")"
        cp -a "${ROLLBACK_DIR}/system${system_path}" "$system_path"
      fi
    done
    local nginx_binary
    nginx_binary="$(read_env_value "${CORE_DIR}/.env" "NGINX_BINARY")"
    nginx_binary="${nginx_binary%\"}"
    nginx_binary="${nginx_binary#\"}"
    [[ -n "$nginx_binary" ]] || nginx_binary="/usr/sbin/nginx"
    if [[ -x "$nginx_binary" ]] && "$nginx_binary" -t >/dev/null 2>&1; then
      systemctl reload nginx >/dev/null 2>&1 || true
    fi
    systemctl reload ssh >/dev/null 2>&1 || systemctl reload sshd >/dev/null 2>&1 || true
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [[ "$PANEL_WAS_ACTIVE" -eq 1 ]]; then
      if ! systemctl start "${SERVICE_NAME}" >/dev/null 2>&1; then
        echo "rollback warning: previous ${SERVICE_NAME} service could not be restarted" >&2
      fi
    fi
  fi
  case "$ROLLBACK_DIR" in
    /tmp/deploycp-update.*) rm -rf -- "$ROLLBACK_DIR" ;;
  esac
  exit "$status"
}

ensure_cli_wrapper() {
  local wrapper="/usr/local/bin/${BIN_NAME}"
  local target="${CORE_DIR}/bin/${BIN_NAME}"
  if [[ ! -x "$target" ]]; then
    return 0
  fi
  if [[ -e "$wrapper" ]] && ! grep -Fq "Managed by DeployCP CLI wrapper" "$wrapper" 2>/dev/null; then
    if [[ "$(readlink "$wrapper" 2>/dev/null || true)" != "$target" ]]; then
      echo "Skipping ${wrapper}; an unmanaged command already exists there" >&2
      return 0
    fi
  fi
  cat >"$wrapper" <<EOF
#!/usr/bin/env bash
# Managed by DeployCP CLI wrapper. Do not edit.
export DEPLOYCP_ENV_FILE="${CORE_DIR}/.env"
exec "${target}" "\$@"
EOF
  chmod 0755 "$wrapper"
}

stage_release_assets() {
  local candidate=""

  for candidate in "${PACKAGE_ROOT}/frontend" "$(pwd)/frontend"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/frontend" ]]; then
      replace_release_directory "$candidate" "${CORE_DIR}/frontend"
      break
    fi
  done

  for candidate in "${PACKAGE_ROOT}/docs" "$(pwd)/docs"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/docs" ]]; then
      replace_release_directory "$candidate" "${CORE_DIR}/docs"
      break
    fi
  done

  for candidate in "${PACKAGE_ROOT}/assets" "$(pwd)/assets"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/assets" ]]; then
      replace_release_directory "$candidate" "${CORE_DIR}/assets"
      break
    fi
  done

  for candidate in "${PACKAGE_ROOT}/scripts/linux" "$(pwd)/scripts/linux"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/scripts/linux" ]]; then
      replace_release_directory "$candidate" "${CORE_DIR}/scripts/linux"
      find "${CORE_DIR}/scripts/linux" -type f -name '*.sh' -exec chmod 0755 {} +
      break
    fi
  done
}

replace_release_directory() {
  local source="$1"
  local target="$2"
  local staged="${target}.new"
  rm -rf -- "$staged"
  mkdir -p "$staged"
  cp -R "${source}/." "$staged/"
  chown -R "${APP_USER}:${APP_USER}" "$staged"
  rm -rf -- "$target"
  mv "$staged" "$target"
}

ensure_bundled_adminer() {
  local target="${CORE_DIR}/assets/adminer/adminer.php"
  local tmp="${target}.tmp"
  if [[ -f "$target" ]] && grep -q 'VERSION="5\.4\.2"' "$target"; then
    return 0
  fi
  mkdir -p "$(dirname "$target")"
  if command -v curl >/dev/null 2>&1; then
    if curl -fsSL https://www.adminer.org/latest-en.php -o "$tmp"; then
      mv -f "$tmp" "$target"
      chown "${APP_USER}:${APP_USER}" "$target"
      return 0
    fi
  elif command -v wget >/dev/null 2>&1; then
    if wget -q https://www.adminer.org/latest-en.php -O "$tmp"; then
      mv -f "$tmp" "$target"
      chown "${APP_USER}:${APP_USER}" "$target"
      return 0
    fi
  fi
  rm -f "$tmp"
  echo "Bundled Adminer missing. Place Adminer 5.4.2 English-only PHP at ${target} and rerun." >&2
  return 1
}

reset_adminer_helper() {
  rm -f \
    "${CORE_DIR}/storage/generated/adminer-helper/adminer.php" \
    "${CORE_DIR}/storage/generated/adminer-helper/index.php" \
    "${CORE_DIR}/storage/generated/adminer-helper/adminer-source.txt"
}

verify_panel_http() {
  local app_port
  app_port="$(read_env_value "${CORE_DIR}/.env" "APP_PORT")"
  app_port="${app_port%\"}"
  app_port="${app_port#\"}"
  [[ "$app_port" =~ ^[0-9]+$ ]] || app_port=2024
  local attempt
  for attempt in {1..20}; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${app_port}/robots.txt" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "DeployCP HTTP readiness check failed on 127.0.0.1:${app_port}" >&2
  return 1
}

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

if ! stage_release_binary; then
  echo "release binary not found in update package" >&2
  exit 1
fi

pkg_manager="$(detect_pkg_manager)"
install_optional_packages "$pkg_manager" sudo bubblewrap util-linux
install_db_ui_helper_packages "$pkg_manager"
install_backup_tool_packages "$pkg_manager"

trap finish_update EXIT
prepare_update_rollback
if [[ "$PANEL_WAS_ACTIVE" -eq 1 ]]; then
  systemctl stop "${SERVICE_NAME}"
fi
mv -f "${CORE_DIR}/bin/${BIN_NAME}.new" "${CORE_DIR}/bin/${BIN_NAME}"
stage_release_assets
ensure_bundled_adminer
reset_adminer_helper

if [[ ! -x "${CORE_DIR}/bin/${BIN_NAME}" ]]; then
  echo "binary not found at ${CORE_DIR}/bin/${BIN_NAME}" >&2
  exit 1
fi
ensure_cli_wrapper

chown "${APP_USER}:${APP_USER}" "${CORE_DIR}/bin/${BIN_NAME}"
for release_path in frontend docs assets scripts; do
  if [[ -e "${CORE_DIR}/${release_path}" ]]; then
    chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/${release_path}"
  fi
done
if [[ -f "${CORE_DIR}/.env" ]]; then
  chown "${APP_USER}:${APP_USER}" "${CORE_DIR}/.env"
fi
set_env_value "${CORE_DIR}/.env" "APP_VERSION" "$(resolved_release_version)"
set_env_value "${CORE_DIR}/.env" "DEPLOYCP_REPO" "${DEPLOYCP_REPO:-saiarlen/deployCP}"
set_env_value "${CORE_DIR}/.env" "ADMINER_URL" "http://127.0.0.1:8081"
if ! grep -q "^ALERT_WEBHOOK_URL=" "${CORE_DIR}/.env"; then
  printf '%s\n' "ALERT_WEBHOOK_URL=" >>"${CORE_DIR}/.env"
fi
systemctl daemon-reload
(
  cd "${CORE_DIR}"
  DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" "${CORE_DIR}/bin/${BIN_NAME}" prepare-update
)
systemctl start "${SERVICE_NAME}"
systemctl is-active --quiet "${SERVICE_NAME}"
verify_panel_http
(
  cd "${CORE_DIR}"
  DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" "${CORE_DIR}/bin/${BIN_NAME}" verify-host
)
systemctl is-active --quiet "${SERVICE_NAME}"
verify_panel_http
systemctl status "${SERVICE_NAME}" --no-pager
UPDATE_COMMITTED=1
