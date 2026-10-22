#!/usr/bin/env python3
"""显式启用的真实 Agent 验收：保存未提交代码，中断并重启 Daemon，再自动续接。"""
import importlib.util, json, os, subprocess, time, urllib.request, uuid
from pathlib import Path

spec=importlib.util.spec_from_file_location('release',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def verify():
    if os.environ.get('MULTICA_RUN_REAL_AGENT_SMOKE')!='1':raise RuntimeError('真实 Agent 验收需要显式设置 MULTICA_RUN_REAL_AGENT_SMOKE=1')
    env=os.environ.get('GOAL_TEST_ENV','int')
    if env not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('真实暂停验收禁止使用生产环境')
    meta=r.read(r.meta_path(env));profile=r.read(meta['daemon_profile_path']);out=r.RUN/'evidence'/'release-pause'/meta['release_id'];out.mkdir(parents=True,exist_ok=True)
    def api(method,route,body=None):
        req=urllib.request.Request(meta['backend_url']+'/api/'+route,data=None if body is None else json.dumps(body).encode(),method=method,
            headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json'})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=30) as response:
            raw=response.read();return json.loads(raw) if raw else None
    if api('GET','config').get('environment_id')!=env:raise RuntimeError('测试环境身份不符')
    if not r.http(r.health_url(env)+'/drain').get('release_pause_supported'):raise RuntimeError('当前 Daemon 尚不支持发布暂停')
    state_path=out/'state.json'
    if state_path.exists():raise RuntimeError('本候选已有真实验收意图，请读取原任务与结果，禁止重复创建')
    nonce=str(uuid.uuid4());state=dict(environment=env,release_id=meta['release_id'],nonce=nonce,phase='creating')
    r.write(state_path,state,True)
    runtimes=api('GET','runtimes')
    provider=os.environ.get('MULTICA_ACCEPTANCE_PROVIDER','codex')
    choices=[x for x in runtimes if x.get('provider')==provider and x.get('status')=='online' and x.get('daemon_id')==r.http(r.health_url(env)+'/health')['daemon_id']]
    if len(choices)!=1:raise RuntimeError('无法唯一确定本测试环境选定的在线运行时')
    agent=api('POST','agents',dict(name='发布暂停续接验收',description='受控发布生命周期测试',instructions='只执行任务正文的受控文件和进程验收，不读取或修改其他仓库，不提交交付记录，不自行取消任务。任务已获用户明确授权，无需另行计划确认。',runtime_id=choices[0]['id'],visibility='private',max_concurrent_tasks=1))
    state['provider']=provider;state['agent_id']=agent['id'];r.write(state_path,state,True)
    program='''import json, os, pathlib, subprocess, time
p=pathlib.Path('.release-pause-test');p.mkdir(exist_ok=True)
nonce=NONCE
def git(*args):return subprocess.check_output(['git',*args],text=True).strip()
permit=p/'permit.json'
if permit.exists():
    assert json.loads(permit.read_text())['nonce']==nonce
    first=json.loads((p/'started.json').read_text())
    assert pathlib.Path('notes.txt').read_text()==nonce
    assert git('rev-parse','HEAD')==first['head']
    result=dict(nonce=nonce,phase='resumed',source_task=first['task'],task=os.environ['MULTICA_TASK_ID'],cwd=str(pathlib.Path.cwd()),head=first['head'])
    (p/'resumed.json').write_text(json.dumps(result));print(json.dumps(result))
else:
    if not pathlib.Path('.git').exists():
        subprocess.run(['git','init','-q'],check=True)
        git('config','user.name','Release acceptance');git('config','user.email','acceptance@example.test')
        pathlib.Path('baseline.txt').write_text('baseline');git('add','baseline.txt');git('commit','-qm','test baseline')
    pathlib.Path('notes.txt').write_text(nonce)
    (p/'started.json').write_text(json.dumps(dict(nonce=nonce,task=os.environ['MULTICA_TASK_ID'],head=git('rev-parse','HEAD'))))
    print('READY_FOR_RELEASE_PAUSE',flush=True)
    time.sleep(600)
'''.replace('NONCE',repr(nonce))
    description='''这是用户已授权的真实发布暂停续接测试。只在当前工作目录执行，不访问其他项目，不提交代码，不修改任务状态。
立即使用终端执行下面完整 Python 程序。首次执行会保存一个未提交文件并等待，必须等待命令结束，不能提前返回或自行终止。发布中断后再次执行同一程序；若 permit.json 已存在，它会核对原文件及 Git HEAD 并保存 resumed.json，成功后仅回复“暂停续接测试已完成”。不要自行创建 permit.json。

```python
'''+program+'\n```'
    issue=api('POST','issues',dict(title='发布暂停与自动续接受控验收',description=description,status='in_progress',assignee_type='agent',assignee_id=agent['id']))
    state.update(issue_id=issue['id'],identifier=issue['identifier'],phase='waiting_for_agent');r.write(state_path,state,True)
    print(json.dumps(dict(phase=state['phase'],issue=issue['identifier'])),flush=True)
    def wait_for(fn,seconds,label):
        end=time.monotonic()+seconds
        while time.monotonic()<end:
            value=fn()
            if value:return value
            time.sleep(2)
        raise RuntimeError('等待超时：'+label)
    def find_started():
        for file in Path(meta['daemon_workspaces_root']).glob('*/*/workdir/.release-pause-test/started.json'):
            if r.read(file).get('nonce')==nonce:return file
        return None
    marker=wait_for(find_started,300,'真实 Agent 写入并等待')
    first=r.read(marker);state.update(source_task=first['task'],work_dir=str(marker.parent.parent),phase='started');r.write(state_path,state,True)
    artifact=Path(meta['release_dir']);cfg=r.read(r.config_path(env))
    with r.lock('publisher'):
        with r.release_task_window(env,artifact):
            window=r.read(r.release_window_path(env));state['window_id']=window['id'];r.write(state_path,state,True)
            r.http(r.health_url(env)+'/drain','POST')
            wait_for(lambda:r.http(r.health_url(env)+'/drain')['claims_in_flight']==0,30,'在途认领退出')
            r.release_window_api(env,dict(id=window['id'],action='pause'))
            def stopped():
                snapshot=r.release_window_api(env);health=r.http(r.health_url(env)+'/drain')
                own=next((x for x in snapshot.get('tasks',[]) if x['task_id']==first['task']),None)
                return snapshot if own and own['state']=='stopped' and health['active_tasks']==0 else None
            paused=wait_for(stopped,90,'真实任务保存与停止确认');r.write(out/'paused.json',paused,True)
            if Path(state['work_dir'],'notes.txt').read_text()!=nonce:raise RuntimeError('暂停丢失未提交代码')
            r.write(marker.parent/'permit.json',dict(nonce=nonce),True)
            old_pid=meta['pids']['daemon'];r.http(r.health_url(env)+'/shutdown','POST')
            wait_for(lambda:not r.exists(old_pid),60,'旧 Daemon 退出')
            r.run([artifact/'server/bin/multica','--profile',meta['daemon_profile'],'daemon','start','--no-auto-update','--no-auto-reload'],cwd=artifact,env=cfg['daemon'],stdout=subprocess.DEVNULL,**r.service_credentials(cfg['daemon']))
            live=r.ready(r.health_url(env)+'/health',timeout=90)
            if live['profile']!=meta['daemon_profile'] or live['pid']==old_pid:raise RuntimeError('Daemon 未实际重启')
            updated=r.read(r.meta_path(env));updated['pids']['daemon']=live['pid'];r.write(r.meta_path(env),updated,True)
            (r.RUN/(env+'-daemon.pid')).write_text(str(live['pid'])+'\n')
            state.update(old_daemon_pid=old_pid,new_daemon_pid=live['pid'],phase='restarted');r.write(state_path,state,True)
            print(json.dumps(dict(phase='restarted',source_task=first['task'])),flush=True)
        proof=wait_for(lambda:r.read(marker.parent/'resumed.json') if (marker.parent/'resumed.json').exists() else None,300,'自动续接并核对原代码')
        if proof['source_task']!=first['task'] or proof['task']==first['task'] or proof['nonce']!=nonce:raise RuntimeError('续接没有绑定原执行')
        def completed():
            rows=api('GET','issues/'+issue['id']+'/task-runs');rows=rows if isinstance(rows,list) else rows.get('tasks',[])
            own=next((x for x in rows if x['id']==proof['task']),None)
            if own and own['status'] in ('failed','cancelled'):raise RuntimeError('续接执行未成功：'+json.dumps(own,ensure_ascii=False))
            return rows if own and own['status']=='completed' else None
        tasks=wait_for(completed,120,'续接执行完成');r.write(out/'tasks.json',tasks,True)
    current=api('GET','issues/'+issue['id']);api('PUT','issues/'+issue['id'],dict(status='done',revision=current['revision']))
    state.update(phase='passed',proof=proof);r.write(state_path,state,True);print(json.dumps(state,ensure_ascii=False),flush=True)
    return state

if __name__=='__main__':verify()
