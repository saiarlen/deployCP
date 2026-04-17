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
  for candidate in "${PACKAGE_ROOT}/${BIN_NAME}" "$(pwd)/${BIN_NAME}"; do
    if [[ -x "$candidate" && "$candidate" != "$target" ]]; then
      cp "$candidate" "$target"
      chmod 0755 "$target"
      chown "${APP_USER}:${APP_USER}" "$target"
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

install_packages() {
  local manager="$1"
  case "$manager" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -y
      apt-get install -y nginx certbot curl tar sqlite3 ca-certificates openssl procps cron redis-server proftpd-basic varnish mariadb-server postgresql ufw
      ;;
    dnf)
      dnf install -y nginx certbot curl tar sqlite sqlite-libs ca-certificates openssl procps-ng cronie redis proftpd varnish mariadb-server postgresql-server firewalld
      ;;
    yum)
      yum install -y nginx certbot curl tar sqlite ca-certificates openssl procps-ng cronie redis proftpd varnish mariadb-server postgresql-server firewalld
      ;;
    zypper)
      zypper --non-interactive install nginx certbot curl tar sqlite3 ca-certificates openssl procps cron redis proftpd varnish mariadb postgresql-server firewalld
      ;;
    pacman)
      pacman -Sy --noconfirm nginx certbot curl tar sqlite ca-certificates openssl procps-ng cronie redis mariadb postgresql varnish ufw
      ;;
    *)
      echo "unsupported package manager" >&2
      exit 1
      ;;
  esac
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

  PROFTPD_SERVICE_NAME="$(first_service_name proftpd proftpd proftpd-basic)"
  REDIS_SERVICE_NAME="$(first_service_name redis-server redis-server redis)"
  DB_SERVICE_NAME="$(first_service_name mariadb mariadb mysql mysqld)"
  POSTGRES_SERVICE_NAME="$(first_service_name postgresql postgresql)"
  VARNISH_SERVICE_NAME="$(first_service_name varnish varnish)"
  CRON_SERVICE_NAME="$(first_service_name cron cron crond)"

  VARNISH_CONFIG_DIR="/etc/varnish/deploycp.d"
  VARNISH_MAIN_VCL="/etc/varnish/default.vcl"
  VARNISH_INCLUDE_VCL="${VARNISH_CONFIG_DIR}/deploycp.vcl"
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

detect_ssh_port() {
  local port=""
  if command -v sshd >/dev/null 2>&1; then
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
    app_port="${APP_PORT:-8080}"
  fi
  ssh_port="$(detect_ssh_port)"

  if command -v "$UFW_BINARY" >/dev/null 2>&1; then
    "$UFW_BINARY" allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
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
    "$FIREWALLCMD_BINARY" --permanent --add-port="${app_port}/tcp" >/dev/null 2>&1 || true
    if [[ "$ssh_port" == "22" ]]; then
      "$FIREWALLCMD_BINARY" --permanent --add-service=ssh >/dev/null 2>&1 || true
    else
      "$FIREWALLCMD_BINARY" --permanent --add-port="${ssh_port}/tcp" >/dev/null 2>&1 || true
    fi
    "$FIREWALLCMD_BINARY" --reload >/dev/null 2>&1 || true
    return
  fi

  if command -v "$IPTABLES_BINARY" >/dev/null 2>&1; then
    "$IPTABLES_BINARY" -C INPUT -p tcp --dport "$ssh_port" -j ACCEPT >/dev/null 2>&1 || "$IPTABLES_BINARY" -I INPUT -p tcp --dport "$ssh_port" -j ACCEPT >/dev/null 2>&1 || true
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
configure_platform_defaults
initialize_databases

if ! id -u "$APP_USER" >/dev/null 2>&1; then
  useradd --create-home --home-dir "$APP_HOME" --shell /bin/bash "$APP_USER"
fi

mkdir -p \
  "$CORE_DIR"/{bin,scripts,storage/db,storage/logs,storage/generated,storage/ssl,storage/runtimes,tmp,docs} \
  "$DATA_DIR"/{sites,logs,backups,tmp} \
  "$NGINX_AVAILABLE_DIR" \
  "$NGINX_ENABLED_DIR" \
  "$PROFTPD_CONF_DIR" \
  "$VARNISH_CONFIG_DIR"

chown -R "$APP_USER:$APP_USER" "$APP_HOME"
ensure_nginx_integration
ensure_varnish_integration
stage_release_binary || true
stage_release_assets

if [[ -f "${CORE_DIR}/.env" ]]; then
  :
else
  cat >"${CORE_DIR}/.env" <<EOF
APP_NAME=DeployCP
APP_ENV=production
APP_HOST=0.0.0.0
APP_PORT=8080
APP_BASE_URL=http://localhost:8080
APP_VERSION=$(resolved_release_version)
DEPLOYCP_REPO=${DEPLOYCP_REPO:-saiarlen/deployCP}
SQLITE_PATH=${CORE_DIR}/storage/db/deploycp.sqlite
SESSION_SECRET=$(openssl rand -hex 32)
SESSION_COOKIE_NAME=deploycp_session
SESSION_SECURE_COOKIES=false
CSRF_ENABLED=true
LOGIN_RATE_LIMIT_PER_MIN=20
BOOTSTRAP_ADMIN_USERNAME=admin
BOOTSTRAP_ADMIN_EMAIL=admin@localhost
BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -base64 18)
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
FTP_NOLOGIN_SHELL=/usr/sbin/nologin
PROFTPD_CONF_DIR=${PROFTPD_CONF_DIR}
PROFTPD_SERVICE_NAME=${PROFTPD_SERVICE_NAME}
REDIS_SERVER_BINARY=${REDIS_SERVER_BINARY}
VARNISH_CONFIG_DIR=${VARNISH_CONFIG_DIR}
VARNISH_SERVICE_NAME=${VARNISH_SERVICE_NAME}
VARNISH_MAIN_VCL=${VARNISH_MAIN_VCL}
VARNISH_INCLUDE_VCL=${VARNISH_INCLUDE_VCL}
VARNISHD_BINARY=${VARNISHD_BINARY}
EOF
  chown "$APP_USER:$APP_USER" "${CORE_DIR}/.env"
  chmod 0600 "${CORE_DIR}/.env"
fi

set_env_value "${CORE_DIR}/.env" "APP_VERSION" "$(resolved_release_version)"
set_env_value "${CORE_DIR}/.env" "DEPLOYCP_REPO" "${DEPLOYCP_REPO:-saiarlen/deployCP}"
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
  "${CORE_DIR}/bin/${BIN_NAME}" bootstrap-host
  "${CORE_DIR}/bin/${BIN_NAME}" reconcile-managed
  systemctl start "${SERVICE_NAME}"
  "${CORE_DIR}/bin/${BIN_NAME}" verify-host || true
  systemctl status "${SERVICE_NAME}" --no-pager || true
else
  echo "DeployCP install layout prepared under ${CORE_DIR}"
  echo "Copy the release binary to ${CORE_DIR}/bin/${BIN_NAME}, then rerun this installer or run:"
  echo "  ${CORE_DIR}/bin/${BIN_NAME} bootstrap-host"
  echo "  ${CORE_DIR}/bin/${BIN_NAME} reconcile-managed"
  echo "  systemctl start ${SERVICE_NAME}"
fi
