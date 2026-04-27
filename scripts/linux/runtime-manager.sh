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

package_available() {
  local manager="$1"
  local pkg="$2"
  case "$manager" in
    apt)
      apt-cache show "$pkg" >/dev/null 2>&1
      ;;
    dnf)
      dnf list --available "$pkg" >/dev/null 2>&1
      ;;
    yum)
      yum list available "$pkg" >/dev/null 2>&1
      ;;
    zypper)
      zypper --non-interactive search --match-exact "$pkg" >/dev/null 2>&1
      ;;
    pacman)
      pacman -Si "$pkg" >/dev/null 2>&1
      ;;
    *)
      return 1
      ;;
  esac
}

enable_module_stream() {
  local manager="$1"
  local module="$2"
  local stream="$3"
  case "$manager" in
    dnf|yum)
      "$manager" -y module reset "$module" >/dev/null 2>&1 || true
      "$manager" -y module enable "${module}:${stream}"
      ;;
    *)
      return 1
      ;;
  esac
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

is_executable_file() {
  local candidate="$1"
  [[ -n "$candidate" && -x "$candidate" && ! -d "$candidate" ]]
}

binary_matches_version() {
  local kind="$1"
  local candidate="$2"
  local expected="$3"
  local output=""
  if ! is_executable_file "$candidate"; then
    return 1
  fi
  case "$kind" in
    go)
      output="$("$candidate" version 2>/dev/null || true)"
      [[ "$output" == *"go${expected}"* ]]
      ;;
    node)
      output="$("$candidate" --version 2>/dev/null || true)"
      [[ "$output" == v${expected}.* || "$output" == "${expected}."* || "$output" == "$expected" ]]
      ;;
    python)
      output="$("$candidate" --version 2>&1 || true)"
      [[ "$output" == *"Python ${expected}"* ]]
      ;;
    php)
      output="$("$candidate" -v 2>/dev/null | head -n1 || true)"
      [[ "$output" == *"PHP ${expected}"* ]]
      ;;
    *)
      return 1
      ;;
  esac
}

detect_go_binary() {
  local expected="${version#go}"
  local candidates=(
    "/usr/lib/go-${expected}/bin/go"
    "/usr/lib/golang-${expected}/bin/go"
    "/usr/local/go${expected}/bin/go"
    "$(command -v "go${expected}" 2>/dev/null || true)"
    "$(command -v go 2>/dev/null || true)"
  )
  local candidate=""
  for candidate in "${candidates[@]}"; do
    if binary_matches_version go "$candidate" "$expected"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

detect_node_binary() {
  local expected="${version#node}"
  local candidates=(
    "$(command -v "node${expected}" 2>/dev/null || true)"
    "$(command -v "nodejs${expected}" 2>/dev/null || true)"
    "$(command -v node 2>/dev/null || true)"
    "$(command -v nodejs 2>/dev/null || true)"
  )
  local candidate=""
  for candidate in "${candidates[@]}"; do
    if binary_matches_version node "$candidate" "$expected"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

detect_python_binary() {
  local expected="${version#python}"
  local candidates=(
    "$(command -v "$version" 2>/dev/null || true)"
    "/usr/bin/${version}"
    "/usr/local/bin/${version}"
  )
  local candidate=""
  for candidate in "${candidates[@]}"; do
    if binary_matches_version python "$candidate" "$expected"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

detect_php_binary() {
  local expected="$version"
  local digits="${version/./}"
  local candidates=(
    "$(command -v "php${version}" 2>/dev/null || true)"
    "/usr/bin/php${version}"
    "/usr/local/bin/php${version}"
    "/opt/remi/php${digits}/root/usr/bin/php"
    "$(command -v php 2>/dev/null || true)"
  )
  local candidate=""
  for candidate in "${candidates[@]}"; do
    if binary_matches_version php "$candidate" "$expected"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

write_exec_wrapper() {
  local name="$1"
  local target="$2"
  local kind="$3"
  local expected="$4"
  if ! is_executable_file "$target"; then
    return 1
  fi
  cat > "${bin_dir}/${name}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
target=$(printf '%q' "$target")
if [[ ! -x "\$target" ]]; then
  echo "DeployCP runtime target missing: ${name}" >&2
  exit 1
fi
output=""
case $(printf '%q' "$kind") in
  go)
    output="\$("\$target" version 2>/dev/null || true)"
    [[ "\$output" == *"go$(printf '%q' "$expected")"* ]] || { echo "DeployCP runtime mismatch for ${name}: expected $(printf '%q' "$expected"), got \$output" >&2; exit 1; }
    ;;
  node)
    output="\$("\$target" --version 2>/dev/null || true)"
    case "\$output" in
      v$(printf '%q' "$expected").*|$(printf '%q' "$expected").*|$(printf '%q' "$expected")) ;;
      *) echo "DeployCP runtime mismatch for ${name}: expected $(printf '%q' "$expected"), got \$output" >&2; exit 1 ;;
    esac
    ;;
  python)
    output="\$("\$target" --version 2>&1 || true)"
    [[ "\$output" == *"Python $(printf '%q' "$expected")"* ]] || { echo "DeployCP runtime mismatch for ${name}: expected Python $(printf '%q' "$expected"), got \$output" >&2; exit 1; }
    ;;
  php)
    output="\$("\$target" -v 2>/dev/null | head -n1 || true)"
    [[ "\$output" == *"PHP $(printf '%q' "$expected")"* ]] || { echo "DeployCP runtime mismatch for ${name}: expected PHP $(printf '%q' "$expected"), got \$output" >&2; exit 1; }
    ;;
esac
exec "\$target" "\$@"
EOF
  chmod 755 "${bin_dir}/${name}"
}

