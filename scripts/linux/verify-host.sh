#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
BIN_NAME="${BIN_NAME:-deploycp}"

exec "${CORE_DIR}/bin/${BIN_NAME}" verify-host
