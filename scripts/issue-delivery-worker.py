#!/usr/bin/env python3
"""外部交付接力：数据库保存审核真源，复用固定发布器执行联调和生产更新。"""
import argparse, importlib.util, json, os, pwd, subprocess, sys, threading, time, urllib.request, urllib.error
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
        if d.get('phase')=='publishing':r.check_delivery_approval(c)
        else:self.advance(d,'publishing')
        r.run(['git','merge','--ff-only',tested],cwd=r.ROOT)
        r.run(['git','push','origin','HEAD:personal-studio-upstream'],cwd=r.ROOT)
        if r.output(['git','ls-remote','origin','refs/heads/personal-studio-upstream'],cwd=r.ROOT).split()[0]!=tested:raise RuntimeError('远端主分支核对失败')
        r.request_release(c['id'])
    def pool_enabled(self):
        path=r.RUN/'verification-pool.json'
        return path.exists() and r.read(path).get('enabled') is True and r.read(path).get('controller_environment')==self.env

    def pool_job_path(self,delivery):
        # 编号来自服务端交付记录，不接收用户指定的文件路径。
        import uuid
        return r.RUN/'verification-jobs'/(str(uuid.UUID(delivery))+'.json')

    def pool_dispatch(self,rows,current_id,allow_new=True):
        if not allow_new and not (r.RUN/'verification-jobs').exists():return set()
        managed=[]; active=0
        records={d['id']:d for d in rows}
        recovering=set()
        for path in (r.RUN/'verification-jobs').glob('*.json'):
            job=r.read(path)
            if not job.get('lease') or job.get('stage') in ('released','quarantined'):continue
            identity=job['delivery_id']
            self.pool_job_path(identity)
            recovering.add(identity)
            if identity not in records:
                found=self.api('delivery-queue?current='+identity)
                records.update({d['id']:d for d in found if d['id']==identity})
        for d in sorted(records.values(),key=lambda d:d.get('created_at','')):
            if d['id']==current_id:continue
            job_path=self.pool_job_path(d['id'])
            existing=job_path.exists()
            if existing and r.read(job_path).get('stage')=='quarantined':continue
            if d['id'] not in recovering and (d['phase'] not in ('waiting_int','testing') or not (allow_new or existing)):
                continue
            managed.append(d)
            unit='multica-verify-'+d['id']
            if subprocess.run(['systemctl','is-active','--quiet',unit]).returncode==0:active+=1
        for d in managed:
            unit='multica-verify-'+d['id']
            if subprocess.run(['systemctl','is-active','--quiet',unit]).returncode==0:continue
            if active>=4:break
            r.run(['systemd-run','--quiet','--collect','--unit',unit,'--property=Type=exec',
                   '--property=KillMode=control-group','--working-directory',r.ROOT,
                   '--setenv=MULTICA_RELEASE_ROOT='+str(r.ROOT),sys.executable,Path(__file__).resolve(),
                   '--env',self.env,'--pool-delivery',d['id']])
            active+=1
        return {d['id'] for d in managed}

    def pool_delivery(self,delivery):
        path=self.pool_job_path(delivery)
        def refresh():
            return next((d for d in self.api('delivery-queue?current='+delivery) if d['id']==delivery),None)
        def slot(lease,action,detail=''):
            return self.api('delivery-queue/slots/advance',dict(lease,action=action,detail=detail))
        def save(stage,**extra):
            r.write(path,dict(stage=stage,delivery_id=delivery,updated_at=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),**extra),True)
            if stage in ('building','waiting_slot'):
                slot(dict(delivery_id=delivery,worker_token=d['worker_token'],source_commit=d['source_commit']),'progress',stage)
        def release(lease,c):
            save('draining',lease=lease,candidate_id=c['id'])
            slot(lease,'drain')
            r.drain(lease['name'],r.read(r.meta_path(lease['name'])))
            r.release_candidate(c['id'],env=lease['name'])
            slot(lease,'release')
            save('released',candidate_id=c['id'])
        lease=None; stopped=threading.Event(); heartbeat_errors=[]
        with r.lock('verification-job-'+delivery):
            try:
                d=refresh()
                if not d:return
                job=r.read(path) if path.exists() else {}
                c=None
                if job.get('lease'):
                    try:
                        lease=self.api('delivery-queue/slots?delivery_id='+delivery)
                    except urllib.error.HTTPError as e:
                        if e.code!=404:raise
                        save('released',candidate_id=job.get('candidate_id',''));return
                    if lease['source_commit']!=job['lease']['source_commit'] or lease['worker_token']!=job['lease']['worker_token']:
                        raise r.RecoveryRequired('恢复占用与原交付身份不一致')
                    c=r.candidate(env=lease['name'])
                    if c['delivery_id']!=delivery or c['id']!=lease['candidate_id']:
                        raise r.RecoveryRequired('部署中断，候选尚未完成登记，需要核对现场')
                    r.check_process_ownership(lease['name'],r.read(r.meta_path(lease['name'])))
                    if d['phase']=='testing':r.validate_candidate(c)
                    lease=slot(lease,'resume')
                    c['generation']=lease['generation']
                    with r.lock(r.candidate_lock(lease['name'])):r.save_candidate(c)
                    save('verifying',lease=lease,candidate_id=c['id'])
                    if d['phase']!='testing':
                        release(lease,c);return
                else:
                    if d['phase'] not in ('waiting_int','testing'):return
                    if d['phase']=='waiting_int':self.advance(d,'claim')
                    source=self.source(d,integrate=True)
                    save('building')
                    artifact=r.build_verification(source,delivery)
                    save('waiting_slot',artifact=str(artifact))
                    while lease is None:
                        d=refresh()
                        if not d or d['phase']!='testing':return
                        try:
                            lease=slot(dict(delivery_id=delivery,worker_token=d['worker_token'],source_commit=d['source_commit']),'claim')
                        except urllib.error.HTTPError as e:
                            detail=json.loads(e.read().decode()) if e.code==409 else {}
                            if detail.get('error')!='没有可用验收槽位':raise
                            time.sleep(5)
                    save('preparing',lease=lease,artifact=str(artifact))
                def heartbeat():
                    while not stopped.wait(20):
                        try:slot(lease,'heartbeat')
                        except Exception as e:
                            heartbeat_errors.append(str(e));return
                heartbeat_thread=threading.Thread(target=heartbeat,daemon=True);heartbeat_thread.start()
                if c is None:
                    c=r.prepare_verification(source,artifact,lease)
                    lease['candidate_id']=c['id'];slot(lease,'prepared')
                    save('verifying',lease=lease,candidate_id=c['id'])
                if heartbeat_errors:raise r.RecoveryRequired('验收心跳失效')
                d=refresh()
                if d['phase']=='testing':
                    if d['candidate_id']!=c['id']:
                        self.advance(d,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path(lease['name']))['frontend_url'])
                    self.advance(d,'notify')
                while True:
                    d=refresh()
                    if not d or d['phase']!='testing':break
                    if heartbeat_errors:raise r.RecoveryRequired('验收心跳失效')
                    attempt=d.get('verification_run') or {}
                    if attempt.get('settled') and attempt.get('status') in ('failed','cancelled','blocked','completed') and not d['acceptance_submitted']:
                        self.advance(d,'fail',error=attempt.get('error') or '验收执行已结束，但没有提交通过证据')
                        break
                    if d['acceptance_submitted']:
                        evidence=r.RUN/'delivery-evidence'/(delivery+'.json')
                        r.write(evidence,dict(candidate=c['id'],summary='任务验收',checks=d['evidence']),True)
                        if c['status']!='accepted':r.accept(c['id'],evidence,env=lease['name'])
                        self.advance(d,'accepted');break
                    time.sleep(5)
                stopped.set();heartbeat_thread.join(timeout=25)
                release(lease,c)
            except Exception as e:
                stopped.set()
                if lease:
                    try:slot(lease,'quarantine',str(e)[:1500])
                    except Exception:pass
                    save('quarantined',lease=lease,error=str(e)[:1500])
                current=refresh()
                if current and current['phase']=='testing':self.advance(current,'fail',error=str(e)[:1500])
                raise
            finally:
                stopped.set()

    def tick(self):
        with r.lock('delivery-worker-'+self.env):
            current=r.candidate() if r.candidate_path().exists() else None
            current_id=current.get('delivery_id') if current else None
            rows=self.api('delivery-queue'+('?current='+current_id if current_id else ''))
            if self.pool_enabled():
                managed=self.pool_dispatch(rows,current_id)
            else:
                managed=self.pool_dispatch(rows,current_id,allow_new=False)
            rows=[d for d in rows if d['id'] not in managed]
            owner=next((d for d in rows if d['id']==current_id),None)
            if current and owner and owner['phase'] in ('cancelled','needs_changes','awaiting_review') and current['status'] in ('verifying','accepted'):
                r.release_candidate(current['id'])
            for d in rows:
                if d['phase'] not in ('cancelled','needs_changes'):
                    try:self.pin(d)
                    except Exception as e:self.advance(d,'fail',error='无法固定交付提交：'+str(e)[:1500]);return
            # 先完成已占用环境的交付，再按创建顺序处理排队项。
            active=[d for d in rows if d['phase'] in ('testing','preparing_release','publishing') or (d['phase']=='awaiting_review' and d.get('plan_snapshot',{}).get('authorization_pending'))]
            queued=[d for d in rows if d['phase'] in ('waiting_int','approved')]
            for d in active+queued:
                try:
                    phase=d['phase']
                    if phase=='awaiting_review':
                        self.advance(d,'notify-authorization');return
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
                        if not target.exists():self.publish(d);return
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
    service='[Unit]\nDescription=Multica delivery '+env+'\nAfter=network.target\n[Service]\nType=oneshot\nKillMode=process\nEnvironment=HOME='+pwd.getpwuid(os.getuid()).pw_dir+'\nWorkingDirectory='+str(r.ROOT)+'\nEnvironment=MULTICA_RELEASE_ROOT='+str(r.ROOT)+'\nExecStart='+sys.executable+' '+str(directory/'issue-delivery-worker.py')+' --env '+env+'\n'
    timer='[Unit]\nDescription=Multica delivery queue '+env+'\n[Timer]\nOnBootSec=30s\nOnUnitInactiveSec=15s\nUnit='+unit+'.service\n[Install]\nWantedBy=timers.target\n'
    (units/(unit+'.service')).write_text(service)
    (units/(unit+'.timer')).write_text(timer)
    r.run(['systemctl','daemon-reload'])
    r.run(['systemctl','enable','--now',unit+'.timer'])

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--env',choices=r.ENVS,required=True);p.add_argument('--install',action='store_true');p.add_argument('--pool-delivery');a=p.parse_args()
    if a.install:install(a.env)
    else:
        if os.environ.get('MULTICA_TASK_ID'):raise RuntimeError('交付接力必须由外部进程执行')
        worker=Worker(a.env)
        if a.pool_delivery: worker.pool_delivery(a.pool_delivery)
        else: worker.tick()
