#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
GUARD_SCRIPT="$SCRIPT_DIR/go-test-with-agent-cli-guard.sh"

usage() {
  echo "usage: $0 [--race]" >&2
}

go_test_args=(test)
case "$#" in
  0) ;;
  1)
    if [ "$1" != "--race" ]; then
      usage
      exit 2
    fi
    go_test_args+=(-race)
    ;;
  *)
    usage
    exit 2
    ;;
esac

# Claude Code 在 root/sudo 下拒绝 bypassPermissions，与之相关的子进程取消测试会直接失败。
# 测试本身不需要真实的权限提升，所以在 root 下显式声明为沙箱运行——只影响测试进程，
# 不改变 daemon 运行时的行为。
if [ "$(id -u)" = "0" ] && [ "${IS_SANDBOX:-}" = "" ]; then
  export IS_SANDBOX=1
  echo "test-go: running as root; setting IS_SANDBOX=1 for Claude-backed agent tests" >&2
fi

cd "$REPO_ROOT/server"
packages=$(go list ./...)
regular_packages=()
for package in $packages; do
  case "$package" in
    */pkg/agent|*/pkg/agent/*) ;;
    *) regular_packages+=("$package") ;;
  esac
done

# Database-backed packages share the target test database. Run packages in
# sequence so package-level TestMain fixtures cannot collide with each other.
"$GUARD_SCRIPT" -- go "${go_test_args[@]}" -p 1 "${regular_packages[@]}"
# Subprocess-backed agent tests have hard deadlines. Limit both package and
# within-package parallelism so race builds do not starve their parent loops.
"$GUARD_SCRIPT" -- go "${go_test_args[@]}" -p 2 -parallel 2 ./pkg/agent/...
