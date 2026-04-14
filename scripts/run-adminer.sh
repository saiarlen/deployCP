#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8081}"

docker run --rm -d \
  --name deploycp-adminer \
  -p "${PORT}:8080" \
  adminer:4

echo "Adminer started at http://127.0.0.1:${PORT}"
