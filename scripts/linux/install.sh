#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
DATA_DIR="${DATA_DIR:-${APP_HOME}/platforms}"
BIN_NAME="${BIN_NAME:-deploycp}"
SERVICE_NAME="${SERVICE_NAME:-deploycp}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

DISTRO_FAMILY=""
PROFTPD_CONF_DIR=""
PROFTPD_SERVICE_NAME=""
REDIS_SERVICE_NAME=""
DB_SERVICE_NAME=""
POSTGRES_SERVICE_NAME=""
VARNISH_SERVICE_NAME=""
CRON_SERVICE_NAME=""
NGINX_BINARY=""
NGINX_CONFIG_DIR=""
NGINX_CONF_D_DIR=""
NGINX_MAIN_CONF=""
NGINX_ENABLED_DIR=""
NGINX_AVAILABLE_DIR=""
SYSTEMCTL_BINARY=""
RUNUSER_BINARY=""
CERTBOT_BINARY=""
UFW_BINARY=""
FIREWALLCMD_BINARY=""
IPTABLES_BINARY=""
REDIS_SERVER_BINARY=""
VARNISH_CONFIG_DIR=""
VARNISH_MAIN_VCL=""
VARNISH_INCLUDE_VCL=""
VARNISHD_BINARY=""
VARNISHADM_BINARY=""
VARNISH_ADMIN_ADDR=""
VARNISH_SECRET_FILE=""

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
      mv -f "$tmp_target" "$target"
      return 0
    fi
  done
  return 1
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

  for candidate in "${PACKAGE_ROOT}/assets" "$(pwd)/assets"; do
    if [[ -d "$candidate" && "$candidate" != "${CORE_DIR}/assets" ]]; then
      mkdir -p "${CORE_DIR}/assets"
      cp -R "${candidate}/." "${CORE_DIR}/assets/"
      chown -R "${APP_USER}:${APP_USER}" "${CORE_DIR}/assets"
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

run_deploycp_cli_quiet() {
  local command="$1"
  shift || true
  (
    cd "${CORE_DIR}"
    DEPLOYCP_ENV_FILE="${CORE_DIR}/.env" \
    DEPLOYCP_DB_LOG_LEVEL=quiet \
      "${CORE_DIR}/bin/${BIN_NAME}" "$command" "$@"
  )
}

print_summary_row() {
  local label="$1"
  local value="$2"
  local width=45
  local prefix=""
  value="${value:-}"
  if [[ -z "$value" ]]; then
    printf "║ %-12s │ %-45s ║\n" "$label" ""
    return
  fi
  while [[ ${#value} -gt $width ]]; do
    prefix="${value:0:$width}"
    printf "║ %-12s │ %-45s ║\n" "$label" "$prefix"
    label=""
    value="${value:$width}"
  done
  printf "║ %-12s │ %-45s ║\n" "$label" "$value"
}

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

detect_pkg_manager() {
  if command -v apt-get >/dev/null 2>&1; then echo apt; return; fi
  if command -v dnf >/dev/null 2>&1; then echo dnf; return; fi
  if command -v yum >/dev/null 2>&1; then echo yum; return; fi
  if command -v zypper >/dev/null 2>&1; then echo zypper; return; fi
  if command -v pacman >/dev/null 2>&1; then echo pacman; return; fi
  echo ""
}

detect_distro_family() {
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "${ID_LIKE:-${ID:-}}" in
      *debian*|*ubuntu*) echo debian; return ;;
      *rhel*|*fedora*|*centos*|*rocky*|*alma*) echo rhel; return ;;
      *suse*) echo suse; return ;;
      *arch*) echo arch; return ;;
    esac
  fi
  echo unknown
}

apt_get_install() {
  DEBIAN_FRONTEND=noninteractive PYTHONWARNINGS=ignore::SyntaxWarning apt-get "$@"
}

install_packages() {
  local manager="$1"
  case "$manager" in
    apt)
      apt_get_install update -y
      apt_get_install install -y nginx certbot curl tar sqlite3 ca-certificates openssl procps cron sudo bubblewrap util-linux redis-server proftpd-basic varnish mariadb-server postgresql ufw fail2ban logrotate acl
      ;;
    dnf)
      dnf install -y nginx certbot curl tar sqlite sqlite-libs ca-certificates openssl procps-ng cronie sudo bubblewrap util-linux redis proftpd varnish mariadb-server postgresql-server firewalld fail2ban logrotate acl
      ;;
    yum)
      yum install -y nginx certbot curl tar sqlite ca-certificates openssl procps-ng cronie sudo bubblewrap util-linux redis proftpd varnish mariadb-server postgresql-server firewalld fail2ban logrotate acl
      ;;
    zypper)
      zypper --non-interactive install nginx certbot curl tar sqlite3 ca-certificates openssl procps cron sudo bubblewrap util-linux redis proftpd varnish mariadb postgresql-server firewalld fail2ban logrotate acl
      ;;
    pacman)
      pacman -Sy --noconfirm nginx certbot curl tar sqlite ca-certificates openssl procps-ng cronie sudo bubblewrap util-linux redis mariadb postgresql varnish ufw fail2ban logrotate acl
      ;;
    *)
      echo "unsupported package manager" >&2
      exit 1
      ;;
  esac
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

