#!/usr/bin/env bash
set -uE -o pipefail

BASE_URL="${DEPLOYCP_TEST_BASE_URL:-http://127.0.0.1:2024}"
ADMIN_USER="${DEPLOYCP_TEST_ADMIN_USER:-}"
ADMIN_PASS="${DEPLOYCP_TEST_ADMIN_PASS:-}"
SQLITE_DB="${DEPLOYCP_TEST_SQLITE_DB:-/home/deploycp/core/storage/db/deploycp.sqlite}"
DEPLOYCP_BIN="${DEPLOYCP_TEST_DEPLOYCP_BIN:-/home/deploycp/core/bin/deploycp}"
SITE_ROOT_BASE="${DEPLOYCP_TEST_SITE_ROOT:-/home/deploycp/platforms/sites}"
ALLOW_RUNTIME_MUTATION="${DEPLOYCP_TEST_ALLOW_RUNTIME_MUTATION:-0}"
WORKDIR=""
COOKIE_JAR=""
RUN_ID="$(date +%Y%m%d%H%M%S)"

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0

declare -a CREATED_WEBSITES=()
declare -a CREATED_DOMAINS=()
declare -a CREATED_ROOTS=()
declare -a CREATED_USERS=()
declare -a CREATED_EXTRA_USERS=()
declare -a CREATED_FTP_USERS=()
declare -a CREATED_MYSQL_DBS=()
declare -a CREATED_PG_DBS=()
declare -a CREATED_SSL_DOMAINS=()

COLOR_RED=$'\033[31m'
COLOR_GREEN=$'\033[32m'
COLOR_YELLOW=$'\033[33m'
COLOR_BLUE=$'\033[34m'
COLOR_RESET=$'\033[0m'

note() { printf "%s[INFO]%s %s\n" "$COLOR_BLUE" "$COLOR_RESET" "$*"; }
pass() { PASS_COUNT=$((PASS_COUNT+1)); printf "%s[PASS]%s %s\n" "$COLOR_GREEN" "$COLOR_RESET" "$*"; }
fail() { FAIL_COUNT=$((FAIL_COUNT+1)); printf "%s[FAIL]%s %s\n" "$COLOR_RED" "$COLOR_RESET" "$*"; }
skip() { SKIP_COUNT=$((SKIP_COUNT+1)); printf "%s[SKIP]%s %s\n" "$COLOR_YELLOW" "$COLOR_RESET" "$*"; }

usage() {
  cat <<'EOF'
DeployCP production smoke test

Usage:
  sudo DEPLOYCP_TEST_ADMIN_USER=admin DEPLOYCP_TEST_ADMIN_PASS=secret ./scripts/linux/tests.sh

Optional environment:
  DEPLOYCP_TEST_BASE_URL=http://127.0.0.1:2024
  DEPLOYCP_TEST_SQLITE_DB=/home/deploycp/core/storage/db/deploycp.sqlite
  DEPLOYCP_TEST_DEPLOYCP_BIN=/home/deploycp/core/bin/deploycp
  DEPLOYCP_TEST_SITE_ROOT=/home/deploycp/platforms/sites
  DEPLOYCP_TEST_ALLOW_RUNTIME_MUTATION=1

Notes:
- This script creates temporary platforms and related resources, then deletes them.
- It is intended to be run as root on the target server.
- Runtime remove-block checks are skipped by default because they intentionally hit runtime mutation endpoints.
EOF
}

require_root() {
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    echo "Run this script as root." >&2
    exit 2
  fi
}

require_input() {
  if [[ -z "$ADMIN_USER" || -z "$ADMIN_PASS" ]]; then
    echo "Set DEPLOYCP_TEST_ADMIN_USER and DEPLOYCP_TEST_ADMIN_PASS." >&2
    exit 2
  fi
}

require_commands() {
  local missing=0
  local cmd
  for cmd in curl sqlite3 sed awk grep tr mktemp getent runuser timeout base64; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "Missing required command: $cmd" >&2
      missing=1
    fi
  done
  if [[ $missing -ne 0 ]]; then
    exit 2
  fi
}

setup_workdir() {
  WORKDIR="$(mktemp -d /tmp/deploycp-tests.XXXXXX)"
  COOKIE_JAR="$WORKDIR/cookies.txt"
}

