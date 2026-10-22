#!/usr/bin/env python3
"""在 make test 的建库和迁移之前核对测试目标；不建立数据库连接。"""
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlsplit, parse_qs


def identity(raw):
    u = urlsplit(raw)
    if u.scheme not in ('postgres', 'postgresql') or not u.hostname or not u.path.strip('/'):
        raise ValueError('测试连接必须明确 PostgreSQL 主机、端口和数据库')
    if set(parse_qs(u.query)) & {'host', 'hostaddr', 'port', 'dbname', 'service', 'servicefile'}:
        raise ValueError('测试连接不允许通过查询参数覆盖目标地址')
    host = 'loopback' if u.hostname in ('localhost', '127.0.0.1', '::1') else u.hostname.lower()
    return host, u.port or 5432, u.path.lstrip('/')


def validate(target, declared, protected, paused_ports):
    actual = identity(target)
    if not declared or actual != identity(declared):
        raise ValueError('请显式设置 MULTICA_TEST_DATABASE_URL，并与测试配置中的 DATABASE_URL 指向同一隔离目标')
    if actual[:2] in {identity(x)[:2] for x in protected}:
        raise ValueError('测试禁止使用固定生产或联调所在的 PostgreSQL 实例')
    if actual[0] == 'loopback' and actual[1] in paused_ports:
        raise ValueError('测试禁止连接或启动冻结的 PostgreSQL 事故实例')
    return actual


def main():
    root = Path(__file__).resolve().parent.parent
    protected = []
    for directory in {root / '.run/env', Path('/data/ida/multica/.run/env')}:
        for env in ('prod', 'int'):
            p = directory / (env + '-services.json')
            if p.exists():
                protected.append(json.loads(p.read_text())['server']['DATABASE_URL'])
    paused = set()
    target = identity(os.environ.get('DATABASE_URL', ''))
    if target[0] == 'loopback':
        try:
            result = subprocess.run(['docker', 'ps', '-q'], check=True, capture_output=True, text=True)
            ids = result.stdout.split()
            if ids:
                result = subprocess.run(['docker', 'inspect', *ids], check=True, capture_output=True, text=True)
                for c in json.loads(result.stdout):
                    if c['State'].get('Paused'):
                        for binding in c['NetworkSettings']['Ports'].get('5432/tcp') or []:
                            paused.add(int(binding['HostPort']))
        except FileNotFoundError:
            pass  # 原生 PostgreSQL 主机没有 Docker，仍核对显式目标和固定配置。
        except subprocess.CalledProcessError:
            raise ValueError('无法核对本机容器状态，停止测试准备') from None
    host, port, db = validate(os.environ.get('DATABASE_URL', ''), os.environ.get('MULTICA_TEST_DATABASE_URL', ''), protected, paused)
    print(f'测试目标已核对：{host}:{port}/{db}')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError) as error:
        print('测试数据库检查失败：' + str(error), file=sys.stderr)
        sys.exit(2)
