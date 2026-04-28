#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
runtime="${2:-}"
version="${3:-}"
runtime_root="${4:-}"

if [[ -z "$action" || -z "$runtime" || -z "$version" || -z "$runtime_root" ]]; then
  echo "usage: runtime-manager.sh <install|remove|set-default|list-remote> <runtime> <version> <runtime_root>" >&2
  exit 1
fi

version_dir="${runtime_root}/${runtime}/${version}"
bin_dir="${version_dir}/bin"
global_bin_dir="/usr/local/bin"
asdf_dir="${runtime_root}/_asdf"
asdf_data_dir="${runtime_root}/_asdf_data"
asdf_version="v0.18.0"
catalog_cache_dir="${runtime_root}/_catalog_cache"
catalog_cache_file="${catalog_cache_dir}/${runtime}.txt"

asdf_tool_name() {
  case "$runtime" in
    go) echo "golang" ;;
    node) echo "nodejs" ;;
    python) echo "python" ;;
    php) echo "php" ;;
    *)
      echo ""
      return 1
      ;;
  esac
}

asdf_plugin_url() {
  case "$runtime" in
    go) echo "https://github.com/asdf-community/asdf-golang.git" ;;
    node) echo "https://github.com/asdf-vm/asdf-nodejs.git" ;;
    python) echo "https://github.com/asdf-community/asdf-python.git" ;;
    php) echo "https://github.com/asdf-community/asdf-php.git" ;;
    *)
      echo ""
      return 1
      ;;
  esac
}

requested_runtime_version() {
  case "$runtime" in
    go) printf '%s\n' "${version#go}" ;;
    node) printf '%s\n' "${version#node}" ;;
    python) printf '%s\n' "${version#python}" ;;
    php) printf '%s\n' "$version" ;;
    *)
      printf '%s\n' "$version"
      ;;
  esac
}

if [[ "$action" != "list-remote" ]]; then
  mkdir -p "$bin_dir"
fi

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

install_manager_prereqs() {
  local manager="$1"
  case "$manager" in
    apt)
      install_packages "$manager" ca-certificates curl git gawk coreutils tar xz-utils unzip
      ;;
    dnf|yum)
      install_packages "$manager" ca-certificates curl git gawk coreutils tar xz unzip
      ;;
    zypper)
      install_packages "$manager" ca-certificates curl git gawk coreutils tar xz unzip
      ;;
    pacman)
      install_packages "$manager" ca-certificates curl git gawk coreutils tar xz unzip
      ;;
  esac
}

install_runtime_build_deps() {
  local manager="$1"
  case "$runtime" in
    node)
      case "$manager" in
        apt) install_packages "$manager" dirmngr gpg build-essential python3 ;;
        dnf|yum) install_packages "$manager" gnupg2 gcc gcc-c++ make python3 ;;
        zypper) install_packages "$manager" gpg2 gcc gcc-c++ make python3 ;;
        pacman) install_packages "$manager" gnupg base-devel python ;;
      esac
      ;;
    python)
      case "$manager" in
        apt) install_packages "$manager" build-essential libssl-dev zlib1g-dev libbz2-dev libreadline-dev libsqlite3-dev libffi-dev liblzma-dev tk-dev xz-utils ;;
        dnf|yum) install_packages "$manager" gcc make patch zlib-devel bzip2 bzip2-devel readline-devel sqlite sqlite-devel openssl-devel tk-devel libffi-devel xz-devel ;;
        zypper) install_packages "$manager" gcc make patch zlib-devel libbz2-devel readline-devel sqlite3-devel libopenssl-devel tk-devel libffi-devel xz-devel ;;
        pacman) install_packages "$manager" base-devel zlib bzip2 readline sqlite openssl tk libffi xz ;;
      esac
      ;;
    php)
      case "$manager" in
        apt) install_packages "$manager" build-essential autoconf bison re2c pkg-config libxml2-dev libsqlite3-dev libssl-dev libcurl4-openssl-dev libonig-dev libzip-dev ;;
        dnf|yum) install_packages "$manager" gcc gcc-c++ make autoconf bison re2c pkgconfig libxml2-devel sqlite-devel openssl-devel libcurl-devel oniguruma-devel libzip-devel ;;
        zypper) install_packages "$manager" gcc gcc-c++ make autoconf bison re2c pkg-config libxml2-devel sqlite3-devel libopenssl-devel libcurl-devel oniguruma-devel libzip-devel ;;
        pacman) install_packages "$manager" base-devel autoconf bison re2c pkgconf libxml2 sqlite openssl curl oniguruma libzip ;;
      esac
      ;;
  esac
}

