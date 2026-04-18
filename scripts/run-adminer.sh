#!/usr/bin/env bash
set -euo pipefail

PORT="${1:-8081}"

echo "Adminer is not bundled with DeployCP." >&2
echo "Provision Adminer separately behind loopback or a trusted private network, then set ADMINER_URL=http://127.0.0.1:${PORT} in /home/deploycp/core/.env." >&2
exit 1
