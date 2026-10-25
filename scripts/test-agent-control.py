#!/usr/bin/env python3
"""发布器维护测试 Agent 配置；每轮最多处理一个环境，不打断运行任务。"""
import json
import os
import re
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

ENVIRONMENTS = ('int', 'verify-1', 'verify-2', 'verify-3')
FLASH = dict(provider='codebuddy',model='deepseek-v4.1-flash',thinking_level='',service_tier='')

def refresh_default(root, environment=None):
    meta=json.loads((root/'.run/deployments/goal-test-prod.json').read_text())
    profile=json.loads(Path(meta['daemon_profile_path']).read_text())
    request=urllib.request.Request(meta['backend_url'].rstrip('/')+'/api/test-agent-control',headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id']})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=10) as response:rows=json.load(response)
    default=next((x['desired'] for x in rows if x['name']=='default'),dict(policy=FLASH,overrides={}))
    target=root/'.run/test-agent-policy.json';temporary=target.with_suffix('.tmp')
    temporary.write_text(json.dumps(default));temporary.replace(target)
    if environment is not None:
        if environment not in ENVIRONMENTS:raise RuntimeError("未知测试环境")
        selected=next((x["desired"] for x in rows if x["name"]==environment),default)
        target=root/(".run/test-agent-policy-"+environment+".json")
        with tempfile.NamedTemporaryFile(mode="w",dir=target.parent,delete=False) as fp:
            json.dump(selected,fp);temporary=Path(fp.name)
        temporary.replace(target)
        return selected
    return default

def tick(worker):
    root = Path(os.environ.get('MULTICA_RELEASE_ROOT', Path(__file__).resolve().parent.parent))
    stamp = root/'.run/test-agent-control-poll.json'
    previous = json.loads(stamp.read_text()) if stamp.exists() else {}
    if time.time()-previous.get('at',0)<30:return
    rows = worker.api('test-agent-control')
    default = next((x['desired'] for x in rows if x['name']=='default'),dict(policy=FLASH,overrides={}))
    # 种子恢复及辅助创建读取同一默认值，文件不包含凭据。
    policy_path=root/'.run/test-agent-policy.json'
    temporary=policy_path.with_suffix('.tmp');temporary.write_text(json.dumps(default));temporary.replace(policy_path)
    index=previous.get('index',-1)+1;env=ENVIRONMENTS[index%len(ENVIRONMENTS)]
    row=next((x for x in rows if x['name']==env),None)
    desired=(row or {}).get('desired',default)
    report=dict(status='pending',revision=(row or {}).get('revision',1),agents=[],error='')
    try:
        with tempfile.NamedTemporaryFile(mode='w',suffix='.json') as fp:
            json.dump(desired,fp);fp.flush()
            command=['python3',str(Path(__file__).with_name('test-agent-models.py')),'--environment',env,'--policy-file',fp.name]
            def run(apply=False):
                result=subprocess.run(command+(['--apply'] if apply else []),capture_output=True,text=True,timeout=40,env={**os.environ,'MULTICA_RELEASE_ROOT':str(root)})
                if result.returncode:raise RuntimeError((result.stderr or result.stdout)[-1600:])
                return json.loads(result.stdout.strip().splitlines()[-1])
            audit=run();report['agents']=[{k:a.get(k) for k in ('id','name','runtime_id','model','thinking_level','service_tier','status','archived_at')} for a in audit['after']]
            providers={r['id']:r['provider'] for r in audit['runtimes']}
            def matches(a):
                p=desired.get('overrides',{}).get(a['id'],desired['policy'])
                return providers.get(a['runtime_id'])==p['provider'] and all((a.get(k) or '')==p[k] for k in ('model','thinking_level','service_tier'))
            if not all(matches(a) for a in audit['after']) or (row or {}).get('observed',{}).get('revision')!=report['revision']:
                audit=run(True);report['agents']=[{k:a.get(k) for k in ('id','name','runtime_id','model','thinking_level','service_tier','status','archived_at')} for a in audit['after']]
            report.update(status='applied' if row else 'pending',revision=row['revision'] if row else 0,runtimes=audit['runtimes'])
    except Exception as error:
        # 只呈现可操作的错误摘要，不回传连接字符串、命令或凭据。
        detail=str(error)
        report['error']='环境忙，空闲后自动应用' if any(x in detail for x in ('仍有','未释放','锁')) else '环境核对或应用失败，请查看发布器日志'
        report['status']='pending' if report['error'].startswith('环境忙') else 'error'
        print('test-agent-control',env,report['error'],re.sub(r'postgres(?:ql)?://[^\s]+','[connection redacted]',detail)[-1600:],flush=True)
        if not report['agents'] and row:report['agents']=row.get('observed',{}).get('agents',[])
    worker.api('test-agent-control',dict(action='report',name=env,observed=report))
    stamp.write_text(json.dumps(dict(at=time.time(),index=index)))
