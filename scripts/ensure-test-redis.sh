#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${1:-.env}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE"
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

if [ -z "${REDIS_TEST_URL:-}" ]; then
  echo "REDIS_TEST_URL is required so Redis-backed tests cannot be silently skipped."
  echo "Generate a worktree env again or add the dedicated test Redis settings from .env.example."
  exit 1
fi

rest="${REDIS_TEST_URL#*://}"
authority="${rest%%/*}"
hostport="${authority##*@}"
host="${hostport%%:*}"
port="${hostport##*:}"
if [ "$host" = "$port" ]; then
  port=6379
fi

case "$host" in
  localhost|127.0.0.1) ;;
  *)
    echo "REDIS_TEST_URL points to non-local host '$host'; refusing to manage or flush it."
    exit 1
    ;;
esac

REDIS_TEST_PORT="${REDIS_TEST_PORT:-$port}"
if [ "$REDIS_TEST_PORT" != "$port" ]; then
  echo "REDIS_TEST_PORT ($REDIS_TEST_PORT) does not match REDIS_TEST_URL port ($port)."
  exit 1
fi
export REDIS_TEST_PORT

if command -v redis-cli >/dev/null 2>&1 && redis-cli -u "$REDIS_TEST_URL" ping 2>/dev/null | grep -q '^PONG$'; then
  echo "✓ Dedicated test Redis ready ($host:$port)"
  exit 0
fi

echo "==> Starting dedicated Redis 7 test service on $host:$port..."
docker compose up -d redis-test
for _ in $(seq 1 30); do
  if docker compose exec -T redis-test redis-cli ping 2>/dev/null | grep -q '^PONG$'; then
    echo "✓ Dedicated test Redis ready ($host:$port)"
    exit 0
  fi
  sleep 1
done

echo "Dedicated test Redis did not become ready on $host:$port."
exit 1