ensure_asdf() {
  local manager="$1"
  install_manager_prereqs "$manager"
  mkdir -p "$runtime_root"
  if [[ ! -f "${asdf_dir}/asdf.sh" ]]; then
    rm -rf "$asdf_dir"
    git clone --branch "$asdf_version" https://github.com/asdf-vm/asdf.git "$asdf_dir"
  fi
  export ASDF_DIR="$asdf_dir"
  export ASDF_DATA_DIR="$asdf_data_dir"
  # shellcheck disable=SC1090
  . "${asdf_dir}/asdf.sh"
}

ensure_asdf_plugin() {
  local tool="$1"
  local url="$2"
  if [[ -z "$tool" || -z "$url" ]]; then
    return 1
  fi
  if ! asdf plugin list | grep -qx "$tool"; then
    asdf plugin add "$tool" "$url"
  fi
}

asdf_install_version() {
  local tool="$1"
  local req="$2"
  [[ -n "$tool" && -n "$req" ]] || return 1
  asdf install "$tool" "$req"
}

asdf_uninstall_version() {
  local tool="$1"
  local req="$2"
  [[ -n "$tool" && -n "$req" ]] || return 1
  asdf uninstall "$tool" "$req"
}

asdf_install_path() {
  local tool="$1"
  local req="$2"
  asdf where "$tool" "$req"
}

list_remote_versions() {
  local tool="$1"
  local raw
  raw="$(asdf list all "$tool" 2>/dev/null || true)"
  [[ -n "$raw" ]] || return 1
  case "$runtime" in
    go)
      printf '%s\n' "$raw" | awk '/^[0-9]+\.[0-9]+(\.[0-9]+)?$/ {print "go"$0}' | tail -n 8 | tac
      ;;
    node)
      printf '%s\n' "$raw" | awk '/^[0-9]+\.[0-9]+\.[0-9]+$/ {print "node"$0}' | tail -n 8 | tac
      ;;
    python)
      printf '%s\n' "$raw" | awk '/^[0-9]+\.[0-9]+(\.[0-9]+)?$/ {print "python"$0}' | tail -n 8 | tac
      ;;
    php)
      printf '%s\n' "$raw" | awk '/^[0-9]+\.[0-9]+(\.[0-9]+)?$/ {print $0}' | tail -n 8 | tac
      ;;
  esac
}

cat_cached_versions_if_fresh() {
  [[ -f "$catalog_cache_file" ]] || return 1
  find "$catalog_cache_file" -mmin -720 >/dev/null 2>&1 || return 1
  cat "$catalog_cache_file"
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
  local requested=""
  requested="$(requested_runtime_version)"
  local tool=""
  tool="$(asdf_tool_name)"
  local install_dir=""
  install_dir="$(asdf_install_path "$tool" "$requested" 2>/dev/null || true)"
  if [[ -z "$install_dir" || ! -d "$install_dir" ]]; then
    echo "failed to resolve managed runtime path for ${runtime}:${version}" >&2
    exit 1
  fi
  case "$runtime" in
    go)
      local go_bin=""
      go_bin="${install_dir}/bin/go"
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
      node_bin="${install_dir}/bin/node"
      if ! is_executable_file "$node_bin"; then
        echo "failed to prepare strict Node runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "node" "$node_bin" "node" "${version#node}"
      write_exec_wrapper "nodejs" "$node_bin" "node" "${version#node}"
      write_passthrough_wrapper "npm" "${install_dir}/bin/npm" || true
      write_passthrough_wrapper "npx" "${install_dir}/bin/npx" || true
      write_passthrough_wrapper "pm2" "${install_dir}/bin/pm2" || true
      ;;
    python)
      local python_bin=""
      python_bin="${install_dir}/bin/python"
      if [[ ! -x "$python_bin" && -x "${install_dir}/bin/python3" ]]; then
        python_bin="${install_dir}/bin/python3"
      fi
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
      php_bin="${install_dir}/bin/php"
      if ! is_executable_file "$php_bin"; then
        echo "failed to prepare strict PHP runtime for ${version}" >&2
        exit 1
      fi
      write_exec_wrapper "php${version}" "$php_bin" "php" "$version"
      write_exec_wrapper "php" "$php_bin" "php" "$version"
      ;;
  esac
}

