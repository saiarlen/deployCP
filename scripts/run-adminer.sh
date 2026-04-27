#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8081}"
PHP_BIN="$(command -v php || true)"
ADMINER_FILE=""

for candidate in \
  /usr/share/adminer/index.php \
  /usr/share/adminer/adminer.php \
  /usr/share/php/adminer/adminer.php
do
  if [[ -f "$candidate" ]]; then
    ADMINER_FILE="$candidate"
    break
  fi
done

if [[ -z "$PHP_BIN" ]]; then
  echo "PHP binary not found; install PHP to run the local Adminer helper." >&2
  exit 1
fi

if [[ -z "$ADMINER_FILE" ]]; then
  echo "Adminer PHP file not found; install Adminer locally (for example distro package adminer) and then rerun." >&2
  exit 1
fi

HELPER_ROOT="/home/deploycp/core/storage/generated/adminer-helper"
mkdir -p "$HELPER_ROOT"
cp "$ADMINER_FILE" "$HELPER_ROOT/index.php"

"$PHP_BIN" -S "127.0.0.1:${PORT}" -t "$HELPER_ROOT"