write_python_module_wrapper() {
  local name="$1"
  local python_bin="$2"
  local module="$3"
  if ! is_executable_file "$python_bin"; then
    return 1
  fi
  cat > "${bin_dir}/${name}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
python_bin=$(printf '%q' "$python_bin")
if [[ ! -x "\$python_bin" ]]; then
  echo "DeployCP runtime target missing: ${name}" >&2
  exit 1
fi
exec "\$python_bin" -m $(printf '%q' "$module") "\$@"
EOF
  chmod 755 "${bin_dir}/${name}"
}

write_passthrough_wrapper() {
  local name="$1"
  local target="$2"
  if ! is_executable_file "$target"; then
    return 1
  fi
  cat > "${bin_dir}/${name}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
target=$(printf '%q' "$target")
if [[ ! -x "\$target" ]]; then
  echo "DeployCP runtime target missing: ${name}" >&2
  exit 1
fi
exec "\$target" "\$@"
EOF
  chmod 755 "${bin_dir}/${name}"
}

prepare_runtime_binaries() {
  rm -f "${bin_dir}/"*
  case "$runtime" in
    go)
      local go_bin=""
      go_bin="$(detect_go_binary || true)"
      if ! is_executable_file "$go_bin"; then
        echo "failed to prepare strict Go runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "go" "$go_bin" "go" "${version#go}"
      if is_executable_file "$(dirname "$go_bin")/gofmt"; then
        write_passthrough_wrapper "gofmt" "$(dirname "$go_bin")/gofmt"
      fi
      ;;
    node)
      local node_bin=""
      node_bin="$(detect_node_binary || true)"
      if ! is_executable_file "$node_bin"; then
        echo "failed to prepare strict Node runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "node" "$node_bin" "node" "${version#node}"
      write_exec_wrapper "nodejs" "$node_bin" "node" "${version#node}"
      write_passthrough_wrapper "npm" "$(command -v npm || true)" || true
      write_passthrough_wrapper "npx" "$(command -v npx || true)" || true
      write_passthrough_wrapper "pm2" "$(command -v pm2 || true)" || true
      ;;
    python)
      local python_bin=""
      python_bin="$(detect_python_binary || true)"
      if ! is_executable_file "$python_bin"; then
        echo "failed to prepare strict Python runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "$version" "$python_bin" "python" "${version#python}"
      write_exec_wrapper "python3" "$python_bin" "python" "${version#python}"
      write_exec_wrapper "python" "$python_bin" "python" "${version#python}"
      write_python_module_wrapper "pip3" "$python_bin" "pip" || true
      write_python_module_wrapper "pip" "$python_bin" "pip" || true
      write_passthrough_wrapper "gunicorn" "$(command -v gunicorn || true)" || true
      write_passthrough_wrapper "uwsgi" "$(command -v uwsgi || true)" || true
      ;;
    php)
      local php_bin=""
      php_bin="$(detect_php_binary || true)"
      if ! is_executable_file "$php_bin"; then
        echo "failed to prepare strict PHP runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "php${version}" "$php_bin" "php" "$version"
      write_exec_wrapper "php" "$php_bin" "php" "$version"
      ;;
  esac
}

package_list() {
  local manager="$1"
  case "$runtime" in
    go)
      case "$manager" in
        apt)
          local go_minor=""
          if [[ "$version" =~ ^go([0-9]+)\.([0-9]+) ]]; then
            go_minor="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"
          fi
          if [[ -n "$go_minor" ]]; then
            echo "golang-${go_minor}-go"
          else
            echo "golang-go"
          fi
          ;;
        dnf|yum) echo "golang" ;;
        zypper) echo "go" ;;
        pacman) echo "go" ;;
      esac
      ;;
    node)
      case "$manager" in
        apt|zypper|pacman) echo "nodejs npm" ;;
        dnf|yum) echo "nodejs npm" ;;
      esac
      ;;
    python)
      case "$manager" in
        apt) echo "${version} ${version}-venv python3-pip" ;;
        dnf|yum)
          local py_version=""
          if [[ "$version" =~ ^python([0-9]+\.[0-9]+) ]]; then
            py_version="${BASH_REMATCH[1]}"
          fi
          if [[ -n "$py_version" ]] && package_available "$manager" "python${py_version}"; then
            echo "python${py_version}"
          else
            echo "python3 python3-pip"
          fi
          ;;
        zypper) echo "python3 python3-pip" ;;
        pacman) echo "python python-pip" ;;
      esac
      ;;
    php)
      case "$manager" in
        apt) echo "php${version}-cli php${version}-fpm" ;;
        dnf|yum)
          local php_digits=""
          if [[ "$version" =~ ^([0-9]+)\.([0-9]+) ]]; then
            php_digits="${BASH_REMATCH[1]}${BASH_REMATCH[2]}"
          fi
          if [[ -n "$php_digits" ]] && package_available "$manager" "php${php_digits}-php-cli"; then
            echo "php${php_digits}-php-cli php${php_digits}-php-fpm"
          else
            echo "php-cli php-fpm"
          fi
          ;;
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
    if [[ "$manager" == "dnf" || "$manager" == "yum" ]]; then
      if [[ "$runtime" == "node" && "$version" =~ ^node([0-9]+) ]]; then
        enable_module_stream "$manager" "nodejs" "${BASH_REMATCH[1]}" || true
      fi
      if [[ "$runtime" == "php" && "$version" =~ ^([0-9]+\.[0-9]+)$ ]]; then
        php_digits="${version/./}"
        if ! package_available "$manager" "php${php_digits}-php-cli"; then
          enable_module_stream "$manager" "php" "$version" || true
        fi
      fi
    fi
    if [[ -n "$pkgs" ]]; then
      # shellcheck disable=SC2086
      install_packages "$manager" $pkgs
    fi
    prepare_runtime_binaries
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