install_default_php_runtime() {
  local manager=""
  local candidate=""
  local runtime_root="${CORE_DIR}/storage/runtimes"
  manager="$(detect_pkg_manager)"

  mkdir -p "$runtime_root"
  for candidate in 8.4 8.3 8.2 8.1; do
    if install_package_php_runtime "$manager" "$candidate" "$runtime_root"; then
      chown -R "${APP_USER}:${APP_USER}" "$runtime_root"
      return 0
    fi
  done
  return 0
}

install_package_php_runtime() {
  local manager="$1"
  local version="$2"
  local runtime_root="$3"
  local cli_pkg=""
  local fpm_pkg=""
  local fpm_unit=""
  local php_bin=""
  local version_dir="${runtime_root}/php/${version}"
  local bin_dir="${version_dir}/bin"

  case "$manager" in
    apt)
      cli_pkg="php${version}-cli"
      fpm_pkg="php${version}-fpm"
      fpm_unit="php${version}-fpm"
      ;;
    dnf|yum)
      local digits="${version//./}"
      if package_available "$manager" "php${digits}-php-cli" && package_available "$manager" "php${digits}-php-fpm"; then
        cli_pkg="php${digits}-php-cli"
        fpm_pkg="php${digits}-php-fpm"
        fpm_unit="php${digits}-php-fpm"
      else
        return 1
      fi
      ;;
    *)
      return 1
      ;;
  esac

  package_available "$manager" "$cli_pkg" || return 1
  package_available "$manager" "$fpm_pkg" || return 1
  install_named_packages "$manager" "$cli_pkg" "$fpm_pkg" || return 1

  php_bin="$(find_php_package_binary "$manager" "$version" "$cli_pkg")"
  [[ -x "$php_bin" ]] || return 1

  mkdir -p "$bin_dir"
  cat >"${bin_dir}/php" <<EOF
#!/bin/sh
exec $(printf '%q' "$php_bin") "\$@"
EOF
  chmod 0755 "${bin_dir}/php"
  cat >"${version_dir}/.deploycp-origin" <<EOF
mode=package
binary=${php_bin}
runtime=php
version=${version}
EOF

  if command -v systemctl >/dev/null 2>&1 && [[ -n "$fpm_unit" ]]; then
    systemctl enable "$fpm_unit" >/dev/null 2>&1 || true
    systemctl start "$fpm_unit" >/dev/null 2>&1 || true
  fi
  return 0
}

find_php_package_binary() {
  local manager="$1"
  local version="$2"
  local cli_pkg="$3"
  local candidate=""
  for candidate in "php${version}" "php${version//./}" "/usr/bin/php${version}" "/usr/local/bin/php${version}" "/bin/php${version}"; do
    if command -v "$candidate" >/dev/null 2>&1; then
      command -v "$candidate"
      return 0
    fi
    if [[ -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  case "$manager" in
    apt)
      while IFS= read -r candidate; do
        if [[ -x "$candidate" && "$(basename "$candidate")" == "php${version}" ]]; then
          echo "$candidate"
          return 0
        fi
      done < <(dpkg-query -L "$cli_pkg" 2>/dev/null || true)
      ;;
    dnf|yum)
      while IFS= read -r candidate; do
        if [[ -x "$candidate" && "$(basename "$candidate")" == "php" ]]; then
          echo "$candidate"
          return 0
        fi
      done < <(rpm -ql "$cli_pkg" 2>/dev/null || true)
      ;;
  esac
  return 1
}

command_path() {
  local fallback="$1"
  shift
  local candidate
  for candidate in "$@"; do
    if [[ -n "$candidate" ]] && command -v "$candidate" >/dev/null 2>&1; then
      command -v "$candidate"
      return
    fi
  done
  echo "$fallback"
}

