#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! grep -Fq 'require_fresh_check_service "Backend" "$PORT" "/health"' scripts/check.sh; then
  echo "check.sh must reject an already-running backend before verification"
  exit 1
fi

if ! grep -Fq 'require_fresh_check_service "Frontend" "$FRONTEND_PORT" "/"' scripts/check.sh; then
  echo "check.sh must reject an already-running frontend before verification"
  exit 1
fi

if ! grep -Fq 'CHECK_ALLOW_EXISTING_SERVICES' scripts/check.sh; then
  echo "check.sh must make service reuse an explicit opt-in"
  exit 1
fi

echo "check service preflight contract ok"
