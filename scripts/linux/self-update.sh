#!/usr/bin/env bash
set -euo pipefail

STATUS_PATH="${1:-}"
LOG_PATH="${2:-}"
CURRENT_VERSION="${3:-}"
TARGET_VERSION="${4:-}"
REPO="${5:-saiarlen/deployCP}"
CORE_DIR="${6:-/home/deploycp/core}"
UNIT_NAME="${7:-deploycp-self-update}"

if [[ -z "$STATUS_PATH" || -z "$LOG_PATH" || -z "$CORE_DIR" ]]; then
  echo "usage: self-update.sh <status-path> <log-path> <current-version> <target-version> <repo> <core-dir> [unit-name]" >&2
  exit 1
fi

mkdir -p "$(dirname "$STATUS_PATH")" "$(dirname "$LOG_PATH")"

timestamp() {
  date -u +"%Y-%m-%dT%H:%M:%SZ"
}

write_status() {
  local state="$1"
  local message="$2"
  cat >"$STATUS_PATH" <<EOF
STATE=${state}
MESSAGE=${message}
CURRENT_VERSION=${CURRENT_VERSION}
TARGET_VERSION=${TARGET_VERSION}
STARTED_AT=${STARTED_AT}
FINISHED_AT=${FINISHED_AT:-}
LOG_PATH=${LOG_PATH}
UNIT_NAME=${UNIT_NAME}
LATEST_VERSION=${TARGET_VERSION}
EOF
}

log() {
  printf '[%s] %s\n' "$(timestamp)" "$*" >>"$LOG_PATH"
}

STARTED_AT="$(timestamp)"
FINISHED_AT=""
: >"$LOG_PATH"

write_status "running" "Preparing update"
log "Starting DeployCP self-update"
log "Current version: ${CURRENT_VERSION:-unknown}"
log "Target version: ${TARGET_VERSION:-latest}"
log "Repository: ${REPO}"

export DEPLOYCP_REPO="$REPO"
if [[ -n "$TARGET_VERSION" ]]; then
  export DEPLOYCP_VERSION="$TARGET_VERSION"
fi

if /bin/bash "${CORE_DIR}/scripts/linux/install-remote.sh" --update >>"$LOG_PATH" 2>&1; then
  FINISHED_AT="$(timestamp)"
  write_status "success" "Update completed"
  log "DeployCP update completed successfully"
  exit 0
else
  rc=$?
fi
FINISHED_AT="$(timestamp)"
write_status "failed" "Update failed"
log "DeployCP update failed with exit code ${rc}"
exit "$rc"