is_public_ipv4() {
  local ip="$1"
  [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  case "$ip" in
    10.*|127.*|169.254.*|172.16.*|172.17.*|172.18.*|172.19.*|172.2[0-9].*|172.3[0-1].*|192.168.*|0.*)
      return 1
      ;;
  esac
  return 0
}

normalize_display_host() {
  local host="$1"
  host="${host#http://}"
  host="${host#https://}"
  host="${host%%/*}"
  host="${host%%:*}"
  host="${host#[}"
  host="${host%]}"
  echo "$host"
}

is_local_display_host() {
  local host="$1"
  case "${host,,}" in
    ""|"localhost"|"0.0.0.0"|"::"|"[::]"|127.*)
      return 0
      ;;
  esac
  return 1
}

detected_external_ipv4() {
  local candidate=""
  local endpoint=""
  for endpoint in https://api.ipify.org https://ifconfig.me/ip; do
    candidate="$(curl -fsS --max-time 3 "$endpoint" 2>/dev/null || true)"
    candidate="${candidate//$'\n'/}"
    candidate="${candidate//$'\r'/}"
    if is_public_ipv4 "$candidate"; then
      echo "$candidate"
      return
    fi
  done
}

detect_display_host() {
  local candidate=""
  local env_base_host=""
  local env_host=""

  env_base_host="$(normalize_display_host "$(read_env_value "${CORE_DIR}/.env" "APP_BASE_URL" 2>/dev/null || true)")"
  if ! is_local_display_host "$env_base_host"; then
    echo "$env_base_host"
    return
  fi

  candidate="$(detected_external_ipv4)"
  if [[ -n "$candidate" ]]; then
    echo "$candidate"
    return
  fi

  env_host="$(normalize_display_host "$(read_env_value "${CORE_DIR}/.env" "APP_HOST" 2>/dev/null || true)")"
  if ! is_local_display_host "$env_host"; then
    echo "$env_host"
    return
  fi

  if command -v ip >/dev/null 2>&1; then
    while IFS= read -r candidate; do
      candidate="${candidate%%/*}"
      if is_public_ipv4 "$candidate"; then
        echo "$candidate"
        return
      fi
    done < <(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}')
  fi

  for candidate in $(hostname -I 2>/dev/null); do
    if is_public_ipv4 "$candidate"; then
      echo "$candidate"
      return
    fi
  done

  candidate="$(hostname -f 2>/dev/null || true)"
  if [[ -n "$candidate" && "$candidate" != "localhost" ]]; then
    echo "$candidate"
    return
  fi

  for candidate in $(hostname -I 2>/dev/null); do
    if [[ -n "$candidate" ]]; then
      echo "$candidate"
      return
    fi
  done

  echo "your-server-ip"
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

configure_platform_defaults() {
  DISTRO_FAMILY="$(detect_distro_family)"
  case "$DISTRO_FAMILY" in
    debian) PROFTPD_CONF_DIR="/etc/proftpd/conf.d" ;;
    rhel) PROFTPD_CONF_DIR="/etc/proftpd.d" ;;
    suse) PROFTPD_CONF_DIR="/etc/proftpd/conf.d" ;;
    arch) PROFTPD_CONF_DIR="/etc/proftpd" ;;
    *) PROFTPD_CONF_DIR="/etc/proftpd/conf.d" ;;
  esac

  NGINX_BINARY="$(command_path /usr/sbin/nginx nginx /usr/sbin/nginx /usr/bin/nginx)"
  NGINX_CONFIG_DIR="/etc/nginx"
  NGINX_CONF_D_DIR="${NGINX_CONFIG_DIR}/conf.d"
  NGINX_MAIN_CONF="${NGINX_CONFIG_DIR}/nginx.conf"
  NGINX_ENABLED_DIR="${NGINX_CONFIG_DIR}/sites-enabled"
  NGINX_AVAILABLE_DIR="${NGINX_CONFIG_DIR}/sites-available"
  SYSTEMCTL_BINARY="$(command_path /bin/systemctl systemctl /bin/systemctl /usr/bin/systemctl)"
  RUNUSER_BINARY="$(command_path /usr/sbin/runuser runuser /usr/sbin/runuser /bin/runuser)"
  CERTBOT_BINARY="$(command_path /usr/bin/certbot certbot /usr/bin/certbot /usr/local/bin/certbot)"
  UFW_BINARY="$(command_path /usr/sbin/ufw ufw /usr/sbin/ufw /usr/bin/ufw)"
  FIREWALLCMD_BINARY="$(command_path /usr/bin/firewall-cmd firewall-cmd /usr/bin/firewall-cmd /bin/firewall-cmd)"
  IPTABLES_BINARY="$(command_path /usr/sbin/iptables iptables /usr/sbin/iptables /bin/iptables)"
  REDIS_SERVER_BINARY="$(command_path /usr/bin/redis-server redis-server /usr/bin/redis-server /usr/sbin/redis-server)"
  VARNISHD_BINARY="$(command_path /usr/sbin/varnishd varnishd /usr/sbin/varnishd /usr/bin/varnishd)"
  VARNISHADM_BINARY="$(command_path /usr/bin/varnishadm varnishadm /usr/bin/varnishadm /usr/sbin/varnishadm)"

  PROFTPD_SERVICE_NAME="$(first_service_name proftpd proftpd proftpd-basic)"
  REDIS_SERVICE_NAME="$(first_service_name redis-server redis-server redis)"
  DB_SERVICE_NAME="$(first_service_name mariadb mariadb mysql mysqld)"
  POSTGRES_SERVICE_NAME="$(first_service_name postgresql postgresql)"
  VARNISH_SERVICE_NAME="$(first_service_name varnish varnish)"
  CRON_SERVICE_NAME="$(first_service_name cron cron crond)"

  VARNISH_CONFIG_DIR="/etc/varnish/deploycp.d"
  VARNISH_MAIN_VCL="/etc/varnish/default.vcl"
  VARNISH_INCLUDE_VCL="${VARNISH_CONFIG_DIR}/deploycp.vcl"
  VARNISH_ADMIN_ADDR="127.0.0.1:6082"
  VARNISH_SECRET_FILE="/etc/varnish/secret"
}

