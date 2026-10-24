#!/usr/bin/env python3
"""测试 Agent 创建参数：人工初始化默认 Flash，模型执行内严格继承本轮已解析配置。"""
import argparse
import json
import os
from pathlib import Path

FLASH = 'deepseek-v4.1-flash'
TEST_ENVS = ('int', 'verify-1', 'verify-2', 'verify-3')


def selection(context=None, environ=None):
    env = os.environ if environ is None else environ
    if context is None:
        if env.get('MULTICA_TASK_ID'):
            path = Path(env.get('TMPDIR', '/nonexistent')) / 'multica-execution-model.json'
            if not path.is_file():
                raise RuntimeError('本轮缺少可信执行配置，停止创建测试 Agent；请保留现场并人工接入')
            context = json.loads(path.read_text())
        else:
            return dict(provider='codebuddy', model=FLASH, thinking_level='', service_tier='')
    if not context.get('resolved') or context.get('task_id') != env.get('MULTICA_TASK_ID') or not context.get('model'):
        raise RuntimeError('本轮模型配置未解析或执行身份不匹配，停止创建测试 Agent')
    result = {key: context.get(key, '') for key in ('provider', 'model', 'thinking_level', 'service_tier')}
    if not result['provider']:
        raise RuntimeError('本轮 runtime 未确定，停止创建测试 Agent')
    if result['provider'] == 'codex' and (not result['thinking_level'] or not result['service_tier']):
        raise RuntimeError('Codex 推理强度或速度未显式确定，停止创建测试 Agent')
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
