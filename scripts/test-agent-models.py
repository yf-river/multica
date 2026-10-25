#!/usr/bin/env python3
"""按槽位锁维护测试模型，避免与候选部署竞争。"""
import argparse
import importlib.util
import os
from pathlib import Path
spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def run(environment, apply=False, policy_file=None, scope_file=None):
    if environment not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('禁止维护生产 Agent 模型')
    with r.lock(r.candidate_lock(environment),wait=False):
        meta=r.read(r.meta_path(environment));cfg=r.read(r.config_path(environment))
        r.check_process_ownership(environment,meta)
        r.check_live_environment(environment,meta,cfg)
        # 1. Targeted maintenance retains both deployment locks and verifies the live database consumer.
        if scope_file:
            if not apply or policy_file:raise RuntimeError('定向维护必须 apply 且从控制真源读取策略')
            scope=r.read(scope_file);current=r.candidate(env=environment)
            if (scope.get('environment')!=environment or current.get('status')!='verifying'
                    or scope.get('candidate_id')!=current.get('id')
                    or scope.get('delivery_id')!=current.get('delivery_id')
                    or scope.get('source_commit')!=current.get('source_commit',current.get('approved_source',current.get('commit')))
                    or current.get('commit')!=meta.get('commit') or current.get('release_id')!=meta.get('release_id')
                    or not scope.get('execution_id') or scope['execution_id']!=os.environ.get('MULTICA_TASK_ID')):
                raise RuntimeError('定向维护候选或执行归属无效')
            if r.process_env(meta['pids']['server']).get('DATABASE_URL')!=cfg['server']['DATABASE_URL']:
                raise RuntimeError('实际数据库消费者与安装配置不符')
            with r.lock(environment,wait=False):
                return r.run(['node',str(Path(__file__).with_suffix('.mjs')),environment,'target','-',str(scope_file.resolve())],cwd=r.ROOT,env={**os.environ,'MULTICA_RELEASE_ROOT':str(r.ROOT)})
        candidate=r.candidate_path(environment)
        if apply and candidate.exists() and r.read(candidate).get('status') not in ('released','failed','succeeded'):
            raise RuntimeError('当前候选未释放，空闲后再维护')
        return r.run(['node',str(Path(__file__).with_suffix('.mjs')),environment,'apply' if apply else 'audit',*([str(policy_file)] if policy_file else [])],cwd=r.ROOT,env={**os.environ,'MULTICA_RELEASE_ROOT':str(r.ROOT)})

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--environment',choices=('int',*r.VERIFICATION_ENVS),required=True)
    parser.add_argument('--apply',action='store_true')
    parser.add_argument('--policy-file',type=Path)
    parser.add_argument('--scope-file',type=Path,help='当前候选单 Agent 维护身份；策略始终读取控制端 desired override')
    args=parser.parse_args();run(args.environment,args.apply,args.policy_file,args.scope_file)
