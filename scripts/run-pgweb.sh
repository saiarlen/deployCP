#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8082}"

if ! command -v pgweb >/dev/null 2>&1; then
  echo "pgweb binary not found; install pgweb locally or configure POSTGRES_GUI_URL to another loopback-only Postgres UI" >&2
  exit 1
fi

pgweb --listen=127.0.0.1 --port="${PORT}" --sessions &
echo "pgweb started at http://127.0.0.1:${PORT}"