validate_nginx_config() {
  if [[ ! -x "$NGINX_BINARY" || ! -f "$NGINX_MAIN_CONF" ]]; then
    return 0
  fi
  "$NGINX_BINARY" -t -c "$NGINX_MAIN_CONF" >/dev/null 2>&1
}

ensure_nginx_integration() {
  local include_glob include_line snippet_path backup_path tmp_path

  mkdir -p "$NGINX_AVAILABLE_DIR" "$NGINX_ENABLED_DIR"

  if [[ ! -f "$NGINX_MAIN_CONF" ]]; then
    return
  fi

  include_glob="${NGINX_ENABLED_DIR}/*.conf"
  include_line="include ${include_glob};"
  snippet_path="${NGINX_CONF_D_DIR}/deploycp-sites-enabled.conf"
  backup_path="${NGINX_MAIN_CONF}.deploycp.bak"
  tmp_path="${NGINX_MAIN_CONF}.tmp"

  if grep -Fq "$NGINX_ENABLED_DIR/" "$NGINX_MAIN_CONF"; then
    validate_nginx_config || {
      echo "nginx config validation failed" >&2
      exit 1
    }
    return
  fi

  if [[ -d "$NGINX_CONF_D_DIR" ]] && grep -Fq "$NGINX_CONF_D_DIR" "$NGINX_MAIN_CONF"; then
    printf '%s\n' "$include_line" >"$snippet_path"
    if validate_nginx_config; then
      return
    fi
    rm -f "$snippet_path"
    echo "failed to validate nginx after writing ${snippet_path}" >&2
    exit 1
  fi

  if [[ ! -f "$backup_path" ]]; then
    cp "$NGINX_MAIN_CONF" "$backup_path"
  fi

  if ! awk -v include_line="    ${include_line}" '
    BEGIN { inserted=0 }
    {
      print
      if (!inserted && $0 ~ /^[[:space:]]*http[[:space:]]*\{/) {
        print include_line
        inserted=1
      }
    }
    END {
      if (!inserted) exit 7
    }
  ' "$NGINX_MAIN_CONF" >"$tmp_path"; then
    rm -f "$tmp_path"
    echo "failed to patch nginx config: could not find http block in ${NGINX_MAIN_CONF}" >&2
    exit 1
  fi

  mv "$tmp_path" "$NGINX_MAIN_CONF"
  if validate_nginx_config; then
    return
  fi

  cp "$backup_path" "$NGINX_MAIN_CONF"
  echo "failed to validate nginx after patching ${NGINX_MAIN_CONF}; restored backup" >&2
  exit 1
}

ensure_service_enabled() {
  local service="$1"
  if [[ -n "$service" ]] && systemd_unit_exists "$service"; then
    systemctl enable "$service" >/dev/null 2>&1 || true
    systemctl start "$service" >/dev/null 2>&1 || true
  fi
}

ensure_sshd_runtime_dir() {
  mkdir -p /run/sshd
  chown root:root /run/sshd
  chmod 0755 /run/sshd
}

detect_ssh_port() {
  local port=""
  if command -v sshd >/dev/null 2>&1; then
    ensure_sshd_runtime_dir
    port="$(sshd -T 2>/dev/null | awk '$1 == "port" { print $2; exit }')"
  fi
  if [[ -z "$port" ]]; then
    port="$(
      awk '
        /^[[:space:]]*#/ { next }
        tolower($1) == "port" && $2 ~ /^[0-9]+$/ { print $2; exit }
      ' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null
    )"
  fi
  if [[ -z "$port" ]]; then
    port="22"
  fi
  echo "$port"
}

