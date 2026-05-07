#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8081}"
PHP_BIN="$(command -v php || true)"
ADMINER_FILE=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CORE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

for candidate in \
  "${CORE_DIR}/assets/adminer/adminer.php" \
  "$(pwd)/assets/adminer/adminer.php"
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
  echo "Bundled Adminer PHP file not found under assets/adminer/adminer.php." >&2
  exit 1
fi

HELPER_ROOT="${CORE_DIR}/storage/generated/adminer-helper"
mkdir -p "$HELPER_ROOT"
cp "$ADMINER_FILE" "$HELPER_ROOT/index.php"

"$PHP_BIN" -S "127.0.0.1:${PORT}" -t "$HELPER_ROOT"
