#!/usr/bin/env python3
"""按候选声明运行验收程序；没有脚本的任务仍通过原任务通知进行业务验收。"""
import argparse, importlib.util, json, os, subprocess
from pathlib import Path
spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def execute(candidate_id,environment):
    if environment not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('自动实验禁止使用生产环境')
    c=r.candidate(candidate_id,environment);r.validate_candidate(c)
    source=Path(c['source']);recipe=source/'scripts/delivery-acceptance.json'
    if not recipe.exists():return None
    plan=c.get('validation_plan',{'mode':'full'})
    if plan['mode']=='reuse':return None
    groups=r.read(recipe)['groups'];selected=groups if plan['mode']=='full' else [g for g in groups if g['name'] in plan['checks']]
    if not selected:raise RuntimeError('候选没有覆盖所需范围的验收程序')
    checks=[]
    for group in selected:
        command=group.get('command')
        if not isinstance(command,list) or not command or not all(isinstance(v,str) for v in command):raise RuntimeError('验收程序必须是参数数组')
        env={**os.environ,'MULTICA_RELEASE_ROOT':str(r.ROOT),'GOAL_TEST_ENV':environment,'MULTICA_ACCEPTANCE_CANDIDATE':candidate_id}
        result=subprocess.run(command,cwd=source,env=env,capture_output=True,text=True,timeout=600)
        report=r.RUN/'delivery-evidence'/(candidate_id+'-'+group['name']+'.log');report.parent.mkdir(parents=True,exist_ok=True);report.write_text(result.stdout+'\n'+result.stderr)
        if result.returncode:raise RuntimeError('验收程序失败：'+group['name']+'；日志 '+str(report))
        checks.append(dict(name=group['name'],status='passed',evidence='实际执行 '+json.dumps(command,ensure_ascii=False)+'；日志 '+str(report)))
    evidence=dict(candidate=candidate_id,summary='候选声明的自动验收程序',checks=checks)
    r.write(r.RUN/'delivery-evidence'/(candidate_id+'-automatic.json'),evidence,True)
    return evidence

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('candidate');p.add_argument('--environment',default='int');a=p.parse_args();print(json.dumps(execute(a.candidate,a.environment),ensure_ascii=False))
