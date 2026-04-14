#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8082}"

if command -v pgweb >/dev/null 2>&1; then
  pgweb --listen=0.0.0.0 --port="${PORT}" --sessions &
  echo "pgweb started at http://127.0.0.1:${PORT}"
  exit 0
fi

docker run --rm -d \
  --name deploycp-pgweb \
  -p "${PORT}:8081" \
  sosedoff/pgweb

echo "pgweb (docker) started at http://127.0.0.1:${PORT}"
