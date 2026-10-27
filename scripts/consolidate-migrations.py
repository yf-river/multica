#!/usr/bin/env python3
"""把历史迁移收敛成一个新的 `000_initial_schema` baseline。

只导出结构（pg_dump --schema-only），不读也不写业务数据：

* 新 baseline 由当前生产库的结构生成；
* 其余迁移文件用 `git mv` 收进 `server/migrations/archive/`，历史仍在 git 里；
* 已有环境（prod/int）的 `schema_migrations` 里 `000_initial_schema` 本来就已记录，
  归档文件不参与文件遍历，因此**不需要**修改任何已有环境的台账。

在临时库验证：

    python3 scripts/consolidate-migrations.py --source . \
        --database-url postgres://.../multica_scratch --dry-run

真正执行（会改仓库文件）：

    python3 scripts/consolidate-migrations.py --source . --database-url <生产 DSN>
"""
import argparse
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

BASELINE = '000_initial_schema'
ARCHIVE = 'archive'


def dump_schema(database_url, container='', database=''):
    command = (['docker', 'exec', container, 'pg_dump', '-U', 'multica', '--schema-only', '--no-owner', '--no-privileges', '--exclude-table=public.schema_migrations', database]
               if container else ['pg_dump', database_url, '--schema-only', '--no-owner', '--no-privileges', '--exclude-table=public.schema_migrations'])
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit('pg_dump 失败：' + result.stderr[-800:])
    return result.stdout


def baseline_body(dump):
    # pg_dump 的会话设置与注释对全新安装没有意义，保留可执行的 DDL 即可。
    lines = [line for line in dump.splitlines()
             if not re.match(r'^(SET |SELECT pg_catalog\.set_config|--|\\|$)', line)]
    body = '\n'.join(lines).strip('\n')
    if 'CREATE TABLE' not in body:
        raise SystemExit('dump 里没有 CREATE TABLE，拒绝用空结构覆盖 baseline')
    return body + '\n'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True, help='仓库根目录')
    parser.add_argument('--database-url', required=True, help='用于导出结构的数据库（通常用生产库）')
    parser.add_argument('--dry-run', action='store_true', help='只报告将要归档的文件，不改动仓库')
    parser.add_argument('--container', default='', help='数据库在容器里时指定容器名，用 docker exec 导出')
    parser.add_argument('--database', default='', help='配合 --container 使用的库名')
    args = parser.parse_args()

    source = Path(args.source).resolve()
    migrations = source / 'server' / 'migrations'
    if not migrations.is_dir():
        raise SystemExit('找不到 ' + str(migrations))
    body = baseline_body(dump_schema(args.database_url, args.container, args.database))
    files = sorted(p for p in migrations.glob('*.sql') if p.is_file() and not p.name.startswith(BASELINE + '.'))
    print('新 baseline：%d 行；将归档 %d 个文件到 server/migrations/%s/' % (len(body.splitlines()), len(files), ARCHIVE))
    if args.dry_run:
        for path in files[:5]:
            print('  would move', path.name)
        if len(files) > 5:
            print('  ... 共 %d 个' % len(files))
        return 0

    archive = migrations / ARCHIVE
    archive.mkdir(exist_ok=True)
    for path in files:
        target = archive / path.name
        if target.exists():
            raise SystemExit('归档目标已存在：' + str(target))
        subprocess.run(['git', 'mv', str(path), str(target)], cwd=source, check=True)
    (migrations / (BASELINE + '.up.sql')).write_text(body)
    print('已写入新 baseline，并归档 %d 个文件' % len(files))
    return 0


if __name__ == '__main__':
    sys.exit(main())
