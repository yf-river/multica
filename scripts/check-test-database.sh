#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ENV_FILE="${1:?需要明确测试环境配置文件}"
if [ ! -f "$ENV_FILE" ]; then
  echo "测试环境配置文件不存在" >&2
  exit 2
fi
set -a
# 与 ensure-postgres.sh 读取同一个文件，在任何容器准备动作前核对真正生效的目标。
. "$ENV_FILE"
set +a
python3 "$SCRIPT_DIR/check-test-database.py"
