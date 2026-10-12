#!/usr/bin/env python3
"""在指定的隔离环境中执行反馈表删除迁移。"""

import argparse
import json
import os
import shutil
import subprocess
import tempfile
import urllib.request
from pathlib import Path


def read_json(path: Path) -> dict:
    return json.loads(path.read_text())


def get_json(url: str) -> dict:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(url, timeout=5) as response:
        return json.load(response)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, required=True)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--environment", choices=("int", "prod"), required=True)
    args = parser.parse_args()

    release = args.release.resolve()
    config = read_json(args.config.resolve())
    server_env = config["server"]
    if server_env.get("MULTICA_ENV_ID") != args.environment:
        raise SystemExit("运行配置的环境标识与目标不一致")

    port = server_env["PORT"]
    running = get_json(f"http://127.0.0.1:{port}/api/config")
    if running.get("environment_id") != args.environment:
        raise SystemExit("当前 API 的环境标识与目标不一致")

    names = ("497_cooking_favorite.up.sql", "498_cooking_favorite_unique.up.sql", "499_remove_feedback.up.sql")
    migrations = [release / "server/migrations" / name for name in names]
    migrate = release / "server/bin/migrate"
    if not all(path.is_file() for path in migrations) or not migrate.is_file():
        raise SystemExit("发布包缺少反馈删除迁移或 migrate 程序")

    with tempfile.TemporaryDirectory(prefix=f"multica-remove-feedback-{args.environment}-") as directory:
        migration_dir = Path(directory) / "migrations"
        migration_dir.mkdir()
        # 同一发布包包含烹饪模块；生产尚未建立收藏表，已应用的迁移由台账跳过。
        for migration in migrations:
            shutil.copy2(migration, migration_dir / migration.name)
        subprocess.run(
            [str(migrate), "up"],
            cwd=directory,
            env={**os.environ, **server_env},
            check=True,
        )

    print(f"{args.environment} feedback migration applied")


if __name__ == "__main__":
    main()
