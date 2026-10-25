#!/usr/bin/env python3
"""测试 Agent 创建参数：读取生产维护的测试默认配置。"""
import argparse
import json
import os
from pathlib import Path

FLASH = 'deepseek-v4.1-flash'
TEST_ENVS = ('int', 'verify-1', 'verify-2', 'verify-3')


def selection(context=None, environ=None):
    env = os.environ if environ is None else environ
    root = Path(env.get('MULTICA_RELEASE_ROOT', Path(__file__).resolve().parent.parent))
    source = root / '.run/test-agent-policy.json'
    if not source.is_file():
        raise RuntimeError('尚未同步生产测试模型设置，停止创建测试 Agent')
    result = json.loads(source.read_text())['policy']
    if result.get('provider') not in ('codebuddy','codex') or not result.get('model'):
        raise RuntimeError('生产测试模型设置无效')
    return result


def agent_fields(runtimes, daemon_id, context=None, environ=None):
    chosen = selection(context, environ)
    matches = [x for x in runtimes if x.get('provider') == chosen['provider'] and x.get('status') == 'online' and x.get('daemon_id') == daemon_id and not x.get('profile_id')]
    if len(matches) != 1:
        raise RuntimeError('无法唯一确定测试环境中同 provider 的在线 runtime；不切换模型或账号')
    return dict(runtime_id=matches[0]['id'], **{k:chosen[k] for k in ('model','thinking_level','service_tier')})


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtimes-file', type=Path, required=True)
    parser.add_argument('--daemon-id', required=True)
    args = parser.parse_args()
    print(json.dumps(agent_fields(json.loads(args.runtimes_file.read_text()), args.daemon_id), ensure_ascii=False))
