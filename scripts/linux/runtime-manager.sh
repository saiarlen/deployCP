#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
runtime="${2:-}"
version="${3:-}"
runtime_root="${4:-}"

if [[ -z "$action" || -z "$runtime" || -z "$version" || -z "$runtime_root" ]]; then
  echo "usage: runtime-manager.sh <install|remove> <runtime> <version> <runtime_root>" >&2
  exit 1
fi

version_dir="${runtime_root}/${runtime}/${version}"
bin_dir="${version_dir}/bin"
mkdir -p "$bin_dir"

pkg_manager() {
  if command -v apt-get >/dev/null 2>&1; then echo apt; return; fi
  if command -v dnf >/dev/null 2>&1; then echo dnf; return; fi
  if command -v yum >/dev/null 2>&1; then echo yum; return; fi
  if command -v zypper >/dev/null 2>&1; then echo zypper; return; fi
  if command -v pacman >/dev/null 2>&1; then echo pacman; return; fi
  echo ""
}

install_packages() {
  local manager="$1"
  shift
  case "$manager" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -y
      apt-get install -y "$@"
      ;;
    dnf) dnf install -y "$@" ;;
    yum) yum install -y "$@" ;;
    zypper) zypper --non-interactive install "$@" ;;
    pacman) pacman -Sy --noconfirm "$@" ;;
    *) echo "no supported package manager found" >&2; exit 1 ;;
  esac
}

remove_packages() {
  local manager="$1"
  shift
  case "$manager" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get remove -y "$@"
      ;;
    dnf) dnf remove -y "$@" ;;
    yum) yum remove -y "$@" ;;
    zypper) zypper --non-interactive remove "$@" ;;
    pacman) pacman -Rns --noconfirm "$@" ;;
    *) ;;
  esac
}

link_if_exists() {
  local candidate="$1"
  local target_name="$2"
  if [[ -x "$candidate" ]]; then
    ln -sf "$candidate" "${bin_dir}/${target_name}"
    return 0
  fi
  return 1
}

link_runtime_binaries() {
  case "$runtime" in
    go)
      link_if_exists "$(command -v go || true)" "go"
      ;;
    node)
      link_if_exists "$(command -v node || true)" "node"
      link_if_exists "$(command -v npm || true)" "npm"
      link_if_exists "$(command -v npx || true)" "npx"
      link_if_exists "$(command -v pm2 || true)" "pm2"
      ;;
    python)
      local py="${version}"
      link_if_exists "$(command -v "$py" || true)" "$py"
      link_if_exists "$(command -v python3 || true)" "python3"
      link_if_exists "$(command -v pip3 || true)" "pip3"
      link_if_exists "$(command -v gunicorn || true)" "gunicorn"
      link_if_exists "$(command -v uwsgi || true)" "uwsgi"
      ;;
    php)
      local php_bin="php${version}"
      link_if_exists "$(command -v "$php_bin" || true)" "$php_bin"
      link_if_exists "$(command -v php || true)" "php"
      ;;
  esac
}

package_list() {
  local manager="$1"
  case "$runtime" in
    go)
      case "$manager" in
        apt) echo "golang-go" ;;
        dnf|yum) echo "golang" ;;
        zypper) echo "go" ;;
        pacman) echo "go" ;;
      esac
      ;;
    node)
      case "$manager" in
        apt|dnf|yum|zypper|pacman) echo "nodejs npm" ;;
      esac
      ;;
    python)
      case "$manager" in
        apt) echo "${version} ${version}-venv python3-pip" ;;
        dnf|yum) echo "python3 python3-pip" ;;
        zypper) echo "python3 python3-pip" ;;
        pacman) echo "python python-pip" ;;
      esac
      ;;
    php)
      case "$manager" in
        apt) echo "php${version}-cli php${version}-fpm" ;;
        dnf|yum) echo "php-cli php-fpm" ;;
        zypper) echo "php8 php8-fpm" ;;
        pacman) echo "php php-fpm" ;;
      esac
      ;;
  esac
}

manager="$(pkg_manager)"
pkgs="$(package_list "$manager")"

case "$action" in
  install)
    if [[ -n "$pkgs" ]]; then
      # shellcheck disable=SC2086
      install_packages "$manager" $pkgs
    fi
    link_runtime_binaries
    ;;
  remove)
    if [[ -n "$pkgs" ]]; then
      # shellcheck disable=SC2086
      remove_packages "$manager" $pkgs || true
    fi
    rm -rf "$version_dir"
    ;;
  *)
    echo "unsupported action: $action" >&2
    exit 1
    ;;
esac
