#!/usr/bin/env bash
set -euo pipefail

if [ ! -f .env ]; then
  cp .env.example .env
  echo "Created .env from .env.example"
fi

mkdir -p storage/db storage/logs storage/sites tmp bin

go mod tidy

echo "Setup complete. Run: make dev"
