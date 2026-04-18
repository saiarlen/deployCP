#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-deploycp}"
APP_HOME="${APP_HOME:-/home/${APP_USER}}"
CORE_DIR="${CORE_DIR:-${APP_HOME}/core}"
SERVICE_NAME="${SERVICE_NAME:-deploycp}"
BIN_NAME="${BIN_NAME:-deploycp}"
VARNISH_MAIN_VCL="${VARNISH_MAIN_VCL:-/etc/varnish/default.vcl}"
VARNISH_INCLUDE_VCL="${VARNISH_INCLUDE_VCL:-/etc/varnish/deploycp.d/deploycp.vcl}"
VARNISH_CONFIG_DIR="${VARNISH_CONFIG_DIR:-/etc/varnish/deploycp.d}"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

if [[ -x "${CORE_DIR}/bin/${BIN_NAME}" ]]; then
  "${CORE_DIR}/bin/${BIN_NAME}" teardown-managed || true
fi

systemctl stop "${SERVICE_NAME}" || true
systemctl disable "${SERVICE_NAME}" || true
rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
systemctl daemon-reload

if [[ -f "${VARNISH_MAIN_VCL}.deploycp.bak" ]]; then
  cp "${VARNISH_MAIN_VCL}.deploycp.bak" "${VARNISH_MAIN_VCL}"
  rm -f "${VARNISH_MAIN_VCL}.deploycp.bak"
fi
rm -f "$VARNISH_INCLUDE_VCL"
rm -rf "$VARNISH_CONFIG_DIR"
rm -f /etc/fail2ban/jail.d/deploycp.local
rm -f /etc/logrotate.d/deploycp
rm -f /etc/cron.d/deploycp-backup

if id -u "$APP_USER" >/dev/null 2>&1; then
  userdel -r "$APP_USER" || true
fi

rm -rf "$APP_HOME"
find /etc/cron.d -maxdepth 1 -type f -name 'deploycp-website-*.cron' -delete || true
echo "DeployCP removed"