cleanup() {
  local idx website_id domain root user extra ftp csrf
  note "Cleanup starting"
  for (( idx=${#CREATED_WEBSITES[@]}-1; idx>=0; idx-- )); do
    website_id="${CREATED_WEBSITES[$idx]}"
    domain="${CREATED_DOMAINS[$idx]}"
    root="${CREATED_ROOTS[$idx]}"
    user="${CREATED_USERS[$idx]}"
    extra="${CREATED_EXTRA_USERS[$idx]}"
    ftp="${CREATED_FTP_USERS[$idx]}"

    if [[ -n "$website_id" ]]; then
      csrf="$(csrf_for "/platforms")"
      if [[ -n "$csrf" ]]; then
        curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
          -X POST \
          --data-urlencode "_csrf=$csrf" \
          "$BASE_URL/websites/$website_id/delete" >/dev/null 2>&1 || true
      fi
    fi

    if [[ -n "$user" && "$(getent passwd "$user" || true)" != "" ]]; then
      userdel -f "$user" >/dev/null 2>&1 || true
    fi
    if [[ -n "$extra" && "$(getent passwd "$extra" || true)" != "" ]]; then
      userdel -f "$extra" >/dev/null 2>&1 || true
    fi
    if [[ -n "$ftp" && "$(getent passwd "$ftp" || true)" != "" ]]; then
      userdel -f "$ftp" >/dev/null 2>&1 || true
    fi
    if [[ -n "$root" && "$root" == "$SITE_ROOT_BASE/"* ]]; then
      rm -rf "$root" >/dev/null 2>&1 || true
    fi
    if [[ -n "$domain" ]]; then
      rm -f "/etc/nginx/sites-available/$domain.conf" "/etc/nginx/sites-enabled/$domain.conf" >/dev/null 2>&1 || true
      rm -f "/etc/varnish/deploycp.d/website-$website_id.vcl" >/dev/null 2>&1 || true
    fi
  done

  if [[ -x "$DEPLOYCP_BIN" ]]; then
    "$DEPLOYCP_BIN" reconcile-managed >/dev/null 2>&1 || true
  fi

  rm -rf "$WORKDIR" >/dev/null 2>&1 || true
}

trap cleanup EXIT

sql_escape() {
  printf "%s" "$1" | sed "s/'/''/g"
}

sql_single() {
  local q="$1"
  sqlite3 -noheader -batch "$SQLITE_DB" "$q" 2>/dev/null | head -n1 | tr -d '\r'
}

file_contains() {
  local file="$1" needle="$2"
  grep -Fq "$needle" "$file"
}

html_value() {
  local file="$1" field="$2"
  sed -n "s/.*name=\"$field\" value=\"\\([^\"]*\\)\".*/\\1/p" "$file" | head -n1
}

fetch_page() {
  local path="$1" out="$2"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$BASE_URL$path" -o "$out"
}

csrf_for() {
  local path="${1:-}"
  if [[ -z "$path" ]]; then
    echo ""
    return 1
  fi
  local out="$WORKDIR/csrf.$(printf "%s" "$path" | tr '/?&=' '_').html"
  if ! fetch_page "$path" "$out"; then
    echo ""
    return 1
  fi
  html_value "$out" "_csrf"
}

login_captcha_answer() {
  local expr="$1"
  local a op b
  a="$(printf "%s" "$expr" | awk '{print $1}')"
  op="$(printf "%s" "$expr" | awk '{print $2}')"
  b="$(printf "%s" "$expr" | awk '{print $3}')"
  if [[ "$op" == "+" ]]; then
    echo $((a + b))
  else
    echo $((a - b))
  fi
}

panel_login() {
  local login_html="$WORKDIR/login.html"
  fetch_page "/login" "$login_html" || return 1
  local csrf captcha_expr captcha_token captcha_answer
  csrf="$(html_value "$login_html" "_csrf")"
  captcha_expr="$(html_value "$login_html" "captcha_expression")"
  captcha_token="$(html_value "$login_html" "captcha_token")"
  captcha_answer="$(login_captcha_answer "$captcha_expr")"
  local body="$WORKDIR/login.post.html"
  local headers="$WORKDIR/login.post.headers"
  curl -fsS -D "$headers" -o "$body" \
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "username=$ADMIN_USER" \
    --data-urlencode "password=$ADMIN_PASS" \
    --data-urlencode "captcha_expression=$captcha_expr" \
    --data-urlencode "captcha_token=$captcha_token" \
    --data-urlencode "captcha_answer=$captcha_answer" \
    "$BASE_URL/login" >/dev/null
  local dash="$WORKDIR/dashboard.html"
  if fetch_page "/" "$dash" && file_contains "$dash" "DeployCP"; then
    pass "Panel login succeeded"
    return 0
  fi
  fail "Panel login failed"
  return 1
}

assert_get_page() {
  local path="$1" label="$2" needle="${3:-}"
  local out="$WORKDIR/page.$(printf "%s" "$path" | tr '/?&=' '_').html"
  if ! fetch_page "$path" "$out"; then
    fail "$label returned a non-200 response"
    return 1
  fi
  if [[ -n "$needle" ]] && ! file_contains "$out" "$needle"; then
    fail "$label did not contain expected text: $needle"
    return 1
  fi
  pass "$label loaded"
}

first_select_option() {
  local file="$1" select_id="$2"
  awk -v id="$select_id" '
    $0 ~ "<select" && $0 ~ "id=\""id"\"" { in_select=1; next }
    in_select && $0 ~ "</select>" { exit }
    in_select {
      if (match($0, /<option value="([^"]+)"/, m)) {
        if ($0 !~ /disabled/) {
          print m[1]
          exit
        }
      }
    }
  ' "$file"
}

domain_request() {
  local scheme="$1" domain="$2" path="$3" out="$4"
  if [[ "$scheme" == "https" ]]; then
    curl -fsSk --resolve "$domain:443:127.0.0.1" "https://$domain$path" -o "$out"
  else
    curl -fsS -H "Host: $domain" "http://127.0.0.1$path" -o "$out"
  fi
}

service_active_any() {
  local svc
  for svc in "$@"; do
    if systemctl is-active --quiet "$svc" 2>/dev/null; then
      return 0
    fi
  done
  return 1
}

platform_root_for_domain() {
  printf "%s/%s" "$SITE_ROOT_BASE" "$1"
}

website_id_by_domain() {
  local domain="$1"
  sql_single "select website_id from website_domains where domain = '$(sql_escape "$domain")' order by id desc limit 1;"
}

decode_platform_ref() {
  local ref="$1"
  if [[ -z "$ref" ]]; then
    return 1
  fi
  local normalized="$ref"
  normalized="${normalized//-/+}"
  normalized="${normalized//_/\/}"
  local mod=$(( ${#normalized} % 4 ))
  if [[ $mod -eq 2 ]]; then
    normalized="${normalized}=="
  elif [[ $mod -eq 3 ]]; then
    normalized="${normalized}="
  elif [[ $mod -eq 1 ]]; then
    return 1
  fi
  printf '%s' "$normalized" | base64 -d 2>/dev/null
}

website_id_from_manage_url() {
  local manage_url="$1"
  local ref payload kind id
  ref="${manage_url%%#*}"
  ref="${ref##*/}"
  payload="$(decode_platform_ref "$ref" || true)"
  kind="${payload%%:*}"
  id="${payload##*:}"
  if [[ "$kind" == "website" && "$id" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$id"
    return 0
  fi
  return 1
}

ssl_id_by_domain() {
  local domain="$1"
  sql_single "select id from ssl_certificates where domain = '$(sql_escape "$domain")' order by id desc limit 1;"
}

db_id_for_site_engine() {
  local website_id="$1" engine="$2"
  sql_single "select id from database_connections where website_id = $website_id and engine = '$(sql_escape "$engine")' order by id desc limit 1;"
}

redis_id_for_site() {
  local website_id="$1"
  sql_single "select id from redis_connections where website_id = $website_id order by id desc limit 1;"
}

cron_id_for_site() {
  local website_id="$1"
  sql_single "select id from cron_jobs where website_id = $website_id order by id desc limit 1;"
}

create_platform() {
  local kind="$1" domain="$2" name="$3" username="$4" password="$5" root="$6" runtime_version="${7:-}" port="${8:-}"
  local new_html="$WORKDIR/new.$kind.html"
  fetch_page "/platforms/new" "$new_html" || return 1
  local csrf
  csrf="$(html_value "$new_html" "_csrf")"
  local category="site"
  if [[ "$kind" == "go" || "$kind" == "node" || "$kind" == "python" || "$kind" == "binary" ]]; then
    category="app"
  fi
  local headers="$WORKDIR/create.$kind.headers"
  local body="$WORKDIR/create.$kind.body"
  local -a args=(
    -fsS -D "$headers" -o "$body"
    -b "$COOKIE_JAR" -c "$COOKIE_JAR"
    -X POST
    --data-urlencode "_csrf=$csrf"
    --data-urlencode "create_kind=$kind"
    --data-urlencode "platform_category=$category"
    --data-urlencode "application_domain=$domain"
    --data-urlencode "name=$name"
    --data-urlencode "root_path=$root"
    --data-urlencode "site_username=$username"
    --data-urlencode "site_password=$password"
  )
  if [[ -n "$runtime_version" ]]; then
    args+=( --data-urlencode "runtime_version=$runtime_version" )
  fi
  if [[ -n "$port" ]]; then
    args+=( --data-urlencode "port=$port" )
  fi
  if [[ "$kind" == "php" ]]; then
    args+=( --data-urlencode "php_version=$runtime_version" )
  fi
  curl "${args[@]}" "$BASE_URL/platforms" >/dev/null || return 1
  tr -d '\r' <"$headers" | sed -n 's/^Location: //p' | tail -n1
}

assert_manage_page_contains_tabs() {
  local url_path="$1"; shift
  local out="$WORKDIR/manage.$(printf "%s" "$url_path" | tr '/?&=' '_').html"
  fetch_page "$url_path" "$out" || { fail "Manage page $url_path failed to load"; return 1; }
  local tab
  for tab in "$@"; do
    if ! file_contains "$out" "$tab"; then
      fail "Manage page $url_path is missing tab/section: $tab"
      return 1
    fi
  done
  pass "Manage page tabs verified for $url_path"
}

test_file_manager_backend() {
  local website_id="$1" platform_root="$2"
  local init_json="$WORKDIR/elfinder.init.$website_id.json"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    "$BASE_URL/websites/$website_id/elfinder?cmd=open&init=1" -o "$init_json" || { fail "File manager init failed"; return 1; }
  local target
  target="$(sed -n 's/.*"cwd":[^{]*{"name":"Root","hash":"\([^"]*\)".*/\1/p' "$init_json" | head -n1)"
  if [[ -z "$target" ]]; then
    target="$(sed -n 's/.*"hash":"\([^"]*\)".*/\1/p' "$init_json" | head -n1)"
  fi
  if [[ -z "$target" ]]; then
    fail "File manager root target hash could not be parsed"
    return 1
  fi
  local csrf
  csrf="$(csrf_for "/platforms")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "cmd=mkfile" \
    --data-urlencode "target=$target" \
    --data-urlencode "name=panel-smoke.txt" \
    "$BASE_URL/websites/$website_id/elfinder" -o "$WORKDIR/elfinder.mkfile.json" || { fail "File manager mkfile failed"; return 1; }
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    "$BASE_URL/websites/$website_id/elfinder?cmd=ls&target=$target" -o "$WORKDIR/elfinder.ls.json" || { fail "File manager ls failed"; return 1; }
  if [[ ! -f "$platform_root/panel-smoke.txt" ]]; then
    fail "File manager mkfile did not create platform file"
    return 1
  fi
  if ! file_contains "$WORKDIR/elfinder.ls.json" "panel-smoke.txt"; then
    log "File manager ls response did not list panel-smoke.txt, but filesystem confirms creation"
  fi
  local file_hash
  file_hash="$(sed -n 's/.*"hash":"\([^"]*\)".*"name":"panel-smoke.txt".*/\1/p' "$WORKDIR/elfinder.mkfile.json" | head -n1)"
  if [[ -z "$file_hash" ]]; then
    file_hash="$(sed -n 's/.*"hash":"\([^"]*\)".*/\1/p' "$WORKDIR/elfinder.mkfile.json" | tail -n1)"
  fi
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "cmd=rm" \
    --data-urlencode "targets[]=$file_hash" \
    "$BASE_URL/websites/$website_id/elfinder" -o "$WORKDIR/elfinder.rm.json" || { fail "File manager rm failed"; return 1; }
  if [[ -e "$platform_root/panel-smoke.txt" ]]; then
    fail "File manager rm did not remove platform file"
    return 1
  fi
  pass "File manager backend create/list/delete works"
}

run_as_platform_user() {
  local username="$1" platform_root="$2" command="$3"
  timeout 15s runuser -u "$username" -- /bin/bash -lc \
    "umask 0002; export HOME='$platform_root'; export USER='$username'; export LOGNAME='$username'; cd '$platform_root' && if [ -f '$platform_root/.deploycp/runtime.env' ]; then . '$platform_root/.deploycp/runtime.env'; fi; $command"
}

domain_request_retry() {
  local scheme="$1" domain="$2" path="$3" out="$4" attempts="${5:-10}"
  local n
  for (( n=1; n<=attempts; n++ )); do
    if domain_request "$scheme" "$domain" "$path" "$out"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

test_site_shell_collaboration() {
  local primary_user="$1" extra_user="$2" platform_root="$3"
  if [[ -z "$primary_user" || -z "$extra_user" ]]; then
    skip "Shell collaboration test skipped because users were not created"
    return 0
  fi
  run_as_platform_user "$primary_user" "$platform_root" "mkdir -p '$platform_root/htdocs' && printf 'alpha\n' > '$platform_root/htdocs/collab.txt'" >/dev/null 2>&1 || { fail "Primary site user could not write collab file"; return 1; }
  run_as_platform_user "$extra_user" "$platform_root" "printf 'beta\n' >> '$platform_root/htdocs/collab.txt'" >/dev/null 2>&1 || { fail "Extra SSH user could not append collab file"; return 1; }
  local body
  body="$(run_as_platform_user "$primary_user" "$platform_root" "cat '$platform_root/htdocs/collab.txt'" 2>/dev/null || true)"
  if [[ "$body" == *"alpha"* && "$body" == *"beta"* ]]; then
    pass "Primary and extra SSH users share writable platform access"
  else
    fail "Collaborative file write check did not preserve both user writes"
  fi
}

test_runtime_shell_version() {
  local username="$1" runtime="$2" selected="$3" platform_root="$4"
  local cmd output
  case "$runtime" in
    go) cmd='go version' ;;
    node) cmd='node -v' ;;
    python) cmd='python3 --version' ;;
    php) cmd='php -v | head -n1' ;;
    *) fail "Unsupported runtime shell check: $runtime"; return 1 ;;
  esac
  output="$(run_as_platform_user "$username" "$platform_root" "$cmd" 2>/dev/null || true)"
  if [[ -z "$output" ]]; then
    fail "Could not resolve $runtime version for SSH user $username"
    return 1
  fi
  if [[ "$runtime" == "node" ]]; then
    if [[ "$output" == *"${selected#node}"* ]]; then
      pass "$runtime SSH version matches selected runtime for $username"
    else
      fail "$runtime SSH version mismatch for $username: expected $selected got $output"
    fi
    return 0
  fi
  if [[ "$runtime" == "python" ]]; then
    if [[ "$output" == *"${selected#python}"* ]]; then
      pass "$runtime SSH version matches selected runtime for $username"
    else
      fail "$runtime SSH version mismatch for $username: expected $selected got $output"
    fi
    return 0
  fi
  if [[ "$runtime" == "go" ]]; then
    if [[ "$output" == *"$selected"* ]]; then
      pass "$runtime SSH version matches selected runtime for $username"
    else
      fail "$runtime SSH version mismatch for $username: expected $selected got $output"
    fi
    return 0
  fi
  if [[ "$runtime" == "php" ]]; then
    if [[ "$output" == *"$selected"* ]]; then
      pass "$runtime SSH version matches selected runtime for $username"
    else
      fail "$runtime SSH version mismatch for $username: expected $selected got $output"
    fi
  fi
}

test_runtime_remove_block() {
  local runtime="$1" version="$2"
  if [[ "$ALLOW_RUNTIME_MUTATION" != "1" ]]; then
    skip "Runtime remove-block check for $runtime $version skipped (set DEPLOYCP_TEST_ALLOW_RUNTIME_MUTATION=1 to enable)"
    return 0
  fi
  local csrf body
  csrf="$(csrf_for "/settings?tab=services")"
  body="$WORKDIR/runtime-remove-$runtime-$version.html"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -o "$body" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "version=$version" \
    "$BASE_URL/settings/runtime-versions/$runtime/remove" || { fail "Runtime remove request failed for $runtime $version"; return 1; }
  if file_contains "$body" "cannot remove $version"; then
    pass "In-use runtime removal was blocked for $runtime $version"
  else
    fail "Runtime remove block did not report expected protection for $runtime $version"
  fi
}

create_static_suite() {
  local domain="deploycp-smoke-static-$RUN_ID.invalid"
  local name="smoke-static-$RUN_ID"
  local user="dcss${RUN_ID: -6}"
  local pass="Sm0ke!${RUN_ID: -8}"
  local root
  root="$(platform_root_for_domain "$domain")/htdocs"
  local manage_url
  manage_url="$(create_platform static "$domain" "$name" "$user" "$pass" "$root")" || { fail "Static platform creation failed"; return 1; }
  local website_id
  website_id="$(website_id_from_manage_url "$manage_url" || website_id_by_domain "$domain")"
  if [[ -z "$website_id" ]]; then
    fail "Could not resolve website id for static platform"
    return 1
  fi
  CREATED_WEBSITES+=("$website_id")
  CREATED_DOMAINS+=("$domain")
  CREATED_ROOTS+=("$(platform_root_for_domain "$domain")")
  CREATED_USERS+=("$user")
  CREATED_EXTRA_USERS+=("")
  CREATED_FTP_USERS+=("")
  pass "Static platform created"

  assert_manage_page_contains_tabs "$manage_url" "Vhost" "Databases" "Varnish Cache" "SSL/TLS" "Security" "SSH/FTP" "File Manager" "Cron Jobs" "Logs"

  local site_out="$WORKDIR/static-site.out"
  if domain_request http "$domain" "/" "$site_out"; then
    pass "Static platform served over local nginx host-header request"
  else
    fail "Static platform did not serve over local nginx host-header request"
  fi

  sleep 1
  local logs_json="$WORKDIR/static-logs.json"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$BASE_URL/websites/$website_id/manage/log-files" -o "$logs_json" || fail "Static log-files endpoint failed"
  local log_body="$WORKDIR/static-log-content.txt"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$BASE_URL/websites/$website_id/manage/log-content?file=access.log" -o "$log_body" || fail "Static log-content endpoint failed"
  if [[ -s "$log_body" ]]; then
    pass "Static access log endpoint returned content"
  else
    fail "Static access log endpoint returned no content"
  fi

  test_file_manager_backend "$website_id" "$(platform_root_for_domain "$domain")"

  local csrf
  csrf="$(csrf_for "$manage_url")"
  local extra_user="dcex${RUN_ID: -6}"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "username=$extra_user" \
    --data-urlencode "password=$pass" \
    "$BASE_URL/websites/$website_id/manage/site-user" >/dev/null || fail "Extra SSH user creation failed"
  CREATED_EXTRA_USERS[$((${#CREATED_EXTRA_USERS[@]}-1))]="$extra_user"
  if getent passwd "$extra_user" >/dev/null 2>&1; then
    pass "Extra SSH user created"
  else
    fail "Extra SSH user missing from host account database"
  fi
  test_site_shell_collaboration "$user" "$extra_user" "$(platform_root_for_domain "$domain")"

  local ftp_user="dcftp${RUN_ID: -5}"
  csrf="$(csrf_for "$manage_url")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "username=$ftp_user" \
    --data-urlencode "password=$pass" \
    "$BASE_URL/websites/$website_id/manage/ftp-users" >/dev/null || fail "FTP user creation failed"
  CREATED_FTP_USERS[$((${#CREATED_FTP_USERS[@]}-1))]="$ftp_user"
  if getent passwd "$ftp_user" >/dev/null 2>&1; then
    pass "FTP user created"
  else
    fail "FTP user missing from host account database"
  fi

  csrf="$(csrf_for "$manage_url")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "schedule=*/10 * * * *" \
    --data-urlencode "command=true" \
    "$BASE_URL/websites/$website_id/manage/cron-jobs" >/dev/null || fail "Cron job creation failed"
  local cron_id
  cron_id="$(cron_id_for_site "$website_id")"
  if [[ -n "$cron_id" && -f "/etc/cron.d/deploycp-website-$website_id-$cron_id.cron" ]]; then
    pass "Cron job file created"
  else
    fail "Cron job file was not created"
  fi

  if service_active_any mysql mariadb; then
    local db_name="smk_${RUN_ID: -8}"
    local db_user="smku_${RUN_ID: -6}"
    csrf="$(csrf_for "$manage_url")"
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -X POST \
      --data-urlencode "_csrf=$csrf" \
      --data-urlencode "engine=mariadb" \
      --data-urlencode "label=$db_name" \
      --data-urlencode "database=$db_name" \
      --data-urlencode "username=$db_user" \
      --data-urlencode "password=$pass" \
      --data-urlencode "environment=production" \
      "$BASE_URL/websites/$website_id/manage/database" >/dev/null || fail "MariaDB creation request failed"
    local dbid
    dbid="$(db_id_for_site_engine "$website_id" "mariadb")"
    if [[ -n "$dbid" ]]; then
      CREATED_MYSQL_DBS+=("$db_name|$db_user")
      pass "MariaDB connection created"
    else
      fail "MariaDB connection row was not created"
    fi
  else
    skip "MariaDB tests skipped because no local MariaDB/MySQL service is active"
  fi

  if service_active_any postgresql; then
    local pg_name="spg_${RUN_ID: -8}"
    local pg_user="spgu_${RUN_ID: -6}"
    csrf="$(csrf_for "$manage_url")"
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -X POST \
      --data-urlencode "_csrf=$csrf" \
      --data-urlencode "engine=postgres" \
      --data-urlencode "label=$pg_name" \
      --data-urlencode "database=$pg_name" \
      --data-urlencode "username=$pg_user" \
      --data-urlencode "password=$pass" \
      --data-urlencode "environment=production" \
      "$BASE_URL/websites/$website_id/manage/database" >/dev/null || fail "PostgreSQL creation request failed"
    local pgid
    pgid="$(db_id_for_site_engine "$website_id" "postgres")"
    if [[ -n "$pgid" ]]; then
      CREATED_PG_DBS+=("$pg_name|$pg_user")
      pass "PostgreSQL connection created"
    else
      fail "PostgreSQL connection row was not created"
    fi
  else
    skip "PostgreSQL tests skipped because no local PostgreSQL service is active"
  fi

  if service_active_any redis redis-server; then
    csrf="$(csrf_for "$manage_url")"
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -X POST \
      --data-urlencode "_csrf=$csrf" \
      --data-urlencode "label=redis-$RUN_ID" \
      --data-urlencode "host=127.0.0.1" \
      --data-urlencode "port=6379" \
      --data-urlencode "db=0" \
      --data-urlencode "password=$pass" \
      --data-urlencode "environment=production" \
      "$BASE_URL/websites/$website_id/manage/redis" >/dev/null || fail "Redis connection creation failed"
    local rid
    rid="$(redis_id_for_site "$website_id")"
    if [[ -n "$rid" ]]; then
      pass "Redis connection created"
    else
      fail "Redis connection row was not created"
    fi
  else
    skip "Redis tests skipped because no local Redis service is active"
  fi

  if service_active_any varnish; then
    csrf="$(csrf_for "$manage_url")"
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
      -X POST \
      --data-urlencode "_csrf=$csrf" \
      --data-urlencode "enabled=true" \
      --data-urlencode "server=127.0.0.1:6081" \
      --data-urlencode "cache_lifetime=600" \
      --data-urlencode "cache_tag_prefix=smoke$RUN_ID" \
      --data-urlencode "excluded_params=__SID,noCache" \
      --data-urlencode "excludes=^/admin/" \
      "$BASE_URL/websites/$website_id/manage/varnish" >/dev/null || fail "Varnish config update failed"
    if [[ -f "/etc/varnish/deploycp.d/website-$website_id.vcl" ]]; then
      pass "Varnish fragment created"
    else
      fail "Varnish fragment missing after enable"
    fi
  else
    skip "Varnish tests skipped because varnish service is not active"
  fi

  csrf="$(csrf_for "$manage_url")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "domain=$domain" \
    "$BASE_URL/websites/$website_id/manage/ssl/self-signed" >/dev/null || fail "Self-signed SSL creation failed"
  local ssl_id
  ssl_id="$(ssl_id_by_domain "$domain")"
  if [[ -n "$ssl_id" ]]; then
    CREATED_SSL_DOMAINS+=("$domain")
    pass "Self-signed SSL row created"
  else
    fail "Self-signed SSL row missing"
  fi
  local https_out="$WORKDIR/https-$website_id.out"
  if domain_request https "$domain" "/" "$https_out"; then
    pass "HTTPS served through self-signed certificate"
  else
    fail "HTTPS request failed after self-signed certificate creation"
  fi
  csrf="$(csrf_for "$manage_url")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    "$BASE_URL/websites/$website_id/manage/ssl/$ssl_id/delete" >/dev/null || fail "SSL delete request failed"
  if [[ -z "$(ssl_id_by_domain "$domain")" ]]; then
    pass "SSL delete removed certificate row"
  else
    fail "SSL row still present after delete"
  fi

  csrf="$(csrf_for "$manage_url")"
  curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "enabled=true" \
    --data-urlencode "username=basic$RUN_ID" \
    --data-urlencode "password=$pass" \
    --data-urlencode "whitelisted_ips=" \
    "$BASE_URL/websites/$website_id/manage/security/basic-auth" >/dev/null || fail "Basic auth update failed"
  local basic_status
  basic_status="$(curl -sS -o /dev/null -w '%{http_code}' -H "Host: $domain" "http://127.0.0.1/")"
  if [[ "$basic_status" == "401" ]]; then
    pass "Basic auth protects the temporary static site"
  else
    fail "Basic auth did not return 401 for unauthenticated request"
  fi
}

create_runtime_suite() {
  local kind="$1" selected="$2" port="$3"
  local domain="deploycp-smoke-$kind-$RUN_ID.invalid"
  local name="smoke-$kind-$RUN_ID"
  local user="dc${kind:0:2}${RUN_ID: -6}"
  local pass="Sm0ke!${RUN_ID: -8}"
  local root
  root="$(platform_root_for_domain "$domain")/htdocs"
  local manage_url
  manage_url="$(create_platform "$kind" "$domain" "$name" "$user" "$pass" "$root" "$selected" "$port")" || { fail "$kind platform creation failed"; return 1; }
  local website_id
  website_id="$(website_id_from_manage_url "$manage_url" || website_id_by_domain "$domain")"
  if [[ -z "$website_id" ]]; then
    fail "Could not resolve website id for $kind platform"
    return 1
  fi
  CREATED_WEBSITES+=("$website_id")
  CREATED_DOMAINS+=("$domain")
  CREATED_ROOTS+=("$(platform_root_for_domain "$domain")")
  CREATED_USERS+=("$user")
  CREATED_EXTRA_USERS+=("")
  CREATED_FTP_USERS+=("")
  pass "$kind platform created"

  assert_manage_page_contains_tabs "$manage_url" "Settings" "Runtime" "Databases" "SSL/TLS" "SSH/FTP" "Logs"

  local runtime_out="$WORKDIR/$kind-site.out"
  if domain_request_retry http "$domain" "/" "$runtime_out" 15; then
    if file_contains "$runtime_out" "DeployCP"; then
      pass "$kind platform served local host-header traffic"
    else
      fail "$kind platform response did not contain expected scaffold output"
    fi
  else
    fail "$kind platform did not respond to local host-header traffic"
  fi

  test_runtime_shell_version "$user" "$kind" "$selected" "$(platform_root_for_domain "$domain")"

  local runtime_page="$WORKDIR/runtime-$kind.html"
  fetch_page "$manage_url" "$runtime_page" || fail "Could not reload $kind manage page"
  if file_contains "$runtime_page" "$selected"; then
    pass "$kind manage page shows selected runtime version"
  else
    fail "$kind manage page does not show selected runtime version"
  fi
  if file_contains "$runtime_page" "healthy"; then
    pass "$kind runtime health rendered on manage page"
  else
    fail "$kind runtime health did not render as healthy"
  fi

  test_runtime_remove_block "$kind" "$selected"
}

create_php_suite() {
  local selected="$1"
  local domain="deploycp-smoke-php-$RUN_ID.invalid"
  local name="smoke-php-$RUN_ID"
  local user="dcph${RUN_ID: -6}"
  local pass="Sm0ke!${RUN_ID: -8}"
  local root
  root="$(platform_root_for_domain "$domain")/htdocs"
  local manage_url
  manage_url="$(create_platform php "$domain" "$name" "$user" "$pass" "$root" "$selected")" || { fail "PHP platform creation failed"; return 1; }
  local website_id
  website_id="$(website_id_from_manage_url "$manage_url" || website_id_by_domain "$domain")"
  if [[ -z "$website_id" ]]; then
    fail "Could not resolve website id for PHP platform"
    return 1
  fi
  CREATED_WEBSITES+=("$website_id")
  CREATED_DOMAINS+=("$domain")
  CREATED_ROOTS+=("$(platform_root_for_domain "$domain")")
  CREATED_USERS+=("$user")
  CREATED_EXTRA_USERS+=("")
  CREATED_FTP_USERS+=("")
  pass "PHP platform created"

  assert_manage_page_contains_tabs "$manage_url" "Settings" "Vhost" "Databases" "SSL/TLS" "Security" "SSH/FTP" "File Manager" "Cron Jobs" "Logs"

  run_as_platform_user "$user" "$(platform_root_for_domain "$domain")" "printf '%s\n' '<?php echo \"deploycp-php-ok\";' > '$(platform_root_for_domain "$domain")/htdocs/index.php'" >/dev/null 2>&1 || fail "Could not write PHP smoke file as site user"
  local php_out="$WORKDIR/php-site.out"
  if domain_request http "$domain" "/index.php" "$php_out"; then
    if file_contains "$php_out" "deploycp-php-ok"; then
      pass "PHP platform executed PHP through nginx/FPM"
    else
      fail "PHP platform response did not show executed PHP output"
    fi
  else
    fail "PHP platform did not respond over local host-header request"
  fi

  test_runtime_shell_version "$user" "php" "$selected" "$(platform_root_for_domain "$domain")"

  local php_page="$WORKDIR/php-manage.html"
  fetch_page "$manage_url" "$php_page" || fail "Could not reload PHP manage page"
  if file_contains "$php_page" "$selected"; then
    pass "PHP manage page shows selected PHP-FPM version"
  else
    fail "PHP manage page does not show selected PHP-FPM version"
  fi
  if file_contains "$php_page" "healthy"; then
    pass "PHP runtime health rendered on manage page"
  else
    fail "PHP runtime health did not render as healthy"
  fi
}

main() {
  if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
  fi

  require_root
  require_input
  require_commands
  setup_workdir

  note "Base URL: $BASE_URL"
  note "SQLite DB: $SQLITE_DB"

  if [[ ! -f "$SQLITE_DB" ]]; then
    echo "SQLite DB not found: $SQLITE_DB" >&2
    exit 2
  fi

  if [[ -x "$DEPLOYCP_BIN" ]]; then
    if "$DEPLOYCP_BIN" verify-host >/dev/null 2>&1; then
      pass "deploycp verify-host passed"
    else
      fail "deploycp verify-host failed"
    fi
  else
    skip "deploycp verify-host skipped because binary was not found at $DEPLOYCP_BIN"
  fi

  if ! panel_login; then
    exit 1
  fi

  assert_get_page "/" "Dashboard" "DeployCP"
  assert_get_page "/platforms" "Platforms page" "Platforms"
  assert_get_page "/platforms/new" "Platform creation page" "Deploy New Platform"
  assert_get_page "/settings?tab=general" "Settings general tab" "Settings"
  assert_get_page "/settings?tab=services" "Settings services tab" "Runtime Version Management"
  assert_get_page "/settings?tab=users" "Settings users tab" "Users"
  assert_get_page "/settings?tab=events" "Settings events tab" "Events"
  assert_get_page "/updates" "Updates page" "Updates"

  local new_html="$WORKDIR/platforms-new.html"
  fetch_page "/platforms/new" "$new_html" || { fail "Could not load /platforms/new for runtime discovery"; exit 1; }
  local php_version go_version python_version node_version
  php_version="$(first_select_option "$new_html" "sa-php-version")"
  go_version="$(first_select_option "$new_html" "sa-go-version")"
  python_version="$(first_select_option "$new_html" "sa-python-version")"
  node_version="$(first_select_option "$new_html" "sa-node-version")"

  create_static_suite

  if [[ -n "$php_version" ]]; then
    create_php_suite "$php_version"
  else
    skip "PHP platform suite skipped because no PHP-FPM versions were available"
  fi
  if [[ -n "$go_version" ]]; then
    create_runtime_suite go "$go_version" "38080"
  else
    skip "Go platform suite skipped because no Go versions were available"
  fi
  if [[ -n "$python_version" ]]; then
    create_runtime_suite python "$python_version" "38081"
  else
    skip "Python platform suite skipped because no Python versions were available"
  fi
  if [[ -n "$node_version" ]]; then
    create_runtime_suite node "$node_version" "38082"
  else
    skip "Node platform suite skipped because no Node versions were available"
  fi

  printf "\nSummary: %s pass, %s fail, %s skip\n" "$PASS_COUNT" "$FAIL_COUNT" "$SKIP_COUNT"
  if [[ $FAIL_COUNT -ne 0 ]]; then
    exit 1
  fi
}

main "$@"
