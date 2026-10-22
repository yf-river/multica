#!/usr/bin/env python3
"""固定联调环境中的组合交付接口验收，仅创建并回收本轮测试任务。"""
import importlib.util,json,os,urllib.request,uuid
from pathlib import Path
spec=importlib.util.spec_from_file_location('release',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def verify():
    environment=os.environ.get('GOAL_TEST_ENV','int')
    if environment not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('接口验收禁止使用生产环境')
    meta=r.read(r.meta_path(environment));profile=r.read(meta['daemon_profile_path'])
    def api(method,route,body=None):
        req=urllib.request.Request(meta['backend_url']+'/api/'+route,method=method,data=None if body is None else json.dumps(body).encode(),headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json'})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=30) as response:
            raw=response.read();return json.loads(raw) if raw else None
    if api('GET','config').get('environment_id')!=environment:raise RuntimeError('环境身份不符')
    checks=[];created=[]
    try:
        settings=api('GET','review/settings')
        if not settings.get('can_manage') or not settings.get('default_instructions') or settings.get('max_rounds')!=6:raise RuntimeError('审查设置返回不完整')
        checks.append('管理员可读取审查设置、默认提示词和停止策略')
        instructions=api('GET','supervision/instructions')
        if not instructions.get('instructions') or not instructions.get('version'):raise RuntimeError('监督默认提示词不可用')
        checks.append('监督默认提示词与版本入口可读取')
        me=api('GET','me')
        issue=api('POST','issues',dict(title='组合交付创建验收 '+str(uuid.uuid4())[:8],status='todo',priority='important_urgent',due_date='2026-11-05',assignee_type='member',assignee_id=me['id']))
        created.append(issue['id']);saved=api('GET','issues/'+issue['id'])
        if saved['priority']!='important_urgent' or saved['due_date']!='2026-11-05':raise RuntimeError('新建任务字段未保存')
        checks.append('真实任务创建、优先级和截止日期刷新持久化')
        report=dict(environment=environment,release_id=meta['release_id'],commit=meta['commit'],checks=checks,status='passed')
        r.write(r.RUN/'delivery-evidence'/('batch-'+meta['release_id']+'.json'),report,True)
        print(json.dumps(report,ensure_ascii=False));return report
    finally:
        for identity in created:api('DELETE','issues/'+identity)

if __name__=='__main__':verify()