set_system_default_runtime() {
  local target_bin=""
  case "$runtime" in
    go)
      target_bin="${bin_dir}/go"
      ;;
    node)
      target_bin="${bin_dir}/node"
      ;;
    python)
      target_bin="${bin_dir}/python3"
      ;;
    php)
      target_bin="${bin_dir}/php"
      ;;
    *)
      echo "unsupported runtime for system default: ${runtime}" >&2
      exit 1
      ;;
  esac
  if [[ ! -x "$target_bin" ]]; then
    echo "runtime ${runtime}:${version} is not prepared; install it first" >&2
    exit 1
  fi
  mkdir -p "$global_bin_dir"
  case "$runtime" in
    go)
      ln -sf "$target_bin" "${global_bin_dir}/go"
      if [[ -x "${bin_dir}/gofmt" ]]; then
        ln -sf "${bin_dir}/gofmt" "${global_bin_dir}/gofmt"
      fi
      ;;
    node)
      ln -sf "$target_bin" "${global_bin_dir}/node"
      ln -sf "$target_bin" "${global_bin_dir}/nodejs"
      [[ -x "${bin_dir}/npm" ]] && ln -sf "${bin_dir}/npm" "${global_bin_dir}/npm"
      [[ -x "${bin_dir}/npx" ]] && ln -sf "${bin_dir}/npx" "${global_bin_dir}/npx"
      [[ -x "${bin_dir}/pm2" ]] && ln -sf "${bin_dir}/pm2" "${global_bin_dir}/pm2"
      ;;
    python)
      ln -sf "$target_bin" "${global_bin_dir}/python3"
      [[ -x "${bin_dir}/python" ]] && ln -sf "${bin_dir}/python" "${global_bin_dir}/python"
      [[ -x "${bin_dir}/pip3" ]] && ln -sf "${bin_dir}/pip3" "${global_bin_dir}/pip3"
      [[ -x "${bin_dir}/pip" ]] && ln -sf "${bin_dir}/pip" "${global_bin_dir}/pip"
      ;;
    php)
      ln -sf "$target_bin" "${global_bin_dir}/php"
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
tool_name="$(asdf_tool_name)"
plugin_url="$(asdf_plugin_url)"
requested_version_value="$(requested_runtime_version)"

case "$action" in
  list-remote)
    mkdir -p "$catalog_cache_dir"
    if cat_cached_versions_if_fresh >/dev/null 2>&1; then
      cat_cached_versions_if_fresh
      exit 0
    fi
    ensure_asdf "$manager"
    ensure_asdf_plugin "$tool_name" "$plugin_url"
    list_remote_versions "$tool_name" | tee "$catalog_cache_file"
    ;;
  install)
    ensure_asdf "$manager"
    ensure_asdf_plugin "$tool_name" "$plugin_url"
    install_runtime_build_deps "$manager" || true
    asdf_install_version "$tool_name" "$requested_version_value"
    prepare_runtime_binaries
    ;;
  remove)
    ensure_asdf "$manager"
    ensure_asdf_plugin "$tool_name" "$plugin_url"
    asdf_uninstall_version "$tool_name" "$requested_version_value" || true
    rm -rf "$version_dir"
    ;;
  set-default)
    ensure_asdf "$manager"
    ensure_asdf_plugin "$tool_name" "$plugin_url"
    prepare_runtime_binaries
    set_system_default_runtime
    ;;
  *)
    echo "unsupported action: $action" >&2
    exit 1
    ;;
esac
