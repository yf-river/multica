#!/usr/bin/env python3
"""按槽位锁维护测试模型，避免与候选部署竞争。"""
import argparse
import importlib.util
import os
from pathlib import Path
spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def run(environment, apply=False):
    if environment not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('禁止维护生产 Agent 模型')
    with r.lock(r.candidate_lock(environment),wait=False):
        meta=r.read(r.meta_path(environment));cfg=r.read(r.config_path(environment))
        r.check_process_ownership(environment,meta)
        r.check_live_environment(environment,meta,cfg)
        candidate=r.candidate_path(environment)
        if apply and candidate.exists() and r.read(candidate).get('status') not in ('released','failed','succeeded'):
            raise RuntimeError('当前候选未释放，空闲后再维护')
        return r.run(['node',str(Path(__file__).with_suffix('.mjs')),environment,'apply' if apply else 'audit'],cwd=r.ROOT,env={**os.environ,'MULTICA_RELEASE_ROOT':str(r.ROOT)})

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--environment',choices=('int',*r.VERIFICATION_ENVS),required=True)
    parser.add_argument('--apply',action='store_true')
    args=parser.parse_args();run(args.environment,args.apply)