read_env_value() {
  local file="$1"
  local key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

ensure_firewall_access() {
  local env_file="${CORE_DIR}/.env"
  local app_port ssh_port ufw_status
  app_port="$(read_env_value "$env_file" "APP_PORT")"
  if [[ -z "$app_port" ]]; then
    app_port="${APP_PORT:-2024}"
  fi
  ssh_port="$(detect_ssh_port)"

  if command -v "$UFW_BINARY" >/dev/null 2>&1; then
    "$UFW_BINARY" allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
    "$UFW_BINARY" allow "80/tcp" >/dev/null 2>&1 || true
    "$UFW_BINARY" allow "443/tcp" >/dev/null 2>&1 || true
    "$UFW_BINARY" allow "${app_port}/tcp" >/dev/null 2>&1 || true
    ufw_status="$("$UFW_BINARY" status 2>/dev/null || true)"
    if printf '%s' "$ufw_status" | grep -qi "Status: inactive"; then
      "$UFW_BINARY" --force enable >/dev/null 2>&1 || true
    fi
    return
  fi

  if command -v "$FIREWALLCMD_BINARY" >/dev/null 2>&1; then
    systemctl enable firewalld >/dev/null 2>&1 || true
    systemctl start firewalld >/dev/null 2>&1 || true
    "$FIREWALLCMD_BINARY" --permanent --add-port="${ssh_port}/tcp" >/dev/null 2>&1 || true
    "$FIREWALLCMD_BINARY" --permanent --add-port="80/tcp" >/dev/null 2>&1 || true
    "$FIREWALLCMD_BINARY" --permanent --add-port="443/tcp" >/dev/null 2>&1 || true
    "$FIREWALLCMD_BINARY" --permanent --add-port="${app_port}/tcp" >/dev/null 2>&1 || true
    "$FIREWALLCMD_BINARY" --reload >/dev/null 2>&1 || true
    return
  fi

  if command -v "$IPTABLES_BINARY" >/dev/null 2>&1; then
    "$IPTABLES_BINARY" -C INPUT -p tcp --dport "$ssh_port" -j ACCEPT >/dev/null 2>&1 || "$IPTABLES_BINARY" -I INPUT -p tcp --dport "$ssh_port" -j ACCEPT >/dev/null 2>&1 || true
    "$IPTABLES_BINARY" -C INPUT -p tcp --dport 80 -j ACCEPT >/dev/null 2>&1 || "$IPTABLES_BINARY" -I INPUT -p tcp --dport 80 -j ACCEPT >/dev/null 2>&1 || true
    "$IPTABLES_BINARY" -C INPUT -p tcp --dport 443 -j ACCEPT >/dev/null 2>&1 || "$IPTABLES_BINARY" -I INPUT -p tcp --dport 443 -j ACCEPT >/dev/null 2>&1 || true
    "$IPTABLES_BINARY" -C INPUT -p tcp --dport "$app_port" -j ACCEPT >/dev/null 2>&1 || "$IPTABLES_BINARY" -I INPUT -p tcp --dport "$app_port" -j ACCEPT >/dev/null 2>&1 || true
  fi
}

initialize_databases() {
  if command -v postgresql-setup >/dev/null 2>&1; then
    postgresql-setup --initdb >/dev/null 2>&1 || true
  elif compgen -G "/usr/bin/postgresql-*-setup" >/dev/null 2>&1; then
    local setup_bin
    setup_bin="$(ls /usr/bin/postgresql-*-setup 2>/dev/null | head -n1)"
    "$setup_bin" --initdb >/dev/null 2>&1 || true
  fi
  if command -v mariadb-install-db >/dev/null 2>&1 && [[ ! -d /var/lib/mysql/mysql ]]; then
    mariadb-install-db --user=mysql >/dev/null 2>&1 || true
  fi
}

ensure_varnish_integration() {
  mkdir -p "$VARNISH_CONFIG_DIR"
  if [[ ! -f "$VARNISH_INCLUDE_VCL" ]]; then
    cat >"$VARNISH_INCLUDE_VCL" <<'EOF'
sub deploycp_recv {
}

sub deploycp_backend_response {
}
EOF
  fi
  if [[ ! -f "$VARNISH_MAIN_VCL" ]]; then
    return
  fi
  if [[ ! -f "${VARNISH_MAIN_VCL}.deploycp.bak" ]]; then
    cp "$VARNISH_MAIN_VCL" "${VARNISH_MAIN_VCL}.deploycp.bak"
  fi
  if ! grep -Fq "$VARNISH_INCLUDE_VCL" "$VARNISH_MAIN_VCL"; then
    awk -v include_line="include \"${VARNISH_INCLUDE_VCL}\";" '
      BEGIN { inserted=0 }
      {
        print
        if (!inserted && $0 ~ /^vcl[[:space:]]+[0-9.]+;/) {
          print include_line
          inserted=1
        }
      }
      END {
        if (!inserted) print include_line
      }
    ' "$VARNISH_MAIN_VCL" >"${VARNISH_MAIN_VCL}.tmp"
    mv "${VARNISH_MAIN_VCL}.tmp" "$VARNISH_MAIN_VCL"
  fi
  if ! grep -Fq "call deploycp_recv;" "$VARNISH_MAIN_VCL"; then
    awk '
      BEGIN { in_recv=0; injected=0 }
      {
        if ($0 ~ /^sub[[:space:]]+vcl_recv[[:space:]]*\{/) {
          print
          in_recv=1
          next
        }
        if (in_recv && $0 ~ /^[[:space:]]*\}/) {
          print "    call deploycp_recv;"
          print
          in_recv=0
          injected=1
          next
        }
        print
      }
      END {
        if (!injected) {
          print ""
          print "sub vcl_recv {"
          print "    call deploycp_recv;"
          print "}"
        }
      }
    ' "$VARNISH_MAIN_VCL" >"${VARNISH_MAIN_VCL}.tmp"
    mv "${VARNISH_MAIN_VCL}.tmp" "$VARNISH_MAIN_VCL"
  fi
  if ! grep -Fq "call deploycp_backend_response;" "$VARNISH_MAIN_VCL"; then
    awk '
      BEGIN { in_backend=0; injected=0 }
      {
        if ($0 ~ /^sub[[:space:]]+vcl_backend_response[[:space:]]*\{/) {
          print
          in_backend=1
          next
        }
        if (in_backend && $0 ~ /^[[:space:]]*\}/) {
          print "    call deploycp_backend_response;"
          print
          in_backend=0
          injected=1
          next
        }
        print
      }
      END {
        if (!injected) {
          print ""
          print "sub vcl_backend_response {"
          print "    call deploycp_backend_response;"
          print "}"
        }
      }
    ' "$VARNISH_MAIN_VCL" >"${VARNISH_MAIN_VCL}.tmp"
    mv "${VARNISH_MAIN_VCL}.tmp" "$VARNISH_MAIN_VCL"
  fi
}

pkg_manager="$(detect_pkg_manager)"
install_packages "$pkg_manager"
install_db_ui_helper_packages "$pkg_manager"
install_backup_tool_packages "$pkg_manager"
configure_platform_defaults
initialize_databases

if ! id -u "$APP_USER" >/dev/null 2>&1; then
  useradd --create-home --home-dir "$APP_HOME" --shell /bin/bash "$APP_USER"
fi

mkdir -p \
  "$CORE_DIR"/{assets,bin,scripts,storage/db,storage/logs,storage/generated,storage/ssl,storage/runtimes,tmp,docs} \
  "$DATA_DIR"/{sites,logs,backups,tmp} \
  "$NGINX_AVAILABLE_DIR" \
  "$NGINX_ENABLED_DIR" \
  "$PROFTPD_CONF_DIR" \
  "$VARNISH_CONFIG_DIR"

chown -R "$APP_USER:$APP_USER" "$APP_HOME"
chmod 755 "$APP_HOME" "$DATA_DIR" "${DATA_DIR}/sites" "${DATA_DIR}/logs" "${DATA_DIR}/tmp" "${DATA_DIR}/backups"
ensure_nginx_integration
ensure_varnish_integration
stage_release_binary || true
ensure_cli_wrapper
stage_release_assets
ensure_bundled_adminer
reset_adminer_helper

if [[ -f "${CORE_DIR}/.env" ]]; then
  :
else
  cat >"${CORE_DIR}/.env" <<EOF
APP_NAME=DeployCP
APP_ENV=production
APP_HOST=0.0.0.0
APP_PORT=2024
APP_BASE_URL=http://localhost:2024
APP_VERSION=$(resolved_release_version)
DEPLOYCP_REPO=${DEPLOYCP_REPO:-saiarlen/deployCP}
SQLITE_PATH=${CORE_DIR}/storage/db/deploycp.sqlite
SESSION_SECRET=$(openssl rand -hex 32)
SESSION_COOKIE_NAME=deploycp_session
SESSION_SECURE_COOKIES=auto
CSRF_ENABLED=true
LOGIN_RATE_LIMIT_PER_MIN=20
STORAGE_ROOT=${CORE_DIR}/storage
DEFAULT_SITE_ROOT=${DATA_DIR}/sites
LOG_ROOT=${DATA_DIR}/logs
RUNTIME_ROOT=${CORE_DIR}/storage/runtimes
HTPASSWD_ROOT=${CORE_DIR}/storage/generated/htpasswd
CRON_DIR=/etc/cron.d
NGINX_BINARY=${NGINX_BINARY}
NGINX_CONFIG_DIR=${NGINX_CONFIG_DIR}
NGINX_ENABLED_DIR=${NGINX_ENABLED_DIR}
NGINX_AVAILABLE_DIR=${NGINX_AVAILABLE_DIR}
SYSTEMCTL_BINARY=${SYSTEMCTL_BINARY}
RESTRICTED_SHELL_PATH=/usr/local/bin/deploycp-rshell
RUNUSER_BINARY=${RUNUSER_BINARY}
CERTBOT_BINARY=${CERTBOT_BINARY}
UFW_BINARY=${UFW_BINARY}
FIREWALLCMD_BINARY=${FIREWALLCMD_BINARY}
IPTABLES_BINARY=${IPTABLES_BINARY}
PLATFORM_MODE=auto
FEATURE_SERVICE_MANAGE=true
FEATURE_NGINX_MANAGE=true
MARIADB_ADMIN_USER=root
MARIADB_ADMIN_PASSWORD=
MARIADB_ADMIN_HOST=127.0.0.1
MARIADB_ADMIN_PORT=3306
POSTGRES_ADMIN_USER=postgres
POSTGRES_ADMIN_PASSWORD=
POSTGRES_ADMIN_HOST=127.0.0.1
POSTGRES_ADMIN_PORT=5432
POSTGRES_ADMIN_DB=postgres
ADMINER_URL=http://127.0.0.1:8081
ALERT_WEBHOOK_URL=
FTP_NOLOGIN_SHELL=/usr/sbin/nologin
PROFTPD_CONF_DIR=${PROFTPD_CONF_DIR}
PROFTPD_SERVICE_NAME=${PROFTPD_SERVICE_NAME}
REDIS_SERVER_BINARY=${REDIS_SERVER_BINARY}
VARNISH_CONFIG_DIR=${VARNISH_CONFIG_DIR}
VARNISH_SERVICE_NAME=${VARNISH_SERVICE_NAME}
VARNISH_MAIN_VCL=${VARNISH_MAIN_VCL}
VARNISH_INCLUDE_VCL=${VARNISH_INCLUDE_VCL}
VARNISHD_BINARY=${VARNISHD_BINARY}
VARNISHADM_BINARY=${VARNISHADM_BINARY}
VARNISH_ADMIN_ADDR=${VARNISH_ADMIN_ADDR}
VARNISH_SECRET_FILE=${VARNISH_SECRET_FILE}
BACKUP_TARGET_DIR=${DATA_DIR}/backups
BACKUP_RETENTION_DAYS=14
BACKUP_INCLUDE_SITE_CONTENT=true
BACKUP_INCLUDE_PLATFORM_LOGS=false
BACKUP_PRE_HOOK=
BACKUP_POST_HOOK=
EOF
  chown "$APP_USER:$APP_USER" "${CORE_DIR}/.env"
  chmod 0600 "${CORE_DIR}/.env"
fi

set_env_value "${CORE_DIR}/.env" "APP_VERSION" "$(resolved_release_version)"
set_env_value "${CORE_DIR}/.env" "DEPLOYCP_REPO" "${DEPLOYCP_REPO:-saiarlen/deployCP}"
set_env_value "${CORE_DIR}/.env" "ADMINER_URL" "http://127.0.0.1:8081"
install_default_php_runtime
if [[ -x "${CORE_DIR}/scripts/linux/harden-host.sh" ]]; then
  bash "${CORE_DIR}/scripts/linux/harden-host.sh"
fi

# Detect MariaDB socket auth — on fresh installs root can connect without a password.
if command -v mariadb >/dev/null 2>&1; then
  if mariadb -u root -e "SELECT 1" >/dev/null 2>&1; then
    set_env_value "${CORE_DIR}/.env" "MARIADB_ADMIN_USER" "root"
    set_env_value "${CORE_DIR}/.env" "MARIADB_ADMIN_PASSWORD" ""
  fi
elif command -v mysql >/dev/null 2>&1; then
  if mysql -u root -e "SELECT 1" >/dev/null 2>&1; then
    set_env_value "${CORE_DIR}/.env" "MARIADB_ADMIN_USER" "root"
    set_env_value "${CORE_DIR}/.env" "MARIADB_ADMIN_PASSWORD" ""
  fi
fi

# Detect PostgreSQL peer auth — on fresh installs postgres user can connect via peer.
if command -v psql >/dev/null 2>&1; then
  if su - postgres -c "psql -c 'SELECT 1'" >/dev/null 2>&1; then
    set_env_value "${CORE_DIR}/.env" "POSTGRES_ADMIN_USER" "postgres"
    set_env_value "${CORE_DIR}/.env" "POSTGRES_ADMIN_PASSWORD" ""
  fi
fi

ensure_firewall_access

cat >/etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=DeployCP Control Panel
After=network.target

[Service]
User=root
Group=root
WorkingDirectory=${CORE_DIR}
ExecStart=${CORE_DIR}/bin/${BIN_NAME}
Restart=on-failure
EnvironmentFile=${CORE_DIR}/.env
Environment=HOME=${APP_HOME}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
ensure_service_enabled nginx
ensure_service_enabled "$REDIS_SERVICE_NAME"
ensure_service_enabled "$PROFTPD_SERVICE_NAME"
ensure_service_enabled "$DB_SERVICE_NAME"
ensure_service_enabled "$POSTGRES_SERVICE_NAME"
ensure_service_enabled "$VARNISH_SERVICE_NAME"
ensure_service_enabled "$CRON_SERVICE_NAME"

if [[ -x "${CORE_DIR}/bin/${BIN_NAME}" ]]; then
  run_deploycp_cli_quiet bootstrap-host
  run_deploycp_cli_quiet reconcile-managed
  systemctl start "${SERVICE_NAME}"
  systemctl is-active --quiet "${SERVICE_NAME}"
  panel_ready=0
  for _ in {1..20}; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${APP_PORT}/robots.txt" >/dev/null 2>&1; then
      panel_ready=1
      break
    fi
    sleep 1
  done
  if [[ "$panel_ready" -ne 1 ]]; then
    echo "DeployCP HTTP readiness check failed on 127.0.0.1:${APP_PORT}" >&2
    exit 1
  fi
  run_deploycp_cli_quiet verify-host

  # Resolve display values for the post-install message.
  DISPLAY_PORT="$(read_env_value "${CORE_DIR}/.env" "APP_PORT")"
  DISPLAY_PORT="${DISPLAY_PORT:-2024}"
  SERVER_IP="$(detect_display_host)"
  DISPLAY_VERSION="$(read_env_value "${CORE_DIR}/.env" "APP_VERSION")"
  DISPLAY_VERSION="${DISPLAY_VERSION:-$(resolved_release_version)}"
  DISPLAY_VERSION="${DISPLAY_VERSION:-dev}"
  PANEL_URL="http://${SERVER_IP}:${DISPLAY_PORT}"

  echo ""
  echo "╔══════════════════════════════════════════════════════════════╗"
  echo "║                    DeployCP Installation Completed :)        ║"
  echo "╠══════════════════════════════════════════════════════════════╣"
  print_summary_row "Name" "DeployCP"
  print_summary_row "Version" "${DISPLAY_VERSION}"
  print_summary_row "Panel URL" "${PANEL_URL}"
  print_summary_row "Config" "${CORE_DIR}/.env"
  print_summary_row "Service" "systemctl status ${SERVICE_NAME}"
  print_summary_row "Logs" "journalctl -u ${SERVICE_NAME} -f"
  echo "╠══════════════════════════════════════════════════════════════╣"
  echo "║  Open the panel URL above to create your admin account.      ║"
  echo "╚══════════════════════════════════════════════════════════════╝"
  echo ""
else
  echo ""
  echo "DeployCP install layout prepared under ${CORE_DIR}"
  echo "Binary not found. Copy the release binary to ${CORE_DIR}/bin/${BIN_NAME}, then run:"
  echo ""
  echo "  ${CORE_DIR}/bin/${BIN_NAME} bootstrap-host"
  echo "  ${CORE_DIR}/bin/${BIN_NAME} reconcile-managed"
  echo "  systemctl start ${SERVICE_NAME}"
  echo ""
fi
