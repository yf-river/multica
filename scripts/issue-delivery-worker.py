#!/usr/bin/env python3
"""外部交付接力：数据库保存审核真源，复用固定发布器执行联调和生产更新。"""
import contextlib, argparse, datetime, importlib.util, json, os, pwd, re, subprocess, sys, threading, time, urllib.request, urllib.error
from pathlib import Path

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

# 修改 1. 接口用 Go 的 time.Time 序列化时间（RFC3339Nano，带 Z 或时区和最多 9 位小数），
# 旧批次配置里可能还是秒级数字；两种都按同一时刻处理，避免直接与 time.time() 比较时报 TypeError。
def timestamp(value):
    if isinstance(value,(int,float)):return float(value)
    text=str(value or '').strip()
    if not text:raise ValueError('批次时间字段为空')
    match=re.match(r'^(.*\.\d{6})\d+([+-]\d{2}:\d{2}|Z|z)$',text)
    if match:text=match.group(1)+match.group(2)
    if text[-1] in 'Zz':text=text[:-1]+'+00:00'
    return datetime.datetime.fromisoformat(text).timestamp()

class Busy(RuntimeError): pass

class SlotWaitTimeout(RuntimeError): pass

def merge_source(path,arguments):
    """只有真实未合并文件归为成员冲突，其它Git失败保留原始诊断。"""
    result=subprocess.run(['git','merge',*arguments],cwd=path,capture_output=True,text=True)
    if result.returncode==0:return True
    detail=(result.stderr+'\n'+result.stdout).strip()
    conflicts=subprocess.run(['git','diff','--name-only','--diff-filter=U'],cwd=path,capture_output=True,text=True)
    pending=subprocess.run(['git','rev-parse','--verify','--quiet','MERGE_HEAD'],cwd=path,capture_output=True,text=True)
    if pending.returncode==0:
        cleanup=subprocess.run(['git','merge','--abort'],cwd=path,capture_output=True,text=True)
        if cleanup.returncode:
            raise r.RecoveryRequired('Git合并失败：'+detail+'；清理失败：'+(cleanup.stderr or cleanup.stdout).strip())
    if conflicts.returncode:
        raise RuntimeError('Git合并失败：'+detail+'；无法核对冲突：'+conflicts.stderr.strip())
    if conflicts.stdout.strip():return False
    raise RuntimeError('Git合并失败：'+detail)

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
        receipt=self.api('delivery-queue/advance',dict(id=d['id'],revision=d['revision'],worker_token=d.get('worker_token') or '',action=action,source_commit=d['source_commit'],publication_generation=d.get('publication_generation',0),**extra))
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
            if not merge_source(path,['--no-edit','origin/personal-studio-upstream']):
                raise RuntimeError('与最新主分支存在合并冲突，请在原任务分支解决并重新提交审核')
        return path
    def own_candidate(self,d):
        c=r.candidate() if r.candidate_path().exists() else None
        if not c or c.get('delivery_id')!=d['id']:return None
        return c
    def candidate_url(self):
        # 组合候选地址取当前 int 的联调入口；成员按同一地址验收同一份固定候选。
        path=r.meta_path('int')
        return (r.read(path).get('frontend_url') or '') if path.exists() else ''
    def busy(self,d):
        c=r.candidate() if r.candidate_path().exists() else None
        return c and c.get('status') not in ('released','succeeded','failed') and c.get('delivery_id')!=d['id']
    def publication_path(self,d):
        import uuid
        return r.RUN/'publication-leases'/(str(uuid.UUID(d['id']))+'.json')
    def publication(self,d,action='check'):
        if not hasattr(self,'_publication_lock'):self._publication_lock=threading.RLock()
        with self._publication_lock:return self.publication_owned(d,action)
    def publication_owned(self,d,action='check'):
        path=self.publication_path(d)
        lease=r.read(path) if path.exists() else dict(delivery_id=d['id'],worker_token=d['worker_token'])
        if lease.get('worker_token')!=d['worker_token']:raise r.RecoveryRequired('发布执行身份变化，需要核对原占用')
        if 'generation' not in lease:
            action='claim';r.write(path,lease,True)
        try:
            # 1. 接管过期占用要给出恢复依据：服务端只在原交付已不在活动阶段时放行。
            evidence='发布器独占锁已取得、原占用已过期、原持有交付不在活动阶段' if action=='claim' else ''
            result=self.api('delivery-queue/publication',dict(lease,action=action,recovery_evidence=evidence,snapshot=dict(source_commit=d['source_commit'])))
        except urllib.error.HTTPError as e:
            if e.code!=409:raise
            current=self.api('delivery-queue/publication')
            if action=='claim' and current.get('delivery_id')==d['id']:
                # 领取响应丢失时核对原凭据，不创建新占用或改变代次。
                lease['generation']=current['generation'];action='check'
                if not current.get('expired'):
                    result=self.api('delivery-queue/publication',dict(lease,action='check'))
                    r.write(path,result,True);d['publication_generation']=result['generation'];return result
            if action in ('check','heartbeat') and current.get('delivery_id')==d['id'] and current.get('generation')==lease.get('generation') and current.get('expired'):
                with r.lock('publisher'):
                    c=self.own_candidate(d)
                    if c and c.get('publisher_pid')!=os.getpid() and r.exists(c.get('publisher_pid',0)):
                        raise r.RecoveryRequired('旧发布进程仍存活，不能交接')
                    request=r.request_path(c['id']) if c else None
                    if request and request.exists() and subprocess.run(['systemctl','is-active','--quiet',r.read(request)['unit']]).returncode==0:
                        raise r.RecoveryRequired('生产切换进程仍在运行，不能交接')
                    r.check_process_ownership('int',r.read(r.meta_path('int')))
                    result=self.api('delivery-queue/publication',dict(lease,action='resume',recovery_evidence='publisher锁已独占、旧执行进程已退出、int进程与部署身份已核对'))
                    if c:
                        c['publication_lease']=result;r.save_candidate(c)
            else:raise Busy('最终发布占用尚不可用；保留原交付等待或恢复核对') from e
        r.write(path,result,True);d['publication_generation']=result['generation']
        return result
    @contextlib.contextmanager
    def publication_guard(self,d):
        lease=self.publication(d)
        stopped=threading.Event();failures=[]
        def renew():
            while not stopped.wait(20):
                try:self.publication(d,'heartbeat')
                except Exception as e:failures.append(str(e));return
        thread=threading.Thread(target=renew,daemon=True);thread.start()
        def fence():
            if failures:raise r.RecoveryRequired('发布占用续期失败：'+failures[0])
            self.publication(d,'check')
        try:yield lease,fence
        finally:stopped.set();thread.join(timeout=21)
    def finish_publication(self,d):
        path=self.publication_path(d)
        if path.exists():
            lease=r.read(path);current=self.api('delivery-queue/publication')
            if (current.get('state')=='idle' and current.get('generation',0)>=lease.get('generation',1)) or (current.get('delivery_id')!=d['id'] and current.get('generation',0)>lease.get('generation',0)):
                path.unlink();return
            self.publication(d,'check');self.publication(d,'release');path.unlink()
    def active_batch_members(self):
        # 修改 11. 活动批次里的成员仍停在 approved，单据本身不能证明发布已结束；
        # 回收判定必须同时看批次归属，否则会释放正在验收的组合候选和发布占用。
        if not self.batch_config():
            local=r.candidate() if r.candidate_path().exists() else {}
            if not local.get('batch_id') or local.get('status') in ('released','succeeded','failed'):return set()
        state=self.batch_call()
        batch=state.get('batch') or {}
        if batch.get('status') not in ('collecting','frozen','preparing','validating','publishing'):return set()
        return {m['delivery_id'] for m in state.get('members') or [] if m.get('result','pending')=='pending'}

    def recover_finished_publications(self,rows):
        # 业务阶段提交和本地清理之间可能重启；持久占用必须在领取下一项前收尾。
        records={d['id']:d for d in rows}
        batch_members=self.active_batch_members()
        for path in (r.RUN/'publication-leases').glob('*.json'):
            identity=path.stem
            if identity not in records:
                records.update({d['id']:d for d in self.api('delivery-queue?current='+identity)})
            d=records.get(identity)
            if not d:raise r.RecoveryRequired('发布占用对应的交付记录缺失')
            finished=d['phase'] in ('published','cancelled','needs_changes') or (d['phase']=='approved' and identity not in batch_members) or (d['phase']=='awaiting_review' and not d.get('plan_snapshot',{}).get('authorization_pending'))
            if not finished:continue
            c=self.own_candidate(d)
            if c:
                if c['status'] not in ('verifying','accepted','released','succeeded','failed'):
                    raise r.RecoveryRequired('交付已结束但环境切换尚未确认，保留发布占用')
                request=r.request_path(c['id'])
                if request.exists() and r.read(request).get('status') in ('queued','publishing'):
                    raise r.RecoveryRequired('生产切换仍在执行，保留发布占用')
                for env in ('int','prod'):r.check_process_ownership(env,r.read(r.meta_path(env)))
                if c['status'] in ('verifying','accepted'):r.release_candidate(c['id'])
            self.finish_publication(d)
    def prepare(self,d):
        with self.publication_guard(d) as (lease,fence):
            return self.prepare_owned(d,lease,fence)
    def prepare_owned(self,d,lease,fence):
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
            fence()
            path=self.source(d,integrate=True)
            snapshot=dict(source_commit=d['source_commit'],base_commit=r.output(['git','rev-parse','origin/personal-studio-upstream'],cwd=path),production_commit=r.read(r.meta_path('prod'))['commit'])
            self.api('delivery-queue/publication',dict(lease,action='freeze',snapshot=snapshot))
            previous=r.read(r.candidate_record_path(d['candidate_id'])) if d.get('candidate_id') and r.candidate_record_path(d['candidate_id']).exists() else None
            plan=r.validation_plan(path,previous)
            # 只有同提交、同产物且所有验证输入一致时复用构建。
            archive=Path(previous.get('accepted_artifact','')) if previous else None
            if previous and plan['mode']=='reuse' and r.output(['git','rev-parse','HEAD'],cwd=path)==previous['commit'] and archive and (archive/'release.json').exists():
                manifest=r.read(archive/'release.json')
                if r.checksums(archive)!=manifest['files']:raise r.RecoveryRequired('已验收产物归档被修改')
                fence();target=r.idle_slot('int');r.copy_tree(archive,target);r.publish('int',target)
                c=dict(previous,status='accepted',publication_lease=lease,source=str(path),branch=r.output(['git','symbolic-ref','HEAD'],cwd=path),deployed_at=r.read(r.meta_path('int'))['deployed_at'])
                r.save_candidate(c)
            else:
                try:c=r.prepare_int(path,delivery=dict(delivery_id=d['id'],delivery_env=self.env,approved_source=d['source_commit'],publication_lease=lease),fence=fence)
                except RuntimeError as e:
                    if '占用' in str(e) or '构建或发布' in str(e):raise Busy(str(e)) from e
                    raise
            c['validation_plan']=plan;r.save_candidate(c)
        elif c['status'] in ('verifying','accepted'):
            r.validate_candidate(c)
        else:raise r.RecoveryRequired('交付构建或恢复未完成，请先检查原发布记录')
        self.advance(d,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path('int'))['frontend_url'],candidate_identity=c.get('validation_identity',{}),validation_plan=c.get('validation_plan',{}),candidate_commit=c['commit'])
        if not d.get('acceptance_submitted'):
            recipe=Path(c['source'])/'scripts/run-delivery-acceptance.py'
            if recipe.exists():
                spec=importlib.util.spec_from_file_location('delivery_acceptance',recipe)
                acceptance=importlib.util.module_from_spec(spec);spec.loader.exec_module(acceptance)
                evidence=acceptance.execute(c['id'],'int')
                if evidence:
                    self.advance(d,'tested',evidence=evidence['checks']);d['acceptance_submitted']=True
            if not d.get('acceptance_submitted'):self.advance(d,'notify')
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
            self.finish_publication(d)
    def publish(self,d):
        if self.env!='prod':raise RuntimeError('联调交付队列不能更新生产')
        c=self.own_candidate(d)
        if not c or c['status']!='accepted':raise RuntimeError('缺少已验收发布候选')
        path=Path(c['source']);tested=c['commit']
        r.run(['git','fetch','origin','personal-studio-upstream'],cwd=r.ROOT)
        if subprocess.run(['git','merge-base','--is-ancestor','origin/personal-studio-upstream',tested],cwd=r.ROOT).returncode:
            self.advance(d,'fail',error='队列外主分支发生变化，候选与证据已保留，需要核对后重排');r.release_candidate(c['id']);self.finish_publication(d);return
        self.publication(d,'check')
        if d.get('phase')!='publishing':self.advance(d,'publishing')
        # 修改 15. 单项发布同样先复核再 push：进入 publishing 后仍可能被撤权，顺序不能反。
        r.check_delivery_approval(c)
        self.publication(d,'check')
        r.run(['git','push','origin',tested+':refs/heads/personal-studio-upstream'],cwd=path)
        if r.output(['git','ls-remote','origin','refs/heads/personal-studio-upstream'],cwd=r.ROOT).split()[0]!=tested:raise RuntimeError('远端主分支核对失败')
        r.request_release(c['id'])
    def pool_enabled(self):
        path=r.RUN/'verification-pool.json'
        return path.exists() and r.read(path).get('enabled') is True and r.read(path).get('controller_environment')==self.env

    def current_seed(self):
        return r.current_seed()

    def seed_matches(self,name):
        current=self.current_seed()
        if not current:return True
        try:record=self.api('delivery-queue/slots/seed?name='+name)
        except urllib.error.HTTPError as e:
            if e.code!=404:raise
            return False
        # 修改 9. 单槽位查询返回 verified（列表接口才是 seed_verified）；按实际接口响应判断，
        # 否则有 current seed 时每次领取都会被误判成版本不一致而隔离。
        return record.get('seed_id')==current.get('seed_id') and record.get('seed_sha256')==current.get('dump_sha256') and record.get('verified') is True

    def ensure_seed(self):
        if not self.current_seed():return
        try:
            # 恢复、登记和恢复接单共用外部维护锁；另一交付等待下一轮，不能重置同一槽位。
            with r.lock('verification-seed-maintenance'):
                return self.ensure_seed_locked()
        except BlockingIOError:
            raise Busy('其他交付正在恢复验收种子，等待下一轮') from None

    def ensure_seed_locked(self):
        # 修改 1. 换任务前先核对槽位数据版本：与当前种子不一致就按单槽位维护恢复，
        # 复用上一任务的残留数据比多等一次恢复更容易给出错误结论。
        current=self.current_seed()
        if not current:return
        for item in sorted(self.api('delivery-queue/slots'),key=lambda slot:slot['name']):
            if not item['enabled'] or item['state']!='idle':continue
            if item.get('seed_id')==current.get('seed_id') and item.get('seed_sha256')==current.get('dump_sha256') and item.get('seed_verified') is True:return
            script=Path(__file__).with_name('verification-seed.py')
            completed=subprocess.run([sys.executable,str(script),'ensure','--environment',item['name'],
                '--postgres-config',str(r.RUN/'verification-postgres.json')],capture_output=True,text=True)
            if completed.returncode:
                detail=(completed.stderr or completed.stdout).strip()[-1200:]
                if 'HTTP Error 409' in detail:
                    raise Busy('槽位维护状态已变化，等待下一轮')
                raise r.RecoveryRequired('种子恢复失败，槽位保持维护：'+(completed.stderr or completed.stdout).strip()[-1200:])
            return
        raise Busy('没有可用的干净验收槽位，等待其他验收结束')

    def pool_job_path(self,delivery):
        # 编号来自服务端交付记录，不接收用户指定的文件路径。
        import uuid
        return r.RUN/'verification-jobs'/(str(uuid.UUID(delivery))+'.json')

    def start_pool_unit(self,delivery):
        unit='multica-verify-'+delivery+'.service'
        command=[sys.executable,str(Path(__file__).resolve()),'--env',self.env,'--pool-delivery',delivery]
        home=pwd.getpwuid(os.getuid()).pw_dir
        properties=dict(line.split('=',1) for line in r.output(['systemctl','show',unit,
            '--property=LoadState,ActiveState,WorkingDirectory,Type,KillMode','--no-pager']).splitlines() if '=' in line)
        if properties.get('LoadState')!='not-found':
            # systemd保留仍有持久服务的inactive单元；只续启参数完全匹配的原单元。
            object_path=json.loads(r.output(['busctl','--json=short','call','org.freedesktop.systemd1',
                '/org/freedesktop/systemd1','org.freedesktop.systemd1.Manager','GetUnit','s',unit]))['data'][0]
            starts=json.loads(r.output(['busctl','--json=short','get-property','org.freedesktop.systemd1',
                object_path,'org.freedesktop.systemd1.Service','ExecStart']))['data']
            environment=json.loads(r.output(['busctl','--json=short','get-property','org.freedesktop.systemd1',
                object_path,'org.freedesktop.systemd1.Service','Environment']))['data']
            environment=dict(item.split('=',1) for item in environment if '=' in item)
            if (properties.get('LoadState')!='loaded' or properties.get('ActiveState') not in ('inactive','failed','active','activating','deactivating')
                    or properties.get('WorkingDirectory')!=str(r.ROOT) or properties.get('Type')!='exec'
                    or properties.get('KillMode')!='process' or len(starts)!=1
                    or starts[0][0]!=sys.executable or starts[0][1]!=command or starts[0][2] is not False
                    or environment.get('MULTICA_RELEASE_ROOT')!=str(r.ROOT) or environment.get('HOME')!=home):
                raise r.RecoveryRequired('已有验收接力单元身份不匹配，拒绝替换：'+unit)
            if properties.get('ActiveState') not in ('active','activating','deactivating'):r.run(['systemctl','start',unit])
            return
        # 槽位服务跨交付存活，由canonical PID/profile管理停启；短命接力单元退出不能杀服务。
        r.run(['systemd-run','--quiet','--collect','--unit',unit,'--property=Type=exec',
               '--property=KillMode=process','--working-directory',r.ROOT,
               '--setenv=MULTICA_RELEASE_ROOT='+str(r.ROOT),'--setenv=HOME='+home,*command])

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
            self.start_pool_unit(d['id'])
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
                    # 1. 等槽位超时退出后，下一轮直接复用已构建产物，避免每次空等都重新构建。
                    reuse=job.get('stage')=='waiting_slot' and job.get('source_commit')==d['source_commit'] and bool(job.get('artifact')) and Path(job['artifact']).exists()
                    if reuse:
                        artifact=Path(job['artifact'])
                        source=Path(job.get('source') or r.read(artifact/'release.json')['source_directory'])
                        save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'])
                    else:
                        source=self.source(d,integrate=True)
                        save('building',source_commit=d['source_commit'])
                        artifact=r.build_verification(source,delivery)
                        save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'])
                    # 等槽位有上限；种子维护和全忙均正常排队，下一轮复用现有构建。
                    deadline=time.monotonic()+600
                    while lease is None:
                        d=refresh()
                        if not d or d['phase']!='testing':return
                        try:self.ensure_seed()
                        except Busy:
                            stopped.set();return
                        try:
                            lease=slot(dict(delivery_id=delivery,worker_token=d['worker_token'],source_commit=d['source_commit']),'claim')
                        except urllib.error.HTTPError as e:
                            detail=json.loads(e.read().decode()) if e.code==409 else {}
                            if detail.get('error')!='没有可用验收槽位':raise
                            if time.monotonic()>=deadline:
                                # 超时只记录现场，进度回执失败不能把交付判成故障。
                                try:save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'],timeout=True)
                                except Exception:pass
                                raise SlotWaitTimeout('等待验收槽位超过 10 分钟，交给下一轮续接')
                            time.sleep(5)
                    # 修改 2. 恢复与领取之间有并发，领到之后还要核对一次数据版本，不能只在领取前判断。
                    if not self.seed_matches(lease['name']):
                        slot(lease,'quarantine','槽位数据版本不是当前种子')
                        raise r.RecoveryRequired('槽位种子与当前版本不一致，已隔离并等待外部恢复')
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
                        self.advance(d,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path(lease['name']))['frontend_url'],candidate_identity=c.get('validation_identity',{}),candidate_commit=c.get('commit',''))
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
            except SlotWaitTimeout:
                # 3. 等槽位超时不是交付失败：保留构建产物与现场，交给下一轮重新排队。
                stopped.set();return
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

    # 1. 选择性组合发布：发布器空闲时开一个收集窗口，把已验收且仍有授权的交付固定成一批，
    # 一个批次只构建、验收和发布一次，成员各自回填结果。
    def batch_config(self):
        path=r.RUN/'delivery-batch.json'
        if not path.exists():return None
        config=r.read(path)
        if config.get('enabled') is not True or config.get('controller_environment')!=self.env:return None
        return config

    def batch_window(self):
        return int((self.batch_config() or {}).get('window_seconds') or 60)

    def batch_call(self,body=None):
        return self.api('delivery-queue/batches',body)

    def publication_owner(self):
        current=self.api('delivery-queue/publication')
        return None if current.get('state')=='idle' else current

    def batch_row(self,rows,identity):
        return next((d for d in rows if d['id']==identity),None)

    def batch_source(self,batch,members):
        """最新主分支 + 各成员固定提交；冲突时回填到具体成员，不半发布。"""
        r.run(['git','-C',r.ROOT,'fetch','origin','refs/heads/personal-studio-upstream'])
        base=batch.get('base_commit') or r.output(['git','-C',r.ROOT,'rev-parse','origin/personal-studio-upstream'])
        path=r.RUN/'delivery-sources'/('batch-'+batch['id']);branch='feature/delivery/batch-'+batch['id']
        if path.exists():
            if r.output(['git','status','--porcelain','--untracked-files=all'],cwd=path):raise RuntimeError('批次构建目录存在未保存内容')
            r.run(['git','-C',r.ROOT,'worktree','remove','--force',path])
        r.run(['git','-C',r.ROOT,'worktree','add','-B',branch,path,base])
        for member in members:
            lookup=subprocess.run(['git','-C',r.ROOT,'cat-file','-e',member['source_commit']+'^{commit}'],capture_output=True,text=True)
            if lookup.returncode:raise RuntimeError('Git提交读取失败：'+lookup.stderr.strip())
            if not merge_source(path,['--no-ff','--no-edit',member['source_commit']]):
                return path,base,None,member
        return path,base,r.output(['git','rev-parse','HEAD'],cwd=path),None

    def pending_members(self,state):
        return [m for m in (state.get('members') or []) if m.get('result','pending')=='pending']

    def tick_batches(self,rows):
        if not self.batch_config():return False
        state=self.batch_call();batch=state.get('batch');members=self.pending_members(state)
        if not batch:
            if not any(d['phase']=='approved' for d in rows):return False
            if self.publication_owner():return True
            self.batch_call(dict(action='open',window_seconds=self.batch_window()))
            return True
        token=batch.get('worker_token') or ''
        if batch['status']=='collecting':
            if time.time()<timestamp(batch['collect_until']):return True
            state=self.batch_call(dict(action='freeze',batch_id=batch['id']))
            batch=state['batch'];members=self.pending_members(state)
            if batch['status'] in ('cancelled','needs_changes'):return True
        if batch['status']=='frozen':return self.batch_prepare(batch,members,rows)
        # 修改 4. 进程在准备阶段退出会留下 preparing；这里接续原批次，不新建批次也不让状态永久卡住。
        if batch['status']=='preparing':return self.batch_resume(batch,members,rows)
        if batch['status']=='validating':return self.batch_validate(batch,members,rows)
        if batch['status']=='publishing':return self.batch_check_release(batch,rows)
        return True

    def batch_prepare(self,batch,members,rows):
        representative=members[0]
        delivery=self.batch_row(rows,representative['delivery_id'])
        if not delivery:return True
        with self.publication_guard(delivery) as (lease,fence):
            try:
                path,base,commit,conflict=self.batch_source(batch,members)
            except Exception as error:
                self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error=str(error)[:1200]))
                self.finish_publication(delivery)
                return True
            if conflict:
                detail='提交 %s 无法合并到最新主分支，请在原任务分支解决后重新交付'%conflict['source_commit']
                self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error=detail,conflict_delivery_id=conflict['delivery_id']))
                self.finish_publication(delivery)
                return True
            # 修改 5. 构建名单以准备阶段返回的 pending 成员为准：名单一变说明代表可能已被撤权，
            # 旧代表持有的 delivery/租约不再代表本批，整批退出释放，下一批由剩余成员重新领取。
            state=self.batch_call(dict(action='preparing',batch_id=batch['id'],worker_token=batch['worker_token'],base_commit=base))
            if state['batch']['status']!='preparing':
                self.finish_publication(delivery);return True
            active=self.pending_members(state)
            if [m['delivery_id'] for m in active]!=[m['delivery_id'] for m in members]:
                self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error='批次成员授权已变化，按剩余有效成员重新组批'))
                self.finish_publication(delivery)
                return True
            fence()
            try:
                record=r.prepare_int(path,delivery=dict(delivery_id=delivery['id'],delivery_env=self.env,
                    approved_source=delivery['source_commit'],publication_lease=lease,batch_id=batch['id'],
                    batch_members=[dict(delivery_id=m['delivery_id'],source_commit=m['source_commit']) for m in members]),fence=fence)
            except r.RecoveryRequired:
                # 环境恢复尚未完成时保留批次与租约，不能让下一批覆盖现场。
                raise
            except Exception as error:
                # 修改 6. 构建异常必须结束整批：成员保留授权等下一批，不能把批次留在 preparing。
                try:self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error='组合构建失败：'+str(error)[:1000]))
                finally:self.finish_publication(delivery)
                raise
            state=self.batch_call(dict(action='validating',batch_id=batch['id'],worker_token=batch['worker_token'],
                candidate_id=record['id'],candidate_commit=commit,candidate_url=self.candidate_url()))
            # 修改 7. 候选固定后成员撤权时服务端已让批次安全停下：丢弃这份候选，不通知成员进入发布。
            if state['batch']['status']!='validating':
                r.release_candidate(record['id']);self.finish_publication(delivery);return True
        self.notify_batch_ready(members)
        return True

    def notify_batch_ready(self,members):
        rows=self.api('delivery-queue')
        for member in members:
            row=self.batch_row(rows,member['delivery_id'])
            if not row:continue
            try:self.advance(row,'notify-result',stage='batch_ready')
            except urllib.error.HTTPError as error:
                if error.code!=409:raise

    def batch_resume(self,batch,members,rows):
        # 修改 8. 准备阶段重启：候选已部署就接续验收，构建中断且 int 未切换就重来，明确失败则结束整批。
        candidate=r.candidate() if r.candidate_path().exists() else None
        if candidate and candidate.get('batch_id')!=batch['id']:candidate=None
        if candidate and candidate['status'] in ('verifying','accepted'):
            state=self.batch_call(dict(action='validating',batch_id=batch['id'],worker_token=batch['worker_token'],
                candidate_id=candidate['id'],candidate_commit=candidate['commit'],candidate_url=self.candidate_url()))
            if state['batch']['status']!='validating':
                r.release_candidate(candidate['id']);self.finish_publication(self.batch_row(rows,candidate['delivery_id']));return True
            self.notify_batch_ready(members)
            return True
        if candidate:
            if candidate['status']=='recovery_required':
                raise r.RecoveryRequired('组合环境恢复未完成：'+candidate.get('error',''))
            if candidate['status']=='failed':
                self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],
                    error=('组合构建未完成：'+(candidate.get('error') or candidate['status']))[:1200]))
                representative=self.batch_row(rows,candidate.get('delivery_id',''))
                if representative:self.finish_publication(representative)
                if candidate['status']=='failed':r.release_candidate(candidate['id'])
                return True
            if candidate['status']=='building':
                if candidate.get('publisher_pid') not in (None,os.getpid()) and r.exists(candidate['publisher_pid']):
                    raise Busy('组合构建进程仍在运行')
                # 半成品只有在 int 仍是构建前版本时才能丢弃重来，否则需要核对现场。
                live=r.read(r.meta_path('int'))
                if live['release_id']!=candidate.get('previous_int_release'):
                    raise r.RecoveryRequired('组合构建中断后 int 已切换，需要核对现场后再续接')
                r.save_candidate(dict(candidate,status='failed',error='组合构建中断，按原成员重新构建'))
        if not members:return True
        return self.batch_prepare(batch,members,rows)

    def batch_acceptance(self,candidate,members,rows):
        row=self.batch_row(rows,candidate.get('delivery_id',''))
        if (row and row.get('candidate_id')==candidate['id'] and row.get('acceptance_submitted')
                and row.get('plan_snapshot',{}).get('candidate_commit')==candidate['commit']):
            return row.get('evidence') or None
        return None

    def batch_validate(self,batch,members,rows):
        representative=self.batch_row(rows,members[0]['delivery_id']) if members else None
        candidate=r.candidate() if r.candidate_path().exists() else None
        if not candidate or candidate.get('batch_id')!=batch['id']:
            self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error='组合候选记录缺失，需要核对 int 现场'))
            return True
        representative=self.batch_row(rows,candidate.get('delivery_id',''))
        if representative:self.publication(representative,'heartbeat')
        self.notify_batch_ready(members)
        if candidate['status']=='verifying':
            # 修改 13. 组合验收由批次成员在同一个固定候选上提交，证据与候选一起归档后才进入发布。
            checks=self.batch_acceptance(candidate,members,rows)
            if not checks:return True
            evidence=r.RUN/'delivery-evidence'/(candidate['id']+'.json')
            r.write(evidence,dict(candidate=candidate['id'],summary='组合候选实际功能验收',checks=checks),True)
            r.accept(candidate['id'],evidence)
            candidate=r.candidate()
        if candidate['status']!='accepted':return True
        state=self.batch_call(dict(action='publishing',batch_id=batch['id'],worker_token=batch['worker_token']))
        if state['batch']['status']!='publishing':
            self.finish_publication(representative);r.release_candidate(candidate['id']);return True
        return self.batch_release(batch,candidate,representative,state['members'])

    def batch_release(self,batch,candidate,delivery,members):
        path=Path(candidate['source']);tested=candidate['commit']
        r.run(['git','fetch','origin','personal-studio-upstream'],cwd=r.ROOT)
        if subprocess.run(['git','merge-base','--is-ancestor','origin/personal-studio-upstream',tested],cwd=r.ROOT).returncode:
            self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error='队列外主分支发生变化，候选与成员证据已保留，需要核对后重排'))
            self.finish_publication(delivery);r.release_candidate(candidate['id']);return True
        self.publication(delivery,'check')
        # 修改 14. 撤权复核必须在 push 之前：先推主分支再验证会让未批准的成员代码进入 main。
        if [(m['delivery_id'],m['source_commit']) for m in members if m.get('result','pending')=='pending'] != [(m['delivery_id'],m['source_commit']) for m in candidate.get('batch_members',[])]:
            self.batch_call(dict(action='abort',batch_id=batch['id'],worker_token=batch['worker_token'],error='固定成员已变化，重新组批'))
            self.finish_publication(delivery);r.release_candidate(candidate['id']);return True
        try:r.check_delivery_approval(candidate)
        except Exception as error:
            self.batch_call(dict(action='abort',batch_id=batch['id'],worker_token=batch['worker_token'],error='推送前授权复核失败：'+str(error)[:1000]))
            self.finish_publication(delivery);r.release_candidate(candidate['id']);return True
        self.publication(delivery,'check')
        r.run(['git','push','origin',tested+':refs/heads/personal-studio-upstream'],cwd=path)
        if r.output(['git','ls-remote','origin','refs/heads/personal-studio-upstream'],cwd=r.ROOT).split()[0]!=tested:
            raise RuntimeError('远端主分支核对失败')
        r.request_release(candidate['id'])
        return True

    def batch_check_release(self,batch,rows):
        representative=self.batch_row(rows,(r.candidate().get('delivery_id') if r.candidate_path().exists() else ''))
        candidate=r.candidate() if r.candidate_path().exists() else None
        if not candidate or candidate.get('batch_id')!=batch['id']:
            return True
        if representative:self.publication(representative,'heartbeat')
        target=r.request_path(candidate['id'])
        if not target.exists():
            state=self.batch_call(dict(action='publishing',batch_id=batch['id'],worker_token=batch['worker_token']))
            if state['batch']['status']!='publishing':
                if representative:self.finish_publication(representative)
                r.release_candidate(candidate['id']);return True
            return self.batch_release(batch,candidate,representative,state['members'])
        result=r.read(target)
        if result['status'] in ('queued','publishing'):return True
        if result['status']=='succeeded':
            self.batch_call(dict(action='published',batch_id=batch['id'],worker_token=batch['worker_token'],candidate_commit=candidate['commit']))
        else:
            self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error=result.get('error','生产更新失败')[:1200]))
        if representative:self.finish_publication(representative)
        r.release_candidate(candidate['id'])
        return True

    def tick(self):
        # 固定 int/prod 只由生产控制队列调度，int 数据库不能另建同名占用抢写环境。
        if self.env!='prod':return
        r.recover_release_windows()
        with r.lock('delivery-worker-'+self.env):
            current=r.candidate() if r.candidate_path().exists() else None
            current_id=current.get('delivery_id') if current and current.get('status') not in ('released','succeeded','failed') else None
            rows=self.api('delivery-queue'+('?current='+current_id if current_id else ''))
            self.recover_finished_publications(rows)
            if self.pool_enabled():
                managed=self.pool_dispatch(rows,current_id)
            else:
                managed=self.pool_dispatch(rows,current_id,allow_new=False)
            rows=[d for d in rows if d['id'] not in managed]
            # 2. 启用组批后，已批准交付统一由批次发布；单项流程不再单独领取，避免同一任务被发布两次。
            if self.batch_config():
                try:
                    if self.tick_batches(rows):return
                except Busy:return
                rows=[d for d in rows if d['phase']!='approved']
            owner=next((d for d in rows if d['id']==current_id),None)
            if current and owner and (owner['phase'] in ('cancelled','needs_changes') or (owner['phase']=='awaiting_review' and not owner.get('plan_snapshot',{}).get('authorization_pending'))) and current['status'] in ('verifying','accepted'):
                r.release_candidate(current['id'])
            for d in rows:
                if d['phase'] not in ('cancelled','needs_changes'):
                    try:self.pin(d)
                    except Exception as e:self.advance(d,'fail',error='无法固定交付提交：'+str(e)[:1500]);return
            # 先完成已占用环境的交付，再按创建顺序处理排队项。
            # 4. 待复核的 awaiting_review 也由队列推进：服务端只在通知未送达、或已送达但负责人
            # 长时间未接续时返回该行，是否补发与次数上限由服务端判定，这里只负责触发一次检查。
            active=[d for d in rows if d['phase'] in ('testing','preparing_release','publishing','published','needs_changes','awaiting_review')]
            queued=[d for d in rows if d['phase'] in ('waiting_int','approved')]
            for d in active+queued:
                try:
                    phase=d['phase']
                    if phase in ('published','needs_changes'):
                        self.advance(d,'notify-result');return
                    if phase=='awaiting_review':
                        if self.publication_path(d).exists():self.publication(d,'heartbeat')
                        self.advance(d,'notify-authorization');return
                    if phase in ('waiting_int','approved'):
                        if self.busy(d):return
                        self.advance(d,'claim');self.prepare(d);return
                    if phase=='testing':
                        if self.publication_path(d).exists():self.publication(d,'heartbeat')
                        if d['acceptance_submitted']:self.accept(d)
                        elif not d['candidate_id']:self.prepare(d)
                        else:self.advance(d,'notify')
                        return
                    if phase in ('preparing_release','publishing'):
                        self.publication(d,'heartbeat')
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
                        if result['status']=='succeeded':
                            self.advance(d,'published');self.finish_publication(d)
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
                    if c and c['status'] in ('verifying','accepted'):r.release_candidate(c['id']);self.finish_publication(d)
                    elif not c:self.finish_publication(d)
                    return

def install(env):
    r.require_control_directory()
    # 修改 3. 种子恢复由接力器调用，操作脚本必须与接力器同版本安装；沿用主工作区的旧脚本会让恢复和清理规则不同步。
    names=('issue-delivery-worker.py','release-environments.py','release-maintenance.py','verification-environments.py',
           'verification-seed.py','verification-seed-copy.mjs','verification-scrub.json','lib/verification-scrub.mjs') + r.BROWSER_OPERATIONS
    digest=r.hashlib.sha256(b''.join((Path(__file__).parent/name).read_bytes() for name in names)).hexdigest()[:16]
    directory=r.RUN/'publisher'/('delivery-'+env+'-'+digest)
    directory.mkdir(parents=True,exist_ok=True)
    for name in names:
        target=directory/name
        target.parent.mkdir(parents=True,exist_ok=True)
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
