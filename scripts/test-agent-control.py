#!/usr/bin/env python3
"""发布器维护测试 Agent 配置；每轮最多处理一个环境，不打断运行任务。"""
import json
from datetime import datetime, timezone
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

def catalog_stale(runtime):
    try:
        observed=datetime.fromisoformat((runtime.get('catalog_observed_at') or '').replace('Z','+00:00'))
        return not runtime.get('models') or (datetime.now(timezone.utc)-observed).total_seconds()>300
    except (ValueError,TypeError):return True


def catalog_error(runtime, policy):
    if not runtime or catalog_stale(runtime):return '运行时模型目录暂不可用，等待更新'
    model=next((item for item in runtime['models'] if item.get('id')==policy['model']),None)
    if not model:return '所选模型不在本环境运行时目录中'
    thinking=policy.get('thinking_level') or ''
    if thinking and not any(item.get('value')==thinking for item in (model.get('thinking') or {}).get('supported_levels',[])):
        return '所选思考强度不受本环境模型支持'
    tier=policy.get('service_tier') or ''
    if tier and not any(item.get('id')==tier for item in model.get('service_tiers',[])) and not (tier=='default' and model.get('supports_explicit_standard_service_tier')):
        return '所选速度不受本环境模型支持'
    return ''


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
    report=dict(status='pending',revision=(row or {}).get('revision',1),agents=[],error='',available=False)
    try:
        if env != 'int':
            slots=worker.api('delivery-queue/slots')
            slot=next((item for item in slots if item.get('name')==env),None)
            if not slot or not slot.get('enabled'):
                report.update(status='pending',error='验收槽位未启用，启用后自动应用')
                if row:
                    report['agents']=row.get('observed',{}).get('agents',[])
                    report['runtimes']=row.get('observed',{}).get('runtimes',[])
                worker.api('test-agent-control',dict(action='report',name=env,observed=report))
                stamp.write_text(json.dumps(dict(at=time.time(),index=index)))
                return
        with tempfile.NamedTemporaryFile(mode='w',suffix='.json') as fp:
            json.dump(desired,fp);fp.flush()
            command=['python3',str(Path(__file__).with_name('test-agent-models.py')),'--environment',env,'--policy-file',fp.name]
            def run(apply=False):
                previous_runtimes=(row or {}).get('observed',{}).get('runtimes',[])
                needs_catalog=not previous_runtimes or any(catalog_stale(runtime) for runtime in previous_runtimes)
                result=subprocess.run(command+(['--apply'] if apply else []),capture_output=True,text=True,timeout=180,env={**os.environ,'MULTICA_RELEASE_ROOT':str(root),'MULTICA_TEST_AGENT_CATALOG':'1' if needs_catalog else '0'})
                if result.returncode:raise RuntimeError((result.stderr or result.stdout)[-1600:])
                return json.loads(result.stdout.strip().splitlines()[-1])
            audit=run();report['available']=True;report['runtimes']=audit['runtimes'];report['agents']=[{k:a.get(k) for k in ('id','name','runtime_id','model','thinking_level','service_tier','status','archived_at')} for a in audit['after']]
            providers={r['id']:r['provider'] for r in audit['runtimes']}
            def matches(a):
                p=desired.get('overrides',{}).get(a['id'],desired['policy'])
                return providers.get(a['runtime_id'])==(p.get('runtime') or p['provider']) and all((a.get(k) or '')==p[k] for k in ('model','thinking_level','service_tier'))
            if not all(matches(a) for a in audit['after']) or (row or {}).get('observed',{}).get('revision')!=report['revision']:
                audit=run(True);report['agents']=[{k:a.get(k) for k in ('id','name','runtime_id','model','thinking_level','service_tier','status','archived_at')} for a in audit['after']]
            previous={r['id']:r for r in (row or {}).get('observed',{}).get('runtimes',[])}
            for runtime in audit['runtimes']:
                if not runtime.get('models') and previous.get(runtime['id'],{}).get('models'):
                    runtime['models']=previous[runtime['id']]['models']
                    runtime['catalog_observed_at']=previous[runtime['id']].get('catalog_observed_at')
            report.update(status='applied' if row else 'pending',revision=row['revision'] if row else 0,runtimes=audit['runtimes'])
            by_id={runtime['id']:runtime for runtime in audit['runtimes']}
            for agent in audit['after']:
                policy=desired.get('overrides',{}).get(agent['id'],desired['policy'])
                problem=catalog_error(by_id.get(agent['runtime_id']),policy)
                if problem:
                    report.update(status='pending' if '暂不可用' in problem else 'error',error=problem)
                    break
    except Exception as error:
        # 只呈现可操作的错误摘要，不回传连接字符串、命令或凭据。
        detail=str(error)
        report['error']=('环境忙，空闲后自动应用' if any(x in detail for x in ('仍有','未释放','锁')) else '环境进程与部署记录不一致，请检查环境状态' if 'PID 工作目录不属于记录的版本' in detail else '所选模型或运行时在本环境不可用，请检查运行时与模型目录' if any(x in detail for x in ('所选运行时','所选模型','所选思考强度','所选速度','工作区所选运行时')) else '环境核对或应用失败，请查看发布器日志')
        report['status']='pending' if report['error'].startswith('环境忙') else 'error'
        print('test-agent-control',env,report['error'],re.sub(r'postgres(?:ql)?://[^\s]+','[connection redacted]',detail)[-1600:],flush=True)
        if row:
            if not report['agents']:report['agents']=row.get('observed',{}).get('agents',[])
            if not report.get('runtimes'):report['runtimes']=row.get('observed',{}).get('runtimes',[])
    worker.api('test-agent-control',dict(action='report',name=env,observed=report))
    stamp.write_text(json.dumps(dict(at=time.time(),index=index)))
