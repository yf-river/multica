#!/usr/bin/env python3
"""外部交付接力：数据库保存审核真源，复用固定发布器执行联调和生产更新。"""
import argparse, importlib.util, json, os, subprocess, sys, urllib.request, urllib.error
from pathlib import Path

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class Busy(RuntimeError): pass

class Worker:
    def __init__(self,env):
        r.require_environment(env)
        self.env=env
        self.meta=r.read(r.meta_path(env))
        self.profile=r.read(self.meta['daemon_profile_path'])
    def api(self,path,body=None,method=None):
        req=urllib.request.Request(self.meta['backend_url'].rstrip('/')+'/api/'+path,
            data=None if body is None else json.dumps(body).encode(),method=method,
            headers={'Authorization':'Bearer '+self.profile['token'],'X-Workspace-ID':self.profile['workspace_id'],'Content-Type':'application/json'})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=30) as response:return json.load(response)
    def advance(self,d,action,**extra):
        receipt=self.api('delivery-queue/advance',dict(id=d['id'],revision=d['revision'],worker_token=d.get('worker_token') or '',action=action,source_commit=d['source_commit'],**extra))
        d.update(receipt)
        return d
    def pin(self,d):
        # 只从固定仓库恢复提交；任务不能通过请求指定任意本机目录。
        if subprocess.run(['git','-C',r.ROOT,'cat-file','-e',d['source_commit']+'^{commit}'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode:
            r.run(['git','-C',r.ROOT,'fetch','origin','refs/heads/'+d['source_branch']])
        r.run(['git','-C',r.ROOT,'cat-file','-e',d['source_commit']+'^{commit}'])
        r.run(['git','-C',r.ROOT,'update-ref','refs/multica/deliveries/'+d['id'],d['source_commit']])
    def source(self,d,integrate=False):
        self.pin(d)
        path=r.RUN/'delivery-sources'/d['id']; branch='feature/delivery/'+d['id']
        if not path.exists():
            args=['git','-C',r.ROOT,'worktree','add','-B',branch,path,d['source_commit']]
            r.run(args)
        if r.output(['git','status','--porcelain','--untracked-files=all'],cwd=path):raise RuntimeError('交付构建目录存在未保存内容，停止覆盖')
        if integrate:
            r.run(['git','fetch','origin','personal-studio-upstream'],cwd=path)
            if subprocess.run(['git','merge','--no-edit','origin/personal-studio-upstream'],cwd=path).returncode:
                r.run(['git','merge','--abort'],cwd=path)
                raise RuntimeError('与最新主分支存在合并冲突，请在原任务分支解决并重新提交审核')
        return path
    def own_candidate(self,d):
        c=r.candidate() if r.candidate_path().exists() else None
        if not c or c.get('delivery_id')!=d['id']:return None
        return c
    def busy(self,d):
        c=r.candidate() if r.candidate_path().exists() else None
        return c and c.get('status') not in ('released','succeeded','failed') and c.get('delivery_id')!=d['id']
    def prepare(self,d):
        if self.busy(d):raise Busy('联调正由另一项交付使用')
        c=self.own_candidate(d)
        if c and c['status']=='building':
            # 服务重启时先证明旧站仍健康；不能把半切换当作可直接重建。
            with r.lock('publisher'):
                c=self.own_candidate(d)
                if c['status']=='building':
                    live=r.read(r.meta_path('int'))
                    if live['release_id']!=c.get('previous_int_release'):raise r.RecoveryRequired('构建中断后环境已切换，需要先核实恢复状态')
                    r.check_process_ownership('int',live);r.isolation_check()
                    c['status']='failed';r.write(r.candidate_path(),c,True)
        if not c or c['status'] in ('released','succeeded','failed'):
            path=self.source(d,integrate=True)
            try:c=r.prepare_int(path,delivery=dict(delivery_id=d['id'],delivery_env=self.env,approved_source=d['source_commit']))
            except RuntimeError as e:
                if '占用' in str(e) or '构建或发布' in str(e):raise Busy(str(e)) from e
                raise
        elif c['status'] in ('verifying','accepted'):
            r.validate_candidate(c)
        else:raise r.RecoveryRequired('交付构建或恢复未完成，请先检查原发布记录')
        self.advance(d,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path('int'))['frontend_url'])
        self.advance(d,'notify')
    def accept(self,d,release=False):
        c=self.own_candidate(d)
        if not c or c['id']!=d['candidate_id']:raise RuntimeError('联调版本已被替换，需要重新排队验证')
        if c['status']=='verifying':
            evidence=r.RUN/'delivery-evidence'/(d['id']+'.json')
            r.write(evidence,dict(candidate=c['id'],summary='任务负责人提交的实际功能验收',checks=d['evidence']),True)
            r.accept(c['id'],evidence)
        if release:self.advance(d,'validated')
        else:
            self.advance(d,'accepted')
            r.release_candidate(c['id'])
    def publish(self,d):
        if self.env!='prod':raise RuntimeError('联调交付队列不能更新生产')
        c=self.own_candidate(d)
        if not c or c['status']!='accepted':raise RuntimeError('缺少已验收发布候选')
        path=Path(c['source']);tested=c['commit']
        r.run(['git','fetch','origin','personal-studio-upstream'],cwd=r.ROOT)
        if subprocess.run(['git','merge-base','--is-ancestor','origin/personal-studio-upstream',tested],cwd=r.ROOT).returncode:
            self.advance(d,'retry',error='主分支已前进，重新整合并联调');r.release_candidate(c['id']);return
        if r.output(['git','status','--porcelain','--untracked-files=all'],cwd=r.ROOT):raise Busy('主工作区有未保存改动，等待处理')
        if r.output(['git','branch','--show-current'],cwd=r.ROOT)!='personal-studio-upstream':raise RuntimeError('主工作区不在交付分支')
        r.run(['git','merge','--ff-only',tested],cwd=r.ROOT)
        r.run(['git','push','origin','HEAD:personal-studio-upstream'],cwd=r.ROOT)
        if r.output(['git','ls-remote','origin','refs/heads/personal-studio-upstream'],cwd=r.ROOT).split()[0]!=tested:raise RuntimeError('远端主分支核对失败')
        self.advance(d,'publishing')
        r.request_release(c['id'])
    def tick(self):
        with r.lock('delivery-worker-'+self.env):
            current=r.candidate() if r.candidate_path().exists() else None
            current_id=current.get('delivery_id') if current else None
            rows=self.api('delivery-queue'+('?current='+current_id if current_id else ''))
            owner=next((d for d in rows if d['id']==current_id),None)
            if current and owner and owner['phase'] in ('cancelled','needs_changes','awaiting_review') and current['status'] in ('verifying','accepted'):
                r.release_candidate(current['id'])
            for d in rows:
                if d['phase'] not in ('cancelled','needs_changes'):
                    try:self.pin(d)
                    except Exception as e:self.advance(d,'fail',error='无法固定交付提交：'+str(e)[:1500]);return
            # 先完成已占用环境的交付，再按创建顺序处理排队项。
            active=[d for d in rows if d['phase'] in ('testing','preparing_release','publishing')]
            queued=[d for d in rows if d['phase'] in ('waiting_int','approved')]
            for d in active+queued:
                try:
                    phase=d['phase']
                    if phase in ('waiting_int','approved'):
                        if self.busy(d):return
                        self.advance(d,'claim');self.prepare(d);return
                    if phase=='testing':
                        if d['acceptance_submitted']:self.accept(d)
                        elif not d['candidate_id']:self.prepare(d)
                        else:self.advance(d,'notify')
                        return
                    if phase=='preparing_release':
                        if not d['candidate_id'] or not self.own_candidate(d):self.prepare(d)
                        elif d.get('release_validated'):self.publish(d)
                        elif d['acceptance_submitted']:self.accept(d,release=True)
                        else:self.advance(d,'notify')
                        return
                    if phase=='publishing':
                        target=r.request_path(d['candidate_id'])
                        if not target.exists():r.request_release(d['candidate_id']);return
                        result=r.read(target)
                        if result['status']=='succeeded':self.advance(d,'published')
                        elif result['status'] not in ('queued','publishing'):self.advance(d,'fail',error=result.get('error','生产更新失败'))
                        return
                except Busy:
                    return
                except urllib.error.HTTPError as e:
                    if e.code==409:return
                    raise
                except Exception as e:
                    self.advance(d,'fail',error=str(e)[:2000])
                    c=self.own_candidate(d)
                    if c and c['status'] in ('verifying','accepted'):r.release_candidate(c['id'])
                    return

def install(env):
    r.require_control_directory()
    names=('issue-delivery-worker.py','release-environments.py','release-maintenance.py')
    digest=r.hashlib.sha256(b''.join((Path(__file__).parent/name).read_bytes() for name in names)).hexdigest()[:16]
    directory=r.RUN/'publisher'/('delivery-'+env+'-'+digest)
    directory.mkdir(parents=True,exist_ok=True)
    for name in names:
        target=directory/name
        if not target.exists():target.write_bytes((Path(__file__).parent/name).read_bytes())
    unit='multica-delivery-'+env
    units=Path('/etc/systemd/system')
    service='[Unit]\nDescription=Multica delivery '+env+'\nAfter=network.target\n[Service]\nType=oneshot\nKillMode=process\nWorkingDirectory='+str(r.ROOT)+'\nEnvironment=MULTICA_RELEASE_ROOT='+str(r.ROOT)+'\nExecStart='+sys.executable+' '+str(directory/'issue-delivery-worker.py')+' --env '+env+'\n'
    timer='[Unit]\nDescription=Multica delivery queue '+env+'\n[Timer]\nOnBootSec=30s\nOnUnitInactiveSec=15s\nUnit='+unit+'.service\n[Install]\nWantedBy=timers.target\n'
    (units/(unit+'.service')).write_text(service)
    (units/(unit+'.timer')).write_text(timer)
    r.run(['systemctl','daemon-reload'])
    r.run(['systemctl','enable','--now',unit+'.timer'])

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--env',choices=r.ENVS,required=True);p.add_argument('--install',action='store_true');a=p.parse_args()
    if a.install:install(a.env)
    else:
        if os.environ.get('MULTICA_TASK_ID'):raise RuntimeError('交付接力必须由外部进程执行')
        Worker(a.env).tick()
