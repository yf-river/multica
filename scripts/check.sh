#!/usr/bin/env bash
set -euo pipefail

# ==========================================================================
# Full verification pipeline: typecheck → unit tests → Go tests → E2E
# Usage: bash scripts/check.sh
# ==========================================================================

ENV_FILE="${ENV_FILE:-.env}"
if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

# shellcheck disable=SC1091
. scripts/local-env.sh

BACKEND_PID=""
FRONTEND_PID=""
STARTED_BACKEND=false
STARTED_FRONTEND=false
EXIT_CODE=0
NEXT_ENV_FILE="apps/web/next-env.d.ts"
NEXT_ENV_BACKUP=""

if [ -f "$NEXT_ENV_FILE" ]; then
  NEXT_ENV_BACKUP="$(mktemp)"
  cp "$NEXT_ENV_FILE" "$NEXT_ENV_BACKUP"
fi

start_service() {
  local name=$1 log_file=$2
  shift 2
  if command -v setsid > /dev/null 2>&1; then
    setsid bash -lc "$*" > "$log_file" 2>&1 &
  else
    bash -lc "$*" > "$log_file" 2>&1 &
  fi
  local pid=$!
  echo "    ${name} PID $pid"
  printf '%s\n' "$pid"
}

stop_service_tree() {
  local pid=$1 name=$2
  if [ -z "$pid" ]; then
    return
  fi
  if kill -0 "$pid" 2>/dev/null; then
    kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 20); do
      if ! kill -0 "$pid" 2>/dev/null; then
        break
      fi
      sleep 0.2
    done
    kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  echo "    Stopped $name (PID $pid)"
}

restore_next_env() {
  if [ -n "$NEXT_ENV_BACKUP" ] && [ -f "$NEXT_ENV_BACKUP" ]; then
    if [ -f "$NEXT_ENV_FILE" ] && ! cmp -s "$NEXT_ENV_FILE" "$NEXT_ENV_BACKUP"; then
      cp "$NEXT_ENV_BACKUP" "$NEXT_ENV_FILE"
      echo "    Restored $NEXT_ENV_FILE"
    fi
    rm -f "$NEXT_ENV_BACKUP"
  fi
}

# --------------------------------------------------------------------------
# Cleanup: kill only services this script started
# --------------------------------------------------------------------------
cleanup() {
  echo ""
  if [ "$STARTED_BACKEND" = true ] && [ -n "$BACKEND_PID" ]; then
    stop_service_tree "$BACKEND_PID" "backend"
  fi
  if [ "$STARTED_FRONTEND" = true ] && [ -n "$FRONTEND_PID" ]; then
    stop_service_tree "$FRONTEND_PID" "frontend"
  fi
  restore_next_env
  echo ""
  if [ "$EXIT_CODE" -eq 0 ]; then
    echo "✓ All checks passed."
  else
    echo "✗ Checks FAILED."
  fi
  exit "$EXIT_CODE"
}
trap cleanup EXIT

# --------------------------------------------------------------------------
# Utility: wait until a port responds
# --------------------------------------------------------------------------
wait_for_port() {
  local port=$1 name=$2 max_wait=${3:-60} path=${4:-/}
  local elapsed=0
  echo "    Waiting for $name on :$port..."
  while ! curl -sf "http://localhost:${port}${path}" > /dev/null 2>&1; do
    sleep 1
    elapsed=$((elapsed + 1))
    if [ "$elapsed" -ge "$max_wait" ]; then
      echo "    ERROR: $name did not start within ${max_wait}s"
      EXIT_CODE=1
      exit 1
    fi
  done
  echo "    $name ready (${elapsed}s)"
}

# --------------------------------------------------------------------------
# Step 0: Ensure DB
# --------------------------------------------------------------------------
echo "==> Using env file: $ENV_FILE"
echo "==> Checking PostgreSQL..."
bash scripts/ensure-postgres.sh "$ENV_FILE"

# --------------------------------------------------------------------------
# Step 1: TypeScript typecheck
# --------------------------------------------------------------------------
echo ""
echo "==> [1/5] TypeScript typecheck..."
pnpm typecheck || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 2: TypeScript unit tests (Vitest)
# --------------------------------------------------------------------------
echo ""
echo "==> [2/5] TypeScript unit tests..."
pnpm test || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 3: Go tests
# --------------------------------------------------------------------------
echo ""
echo "==> [3/5] Go tests..."
echo "==> Running database migrations..."
(cd server && go run ./cmd/migrate up) || { EXIT_CODE=1; exit 1; }
echo "==> Checking dedicated Redis test service..."
bash scripts/ensure-test-redis.sh "$ENV_FILE" || { EXIT_CODE=1; exit 1; }
(cd server && go test ./...) || { EXIT_CODE=1; exit 1; }

# --------------------------------------------------------------------------
# Step 4: Start services for E2E (only if not already running)
# --------------------------------------------------------------------------
echo ""
echo "==> [4/5] Starting services for E2E..."

if curl -sf "http://localhost:${PORT}/health" > /dev/null 2>&1; then
  echo "    Backend already running on :$PORT"
else
  echo "    Starting backend..."
  BACKEND_PID="$(start_service "backend" "/tmp/multica-check-backend.log" "cd server && go run ./cmd/server" | tail -n 1)"
  STARTED_BACKEND=true
  wait_for_port "$PORT" "Backend" 90 "/health"
fi

if curl -sf "http://localhost:${FRONTEND_PORT}" > /dev/null 2>&1; then
  echo "    Frontend already running on :$FRONTEND_PORT"
else
  echo "    Starting frontend..."
  FRONTEND_PID="$(start_service "frontend" "/tmp/multica-check-frontend.log" "pnpm dev:web" | tail -n 1)"
  STARTED_FRONTEND=true
  wait_for_port "$FRONTEND_PORT" "Frontend" 120 "/"
fi

# --------------------------------------------------------------------------
# Step 5: E2E tests (Playwright)
# --------------------------------------------------------------------------
echo ""
echo "==> [5/5] E2E tests (Playwright)..."
pnpm exec playwright test || { EXIT_CODE=1; exit 1; }
