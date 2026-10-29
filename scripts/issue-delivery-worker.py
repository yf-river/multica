#!/usr/bin/env python3
"""外部交付接力：数据库保存审核真源，复用固定发布器执行联调和生产更新。"""
import io, hashlib, contextlib, argparse, datetime, importlib.util, json, os, pwd, re, subprocess, sys, threading, time, urllib.request, urllib.error
from pathlib import Path

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
spec=importlib.util.spec_from_file_location('verification_dev',Path(__file__).with_name('verification-dev.py'))
dev=importlib.util.module_from_spec(spec);spec.loader.exec_module(dev)
spec=importlib.util.spec_from_file_location('verification_seed',Path(__file__).with_name('verification-seed.py'))
seed=importlib.util.module_from_spec(spec);spec.loader.exec_module(seed)
seed.r=r
spec=importlib.util.spec_from_file_location('integration_data',Path(__file__).with_name('integration-data.py'))
integration=importlib.util.module_from_spec(spec);spec.loader.exec_module(integration)

# 修改 1. 接口用 Go 的 time.Time 序列化时间（RFC3339Nano，带 Z 或时区和最多 9 位小数），
# 旧批次配置里可能还是秒级数字；两种都按同一时刻处理，避免直接与 time.time() 比较时报 TypeError。
# 在 int 等待验收的最长时间；超过即自动释放发布车道，避免别的发布被无限阻塞。
TESTING_STALE_SECONDS=int(os.environ.get('MULTICA_TESTING_STALE_SECONDS','900'))
ENVIRONMENT_BRANCHES=tuple('refs/heads/multica-env/'+name for name in ('prod','int','verify-1','verify-2','verify-3'))


def sync_environment_refs(repo, commit, fence):
    """将已发布提交快进到环境基线，不修改任何已检出工作树。"""
    import uuid
    if not re.fullmatch(r'[0-9a-f]{40}', commit or ''):
        raise r.RecoveryRequired('环境同步缺少完整生产提交')
    main='refs/heads/personal-studio-upstream'
    def git(args, timeout=60):
        try:
            result=subprocess.run(['git',*args],cwd=repo,capture_output=True,text=True,timeout=timeout)
        except subprocess.TimeoutExpired as error:
            raise r.RecoveryRequired('环境分支 Git 操作超时：'+args[0]) from error
        if result.returncode:
            raise r.RecoveryRequired('环境分支 Git 操作失败：'+args[0]+'；'+r.redact_failure(result.stderr.strip())[:600])
        return result.stdout.strip()
    def remote():
        raw=git(['ls-remote','--heads','origin',main,*ENVIRONMENT_BRANCHES])
        refs={}
        for line in raw.splitlines():
            fields=line.split()
            if len(fields)!=2 or not re.fullmatch(r'[0-9a-f]{40}',fields[0]) or fields[1] not in (main,*ENVIRONMENT_BRANCHES) or fields[1] in refs:
                raise r.RecoveryRequired('远端环境分支响应无效')
            refs[fields[1]]=fields[0]
        return refs
    fence();git(['cat-file','-e',commit+'^{commit}']);before=remote()
    if before.get(main)!=commit:
        raise r.RecoveryRequired('主分支与已发布提交不同，不能同步环境基线')
    temporary=[]
    try:
        for branch in ENVIRONMENT_BRANCHES:
            old=before.get(branch)
            if not old or old==commit:continue
            local='refs/multica-release-sync/'+uuid.uuid4().hex
            temporary.append(local)
            git(['fetch','--no-tags','origin',branch+':'+local],timeout=120)
            actual=git(['rev-parse',local])
            if actual!=old:
                raise r.RecoveryRequired('环境分支在同步前发生变化，请重新核对')
            if subprocess.run(['git','merge-base','--is-ancestor',old,commit],cwd=repo,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode:
                raise r.RecoveryRequired('环境分支包含未发布的独立提交：'+branch)
        fence()
        updates=[commit+':'+branch for branch in ENVIRONMENT_BRANCHES if before.get(branch)!=commit]
        if updates:git(['push','--atomic','origin',*updates],timeout=120)
        fence();after=remote()
        if after.get(main)!=commit or any(after.get(branch)!=commit for branch in ENVIRONMENT_BRANCHES):
            raise r.RecoveryRequired('环境分支同步后与生产提交不一致')
        return {branch:after[branch] for branch in ENVIRONMENT_BRANCHES}
    finally:
        for ref in temporary:
            git(['update-ref','-d',ref])

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

# Modification 1. Hash the complete validated inventory canonically; keep audit bodies bounded independently of artifact size.
def repair_detail(artifact, manifest, previous_release, compatibility):
    canonical=lambda value:json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode('utf-8')
    report=compatibility.encode('utf-8')
    reference=Path(artifact).with_name(Path(artifact).name+'-repair-compatibility.txt')
    reference.write_bytes(report)
    detail=dict(artifact=str(artifact),manifest_ref=str(Path(artifact)/'release.json'),
                candidate_id=manifest.get('id'),source_commit=manifest['source_commit'],
                source_digest=manifest.get('source_digest'),previous_release=previous_release,
                files_count=len(manifest['files']),files_sha256=hashlib.sha256(canonical(manifest['files'])).hexdigest(),
                canonical='UTF-8 JSON; sort_keys=true; separators=(comma,colon); ensure_ascii=false',
                full_inventory_verified=True,compatibility=dict(ref=str(reference),sha256=hashlib.sha256(report).hexdigest(),
                bytes=len(report),summary=compatibility[:256]))
    return json.dumps(detail,ensure_ascii=False,separators=(',',':'))

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
        # Modification 2. Preserve HTTPError semantics and its readable body while recording bounded, credential-free diagnostics.
        if path=='delivery-queue/slots/advance' and len(req.data or b'')>8192:
            raise r.RecoveryRequired('slot request exceeds 8192-byte contract')
        try:
            with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=30) as response:return json.load(response)
        except urllib.error.HTTPError as error:
            raw=error.read(4096)
            clean=raw.decode('utf-8',errors='replace').replace(self.profile['token'],'[redacted]')
            clean=re.sub(r'(?i)("(?:authorization|[a-z_]*token|password|secret|credentials)"\s*:\s*)"(?:\\.|[^"\\])*"',r'\1"[redacted]"',clean)
            clean=r.redact_failure(clean)
            diagnostic=dict(status=error.code,endpoint=path.split('?')[0],body=clean,
                            failure_stage=getattr(self,'failure_stage','api'),journal_unit='multica-delivery-'+self.env)
            target=r.RUN/'publisher'/('http-failure-'+self.env+'-'+str(os.getpid())+'.json')
            target.parent.mkdir(parents=True,exist_ok=True)
            diagnostic['log_path']=str(target)
            r.write(target,diagnostic,True)
            message='HTTP %s: %s; stage=%s; log=%s'%(error.code,clean,diagnostic['failure_stage'],target)
            raise urllib.error.HTTPError(error.url,error.code,message,error.headers,io.BytesIO(raw)) from None
    def advance(self,d,action,**extra):
        receipt=self.api('delivery-queue/advance',dict(id=d['id'],revision=d['revision'],worker_token=d.get('worker_token') or '',action=action,source_commit=d['source_commit'],publication_generation=d.get('publication_generation',0),**extra))
        d.update(receipt)
        return d
    def verification_pending(self,d,notify=True):
        # 外部执行者依靠自己的归属心跳续租；发布器不替它续租，也不另派验收模型。
        if (d.get('verification_owner') or {}).get('external') is True:return True
        attempt=d.get('verification_run') or {}
        if attempt.get('settled') and attempt.get('status') in ('failed','cancelled','blocked','completed'):
            result=self.advance(d,'fail',verification_run_id=attempt['id'],error=attempt.get('error') or '验收执行已结束，但没有提交通过证据')
            return result.get('verification_deferred') is True
        # 通知只在没有执行时补一次；验收轮询已经先发过，不能每轮都重发同一条通知。
        if not attempt and notify:self.advance(d,'notify')
        return True
    def pin(self,d):
        # 只从固定仓库恢复提交；任务不能通过请求指定任意本机目录。
        if subprocess.run(['git','-C',r.ROOT,'cat-file','-e',d['source_commit']+'^{commit}'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode:
            r.run(['git','-C',r.ROOT,'fetch','origin','refs/heads/'+d['source_branch']])
        r.run(['git','-C',r.ROOT,'cat-file','-e',d['source_commit']+'^{commit}'])
        r.run(['git','-C',r.ROOT,'update-ref','refs/multica/deliveries/'+d['id'],d['source_commit']])
    def source(self,d,integrate=False):
        if integrate:
            # 合并前检查实际服务进程的身份，避免构建结束才暴露 Git 配置问题。
            for identity in ('GIT_AUTHOR_IDENT','GIT_COMMITTER_IDENT'):
                if subprocess.run(['git','-C',r.ROOT,'var',identity],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode:
                    raise RuntimeError('发布预检失败：实际执行器缺少 Git 合并身份')
        self.pin(d)
        if not integrate and d.get('source_work_dir') and self.batch_project() and d.get('project_id')==self.batch_project():
            path=Path(d['source_work_dir']).resolve(strict=True)
            workspaces=Path(r.read(r.meta_path(self.env))['daemon_workspaces_root']).resolve(strict=True)
            if not path.is_relative_to(workspaces) or path==workspaces:
                raise r.RecoveryRequired('任务目录不属于当前运行环境')
            common=Path(r.output(['git','rev-parse','--path-format=absolute','--git-common-dir'],cwd=path)).resolve()
            expected=Path(r.output(['git','-C',r.ROOT,'rev-parse','--path-format=absolute','--git-common-dir'])).resolve()
            if common!=expected or r.output(['git','symbolic-ref','--short','HEAD'],cwd=path)!=d['source_branch'] or r.output(['git','rev-parse','HEAD'],cwd=path)!=d['source_commit']:
                raise r.RecoveryRequired('任务目录的仓库、分支或提交不匹配')
            if r.output(['git','status','--porcelain','--untracked-files=all'],cwd=path):
                raise r.RecoveryRequired('任务目录存在未保存内容，无法绑定验收提交')
            return path
        # 2. A repaired source gets its own pinned checkout; never reuse the failed attempt's old HEAD.
        identity=d['id']+('-'+d['source_commit'] if any(k in (d.get('plan_snapshot') or {}) for k in ('verification_repair','batch_repair')) else '')
        path=r.RUN/'delivery-sources'/identity; branch='feature/delivery/'+identity
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
    def prebuild(self,source):
        recipe=Path(source)/'scripts/run-delivery-acceptance.py'
        if not recipe.exists():return
        spec=importlib.util.spec_from_file_location('delivery_acceptance_prebuild',recipe)
        acceptance=importlib.util.module_from_spec(spec);spec.loader.exec_module(acceptance)
        acceptance.execute_prebuild(source)

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
            self.ensure_int_data(d['id'],fence,mandatory=False)
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
                # 1. 构建要花几分钟，而静态与协调检查只要几秒；先跑预检，避免让不可能通过的候选进入构建。
                self.prebuild(path)
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
                    self.advance(d,'tested',evidence=evidence['checks'])
            if not d.get('acceptance_submitted'):self.verification_pending(d)
    def accept(self,d,release=False):
        c=self.own_candidate(d)
        if not c or c['id']!=d['candidate_id']:raise RuntimeError('联调版本已被替换，需要重新排队验证')
        if c['status']=='verifying':
            evidence=r.RUN/'delivery-evidence'/(d['id']+'.json')
            r.write(evidence,dict(candidate=c['id'],summary='任务负责人提交的实际功能验收',checks=d['evidence']),True)
            r.accept(c['id'],evidence)
        # Modification 1. Complete an int-only candidate without invoking any production gate.
        if release and d.get('plan_snapshot',{}).get('integration_only') is True and not d.get('approved_by'):
            self.advance(d,'integrated',candidate_id=c['id'])
            r.release_candidate(c['id']);self.finish_publication(d)
        elif release:self.advance(d,'validated')
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
        if not (path.exists() and r.read(path).get('enabled') is True and r.read(path).get('controller_environment')==self.env):return False
        current=r.candidate() if r.candidate_path().exists() else None
        # 维护窗口只暂停新验收，pool_dispatch 继续恢复和维护已有执行。
        return not (current and current.get('maintenance') and current['status'] not in ('released','failed','succeeded'))

    def current_seed(self):
        return r.current_seed()

    def ensure_int_data(self, batch, fence, *, mandatory):
        journal=r.RUN/'integration-data.json'
        if not mandatory and not journal.exists():return
        pointer=self.require_sql_seed()
        state=r.read(journal) if journal.exists() else {}
        if not state or (state.get('stage')=='ready' and state.get('batch')!=batch):
            integration.clear_environment(r,batch,fence)
        elif state.get('stage') in ('draining','stopping','cleaning'):
            if state.get('batch')!=batch:
                raise r.RecoveryRequired('上一个批次的联调清理尚未完成')
            integration.clear_environment(r,batch,fence)
        integration.restore_environment(r,seed,batch,pointer,fence)

    def require_sql_seed(self):
        current=self.current_seed()
        if not current or not current.get('seed_id'):
            raise r.RecoveryRequired('未配置已验证的 SQL 种子')
        path,manifest,artifact=seed.read_seed(current['seed_id'])
        if artifact['sha256']!=current.get('dump_sha256') or not seed.verified_sql_seed(path,manifest):
            raise r.RecoveryRequired('当前 SQL 种子身份不符或尚未验收')
        return current

    def seed_matches(self,name,expected=None):
        current=expected if expected is not None else self.current_seed()
        if not current:return True
        try:record=self.api('delivery-queue/slots/seed?name='+name)
        except urllib.error.HTTPError as e:
            if e.code!=404:raise
            return False
        # 修改 9. 单槽位查询返回 verified（列表接口才是 seed_verified）；按实际接口响应判断，
        # 否则有 current seed 时每次领取都会被误判成版本不一致而隔离。
        return record.get('seed_id')==current.get('seed_id') and record.get('seed_sha256')==current.get('seed_sha256',current.get('dump_sha256')) and record.get('verified') is True

    def lease_seed(self,name):
        if not self.current_seed():return None
        record=self.api('delivery-queue/slots/seed?name='+name)
        if not record.get('verified') or not record.get('seed_id') or not record.get('seed_sha256'):
            raise r.RecoveryRequired('验收槽位缺少已验证的种子身份')
        return record

    def ensure_seed(self,claim=None):
        if claim is None and not self.current_seed():return
        try:
            # 种子核对与领取共用维护锁，基线完成判定期间不能插入新的槽位领取。
            with r.lock('verification-seed-maintenance'):
                self.ensure_seed_locked()
                if claim is not None:return claim()
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

    def pool_unit_name(self,delivery,script=None):
        script=Path(script or __file__).resolve()
        installed=re.fullmatch('delivery-'+self.env+'-([0-9a-f]{16})',script.parent.name)
        version=installed[1] if installed else r.hashlib.sha256(str(script).encode()).hexdigest()[:16]
        return 'multica-verify-'+delivery+'-'+version+'.service'

    def check_idle_pool_services(self,unit,properties):
        if properties.get('MainPID')!='0':
            raise r.RecoveryRequired('验收接力单元仍有控制进程：'+unit)
        group=properties.get('ControlGroup','')
        if not group:return
        if group!='/system.slice/'+unit:
            raise r.RecoveryRequired('验收接力控制组身份不匹配：'+unit)
        directory=Path('/sys/fs/cgroup')/group.lstrip('/')
        pids=set()
        for file in directory.rglob('cgroup.procs'):
            pids.update(int(pid) for pid in file.read_text().split())
        anchors={}
        for env in r.VERIFICATION_ENVS:
            path=r.meta_path(env)
            if path.exists():
                metadata=r.read(path)
                for pid in metadata.get('pids',{}).values():anchors[int(pid)]=(env,metadata)
        checked={}
        for pid in pids:
            identity=r.process_identity(pid)
            if identity is None:continue
            parent=pid;seen=set()
            while parent not in anchors and parent>1 and parent not in seen:
                seen.add(parent)
                try:parent=int(Path('/proc',str(parent),'stat').read_text().split(') ')[1].split()[1])
                except FileNotFoundError:break
            if parent not in anchors:
                if r.process_identity(pid) is None:continue
                raise r.RecoveryRequired('旧验收接力控制组包含非槽位服务进程：'+str(pid))
            env,metadata=anchors[parent]
            before=r.process_identity(parent)
            if env not in checked:
                r.check_process_ownership(env,metadata)
                checked[env]={int(value):r.process_identity(value) for value in metadata['pids'].values()}
            if (before is None or checked[env].get(parent)!=before or r.process_identity(parent)!=before
                    or r.process_identity(pid)!=identity):
                raise r.RecoveryRequired('验收服务进程身份在交接期间变化：'+unit)

    def pool_units(self,delivery):
        self.pool_job_path(delivery)
        legacy='multica-verify-'+delivery+'.service'
        names={legacy,self.pool_unit_name(delivery)}
        listed=r.output(['systemctl','list-units','--all','--plain','--no-legend','--full',
                         'multica-verify-'+delivery+'*.service'])
        names.update(line.split()[0] for line in listed.splitlines() if line.strip())
        states=[]
        for unit in sorted(names):
            if unit!=legacy and not re.fullmatch('multica-verify-'+delivery+r'-[0-9a-f]{16}\.service',unit):
                raise r.RecoveryRequired('验收接力单元名称不匹配：'+unit)
            properties=dict(line.split('=',1) for line in r.output(['systemctl','show',unit,
                '--property=LoadState,ActiveState,MainPID,ControlGroup,WorkingDirectory,Type,KillMode','--no-pager']).splitlines() if '=' in line)
            if properties.get('LoadState')=='not-found':continue
            object_path=json.loads(r.output(['busctl','--json=short','call','org.freedesktop.systemd1',
                '/org/freedesktop/systemd1','org.freedesktop.systemd1.Manager','GetUnit','s',unit]))['data'][0]
            starts=json.loads(r.output(['busctl','--json=short','get-property','org.freedesktop.systemd1',
                object_path,'org.freedesktop.systemd1.Service','ExecStart']))['data']
            environment=json.loads(r.output(['busctl','--json=short','get-property','org.freedesktop.systemd1',
                object_path,'org.freedesktop.systemd1.Service','Environment']))['data']
            environment=dict(item.split('=',1) for item in environment if '=' in item)
            command=starts[0][1] if len(starts)==1 else []
            script=Path(command[1]).resolve() if len(command)==6 else None
            current=Path(__file__).resolve()
            trusted=script==current or (script is not None and script.name==current.name and script.is_file()
                and script.parent.parent==(r.RUN/'publisher').resolve()
                and re.fullmatch('delivery-'+self.env+'-[0-9a-f]{16}',script.parent.name))
            if (properties.get('LoadState')!='loaded' or properties.get('ActiveState') not in ('inactive','failed','active','activating','deactivating')
                    or properties.get('WorkingDirectory')!=str(r.ROOT) or properties.get('Type')!='exec'
                    or properties.get('KillMode')!='process' or len(starts)!=1 or not trusted
                    or starts[0][0]!=sys.executable or starts[0][2] is not False
                    or command!=[sys.executable,str(script),'--env',self.env,'--pool-delivery',delivery]
                    or unit not in (legacy,self.pool_unit_name(delivery,script))
                    or environment.get('MULTICA_RELEASE_ROOT')!=str(r.ROOT)
                    or environment.get('HOME')!=pwd.getpwuid(os.getuid()).pw_dir):
                raise r.RecoveryRequired('已有验收接力单元身份不匹配，拒绝替换：'+unit)
            properties.update(unit=unit,script=str(script))
            states.append(properties)
        return states

    def start_pool_unit(self,delivery):
        unit=self.pool_unit_name(delivery)
        states=self.pool_units(delivery)
        # 活动旧版本先完成当前轮；同一作业仍由原文件锁和服务端令牌隔离。
        if any(state['ActiveState'] in ('active','activating','deactivating') for state in states):return
        for state in states:self.check_idle_pool_services(state['unit'],state)
        if any(state['unit']==unit for state in states):
            r.run(['systemctl','start',unit]);return
        home=pwd.getpwuid(os.getuid()).pw_dir
        command=[sys.executable,str(Path(__file__).resolve()),'--env',self.env,'--pool-delivery',delivery]
        # 旧单元里的槽位服务继续由 canonical PID/profile 管理，新控制器独立接续原作业。
        r.run(['systemd-run','--quiet','--collect','--unit',unit,'--property=Type=exec',
               '--property=KillMode=process','--working-directory',r.ROOT,
               '--setenv=MULTICA_RELEASE_ROOT='+str(r.ROOT),'--setenv=HOME='+home,*command])

    def pool_dispatch(self,rows,current_id,allow_new=True):
        if not allow_new and not (r.RUN/'verification-jobs').exists():
            project=self.batch_project()
            return {d['id'] for d in rows if d['id']!=current_id and d['phase']=='waiting_int' and project and d.get('project_id')==project}
        managed=[]; reserved=set(); active=set(); blocked=set()
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
            if d['phase']=='waiting_int' and self.batch_project() and d.get('project_id')==self.batch_project():
                reserved.add(d['id'])
                review=d.get('final_review') or {}
                if not d.get('source_ready') or d.get('issue_status')!='in_progress' or review.get('state')!='passed' or review.get('head_sha')!=d.get('source_commit'):continue
            job_path=self.pool_job_path(d['id'])
            existing=job_path.exists()
            if existing and r.read(job_path).get('stage')=='quarantined' and not (d.get('plan_snapshot') or {}).get('verification_repair'):continue
            if d['id'] not in recovering and (d['phase'] not in ('waiting_int','testing') or not (allow_new or existing)):
                continue
            managed.append(d)
            try:
                if any(state['ActiveState'] in ('active','activating','deactivating') for state in self.pool_units(d['id'])):
                    active.add(d['id'])
            except Exception as error:
                blocked.add(d['id'])
                print('验收接力恢复受阻 '+d['id']+'：'+str(error),file=sys.stderr)
        for d in managed:
            if d['id'] in active or d['id'] in blocked:continue
            if len(active)>=4:break
            try:
                self.start_pool_unit(d['id'])
                active.add(d['id'])
            except Exception as error:
                print('验收接力恢复受阻 '+d['id']+'：'+str(error),file=sys.stderr)
        return reserved | {d['id'] for d in managed}

    def prepare_repair(self,d,job,save):
        # 1. Retain the original quarantine until the pinned replacement and unchanged running baseline pass all gates.
        request=d['plan_snapshot']['verification_repair']['request']
        old=job.get('repair_lease') or job.get('lease')
        if not old or old['name']!=request['environment'] or old['generation']!=request['generation']:
            raise r.RecoveryRequired('修复请求与原槽位记录不符')
        env=old['name']
        metadata=r.read(r.meta_path(env))
        r.check_process_ownership(env,metadata)
        if not self.seed_matches(env,request):raise r.RecoveryRequired('修复不能重置种子或换任务')
        source=self.source(d,integrate=False)
        self.failure_stage='repair_building'
        save('repair_building',lease=old,repair_lease=old,source_commit=d['source_commit'])
        artifact=r.build_verification(source,d['id'])
        manifest=r.read(artifact/'release.json')
        if manifest['source_commit']!=d['source_commit'] or manifest['files']!=r.checksums(artifact):raise r.RecoveryRequired('修复制品指纹不符')
        r.validate_pending_migrations(artifact,metadata['release_dir'])
        active=r.candidate(env=env) if r.candidate_path(env).exists() else None
        if active and active['status'] not in ('released','failed','succeeded'):
            if (active.get('delivery_id')!=d['id'] or active.get('release_id')
                    or active.get('previous_release')!=metadata['release_id']
                    or active.get('status')!='recovery_required'):
                raise r.RecoveryRequired('槽位存在已部署或未知候选，禁止覆盖')
            r.release_candidate(active['id'],env=env)
        # Modification 3. Only attest the inventory after the complete local checksum comparison and compatibility gate passed.
        detail=repair_detail(artifact,manifest,metadata['release_id'],request['compatibility_evidence'])
        self.failure_stage='repair_slot_advance'
        lease=self.api('delivery-queue/slots/advance',dict(old,action='repair',replacement_worker_token=d['worker_token'],
                       replacement_source_commit=d['source_commit'],detail=detail))
        save('preparing',lease=lease,repair_lease=old,artifact=str(artifact),source=str(source),source_commit=d['source_commit'])
        return source,artifact,lease

    def pool_delivery(self,delivery):
        self.refresh_release_settings()
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
            managed=bool(self.batch_project() and d.get('project_id')==self.batch_project())
            install=r.RUN/'verification-install'/(lease['name']+'.json')
            cleanup=r.read(install) if managed and install.exists() else {}
            recovering=cleanup.get('stage') in ('cleaning','cleaned')
            if recovering and (cleanup.get('cleanup_delivery')!=lease['delivery_id'] or cleanup.get('cleanup_generation')!=lease['generation'] or cleanup.get('cleanup_candidate')!=lease['candidate_id']):
                raise r.RecoveryRequired('清理记录与当前槽位不符')
            if not recovering:
                r.drain(lease['name'],r.read(r.meta_path(lease['name'])))
                dev.stop(r,lease['name'],lease)
            if managed:
                self.require_sql_seed()
                def fence():
                    actual=self.api('delivery-queue/slots?delivery_id='+delivery)
                    if any(actual.get(k)!=lease.get(k) for k in ('name','generation','delivery_id','worker_token','source_commit','candidate_id')) or actual.get('state')!='draining':
                        raise r.RecoveryRequired('清理前验收槽位占用已变化')
                seed.clear_slot(lease['name'],lease,r.RUN/'verification-postgres.json',fence)
            r.release_candidate(c['id'],env=lease['name'])
            slot(lease,'release')
            save('released',candidate_id=c['id'])
        def deploy_update(lease,c,source,artifact):
            manifest=r.read(Path(artifact)/'release.json')
            if manifest['source_commit']!=lease['source_commit'] or manifest.get('verification_frontend')!='dev' or r.checksums(artifact)!=manifest['files']:
                raise r.RecoveryRequired('测试更新产物身份不符')
            live=r.read(r.meta_path(lease['name']))
            ready_new=c and c.get('commit')==lease['source_commit'] and c.get('status')=='verifying' and live.get('release_id')==manifest['id']
            if c and c.get('commit')==lease['source_commit'] and c.get('status') in ('building','recovery_required'):
                if live.get('release_id')==manifest['id'] and dev.marker(r,lease['name']).exists() and r.read(dev.marker(r,lease['name'])).get('state')=='running':
                    r.check_process_ownership(lease['name'],live);r.verify_browser(lease['name'])
                    c.update(status='verifying',release_id=manifest['id'],source_digest=manifest['source_digest'],validation_identity=r.validation_identity(source,lease['name'],self.lease_seed(lease['name'])),deployed_at=live['deployed_at'])
                    with r.lock(r.candidate_lock(lease['name'])):r.save_candidate(c)
                    ready_new=True
                elif live.get('release_id')==c.get('previous_release'):
                    c.update(status='failed',error='测试更新中断，基线仍在运行')
                    with r.lock(r.candidate_lock(lease['name'])):r.save_candidate(c)
                else:raise r.RecoveryRequired('测试更新中断后的环境身份不明确')
            if not ready_new:
                dev.stop(r,lease['name'],lease)
                if c and c.get('status') not in ('released','failed','succeeded'):
                    r.release_candidate(c['id'],env=lease['name'])
                c=r.prepare_verification(source,artifact,lease,dev_web=True,slot_seed=self.lease_seed(lease['name']))
            elif c.get('generation')!=lease['generation']:
                c['generation']=lease['generation']
                with r.lock(r.candidate_lock(lease['name'])):r.save_candidate(c)
            already_prepared=lease.get('state')=='verifying' and lease.get('candidate_id')==c['id']
            lease['candidate_id']=c['id']
            if not already_prepared:slot(lease,'prepared')
            lease['state']='verifying'
            save('verifying',lease=lease,candidate_id=c['id'])
            current=refresh()
            if not current or current['source_commit']!=lease['source_commit']:raise r.RecoveryRequired('测试更新后的交付身份不符')
            if current['candidate_id']!=c['id']:
                self.advance(current,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path(lease['name']))['frontend_url'],candidate_identity=c.get('validation_identity',{}),candidate_commit=c.get('commit',''))
            return c
        lease=None; c=None; hot=False; stopped=threading.Event(); heartbeat_errors=[]
        with r.lock('verification-job-'+delivery):
            try:
                d=refresh()
                if not d:return
                job=r.read(path) if path.exists() else {}
                c=None
                repairing=bool((d.get('plan_snapshot') or {}).get('verification_repair')) and not d.get('candidate_id')
                if repairing:
                    if d['phase']=='waiting_int':
                        self.advance(d,'claim');d=refresh()
                    if d['phase']!='testing':return
                    source,artifact,lease=self.prepare_repair(d,job,save)
                elif job.get('lease'):
                    try:
                        lease=self.api('delivery-queue/slots?delivery_id='+delivery)
                    except urllib.error.HTTPError as e:
                        if e.code!=404:raise
                        save('released',candidate_id=job.get('candidate_id',''));return
                    previous=job['lease']
                    updating=job.get('stage')=='updating'
                    if lease['worker_token']!=previous['worker_token'] or (lease['source_commit']!=previous['source_commit'] and not (updating and lease['source_commit']==job.get('source_commit') and (d.get('plan_snapshot') or {}).get('verification_update',{}).get('state')=='applying')):
                        raise r.RecoveryRequired('恢复占用与原交付身份不一致')
                    c=r.candidate(env=lease['name'])
                    if c['delivery_id']!=delivery or (not updating and c['id']!=lease['candidate_id']):
                        raise r.RecoveryRequired('部署中断，候选尚未完成登记，需要核对现场')
                    r.check_process_ownership(lease['name'],r.read(r.meta_path(lease['name'])),allow_unhealthy=job.get('stage')=='draining')
                    if d['phase']=='testing' and not updating:r.validate_candidate(c)
                    resume_lease=dict(lease)
                    lease=slot(lease,'resume')
                    marker=dev.marker(r,lease['name'])
                    marker_generation=r.read(marker).get('generation') if marker.exists() else None
                    old_lease=resume_lease if marker_generation in (None,resume_lease['generation']) else (job.get('previous_lease') or previous)
                    dev.adopt(r,lease['name'],old_lease,lease)
                    seed.adopt_cleaning(lease['name'],resume_lease,lease)
                    if not updating:
                        c['generation']=lease['generation']
                        with r.lock(r.candidate_lock(lease['name'])):r.save_candidate(c)
                        save('draining' if job.get('stage')=='draining' else 'verifying',lease=lease,candidate_id=c['id'])
                    if d['phase']!='testing':
                        release(lease,c);return
                else:
                    if d['phase'] not in ('waiting_int','testing'):return
                    if d['phase']=='waiting_int':self.advance(d,'claim')
                    project=self.batch_project()
                    hot=bool(project and d.get('project_id')==project and d.get('source_work_dir'))
                    if project and d.get('project_id')==project:
                        self.require_sql_seed()
                        if not hot:raise r.RecoveryRequired('任务工作目录尚未就绪，不能启动项目验收')
                    # 1. 等槽位超时退出后，下一轮直接复用已构建产物，避免每次空等都重新构建。
                    reuse=job.get('stage')=='waiting_slot' and job.get('source_commit')==d['source_commit'] and bool(job.get('artifact')) and Path(job['artifact']).exists() and bool(job.get('dev_web'))==hot
                    if reuse:
                        artifact=Path(job['artifact'])
                        source=Path(job.get('source') or r.read(artifact/'release.json')['source_directory'])
                        save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'],dev_web=hot)
                    else:
                        source=self.source(d,integrate=not (project and d.get('project_id')==project))
                        save('building',source_commit=d['source_commit'])
                        artifact=r.build_verification(source,delivery,dev_web=True) if hot else r.build_verification(source,delivery)
                        save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'],dev_web=hot)
                    # 等槽位有上限；种子维护和全忙均正常排队，下一轮复用现有构建。
                    deadline=time.monotonic()+600
                    while lease is None:
                        d=refresh()
                        if not d or d['phase']!='testing':return
                        try:
                            lease=self.ensure_seed(lambda:slot(dict(delivery_id=delivery,worker_token=d['worker_token'],source_commit=d['source_commit']),'claim'))
                        except Busy:
                            stopped.set();return
                        except urllib.error.HTTPError as e:
                            detail=json.loads(e.read().decode()) if e.code==409 else {}
                            if detail.get('error')!='没有可用验收槽位':raise
                            if time.monotonic()>=deadline:
                                # 超时只记录现场，进度回执失败不能把交付判成故障。
                                try:save('waiting_slot',artifact=str(artifact),source=str(source),source_commit=d['source_commit'],dev_web=hot,timeout=True)
                                except Exception:pass
                                raise SlotWaitTimeout('等待验收槽位超过 10 分钟，交给下一轮续接')
                            time.sleep(5)
                    # 修改 2. 恢复与领取之间有并发，领到之后还要核对一次数据版本，不能只在领取前判断。
                    if not self.seed_matches(lease['name']):
                        slot(lease,'quarantine','槽位数据版本不是当前种子')
                        raise r.RecoveryRequired('槽位种子与当前版本不一致，已隔离并等待外部恢复')
                    save('preparing',lease=lease,artifact=str(artifact),dev_web=hot)
                def heartbeat():
                    while not stopped.wait(20):
                        try:slot(lease,'heartbeat')
                        except Exception as e:
                            heartbeat_errors.append(e);return
                heartbeat_thread=threading.Thread(target=heartbeat,daemon=True);heartbeat_thread.start()
                if job.get('stage')=='updating' and (d.get('plan_snapshot') or {}).get('verification_update',{}).get('state')=='applying':
                    source=Path(job['source']);artifact=Path(job['artifact'])
                    save('updating',lease=lease,previous_lease=old_lease,candidate_id=c['id'],source=str(source),source_commit=lease['source_commit'],artifact=str(artifact))
                    c=deploy_update(lease,c,source,artifact)
                if c is None:
                    pinned_seed=self.lease_seed(lease['name'])
                    c=r.prepare_verification(source,artifact,lease,dev_web=hot,slot_seed=pinned_seed)
                    lease['candidate_id']=c['id'];slot(lease,'prepared')
                    save('verifying',lease=lease,candidate_id=c['id'])
                if heartbeat_errors:raise heartbeat_errors[0]
                d=refresh()
                if d['phase']=='testing':
                    if d['candidate_id']!=c['id']:
                        self.advance(d,'prepared',candidate_id=c['id'],candidate_url=r.read(r.meta_path(lease['name']))['frontend_url'],candidate_identity=c.get('validation_identity',{}),candidate_commit=c.get('commit',''))
                # 停在 int 等验收的交付不能无限持有发布车道：超过阈值且没有验收证据就自动释放。
                testing_entered=time.time()
                notified=False
                while True:
                    d=refresh()
                    if not d or d['phase']!='testing':break
                    if heartbeat_errors:raise heartbeat_errors[0]
                    if d.get('pending_update_notice'):
                        try:self.advance(d,'notify-pending-verification',stage=d['pending_update_notice'])
                        except urllib.error.HTTPError as error:
                            if error.code!=409:raise
                    update=(d.get('plan_snapshot') or {}).get('verification_update') or {}
                    if update.get('state')=='pending':
                        request=update['request']
                        changed=dict(d,source_commit=request['source_commit'],source_branch=request['source_branch'])
                        try:
                            source=self.source(changed,integrate=False)
                            save('updating',lease=lease,candidate_id=c['id'],source=str(source),source_commit=request['source_commit'])
                            artifact=r.build_verification(source,delivery,dev_web=True)
                        except Exception as error:
                            self.advance(d,'verification-update-failed',error=r.redact_failure(str(error))[:1500] or '测试更新构建失败')
                            save('verifying',lease=lease,candidate_id=c['id'])
                            time.sleep(5);continue
                        save('updating',lease=lease,candidate_id=c['id'],source=str(source),source_commit=request['source_commit'],artifact=str(artifact))
                        stopped.set();heartbeat_thread.join(timeout=25)
                        if heartbeat_errors:raise heartbeat_errors[0]
                        old_lease=lease
                        result=self.advance(d,'begin-verification-update',slot_generation=lease['generation'],candidate_commit=request['source_commit'])
                        lease=self.api('delivery-queue/slots?delivery_id='+delivery)
                        if lease['generation']!=result['slot_generation'] or lease['source_commit']!=request['source_commit'] or lease['state']!='preparing':
                            raise r.RecoveryRequired('测试更新后的槽位身份不符')
                        dev.adopt(r,lease['name'],old_lease,lease)
                        save('updating',lease=lease,previous_lease=old_lease,candidate_id=c['id'],source=str(source),source_commit=request['source_commit'],artifact=str(artifact))
                        stopped.clear();heartbeat_thread=threading.Thread(target=heartbeat,daemon=True);heartbeat_thread.start()
                        c=deploy_update(lease,c,source,artifact)
                        testing_entered=time.time();notified=False
                        continue
                    project=self.batch_project()
                    managed=bool(project and d.get('project_id')==project)
                    if not managed and not d['acceptance_submitted'] and time.time()-testing_entered>TESTING_STALE_SECONDS:
                        self.advance(d,'needs_changes',error='候选在 int 等待验收超过 %d 分钟且没有提交证据，已自动释放发布车道' % (TESTING_STALE_SECONDS//60))
                        break
                    try:
                        if not d['acceptance_submitted']:
                            # 外部归属结束后补派一次；被归属围栏延后的通知不算送达。
                            # 服务端固定评论标识与实际执行记录负责跨进程幂等。
                            if not notified and not d.get('verification_run') and not (d.get('verification_owner') or {}).get('external'):
                                result=self.advance(d,'notify')
                                notified=result.get('verification_deferred') is not True
                            attempt=d.get('verification_run') or {}
                            if managed and attempt.get('settled') and attempt.get('status') in ('failed','cancelled','blocked','completed'):
                                if attempt.get('failure_notice') or d.get('verification_failure_count',0)<3:
                                    self.advance(d,'notify-verification-failure',verification_run_id=attempt['id'],error=attempt.get('error') or '测试运行结束但没有提交通过证据')
                                pending=True
                            else:
                                pending=self.verification_pending(d,notify=False)
                    except urllib.error.HTTPError as e:
                        if e.code!=409:raise
                        # 服务端围栏：该执行不是当前触发评论的最新运行，或阶段已经变化。
                        # 这不是交付故障，不能隔离槽位或用兜底 fail 把被拦下的写入重做一遍。
                        d=refresh()
                        if not d or d['phase']!='testing':break
                        time.sleep(5);continue
                    if not d['acceptance_submitted'] and not pending:break
                    if d['acceptance_submitted']:
                        project=self.batch_project()
                        review=d.get('final_review') or {}
                        if project and d.get('project_id')==project and (review.get('state')!='passed' or review.get('head_sha')!=d['source_commit']):
                            waiting_fix=review.get('state') in ('rejected','stopped') and review.get('head_sha')==d['source_commit']
                            if not waiting_fix and d.get('issue_status')!='in_review':
                                self.advance(d,'request-final-review')
                            time.sleep(5);continue
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
                if isinstance(e,urllib.error.HTTPError) and e.code==409:
                    # 槽位代次或心跳已被围栏拒绝，旧接力不能再隔离或失败回填新归属。
                    save('fenced',lease=lease,candidate_id=c['id'] if c else '',detail=str(e)[:500]);return
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
        settings=getattr(self,'release_settings',None)
        if settings and settings.get('configured'):
            if settings.get('enabled') is not True:return None
            return dict(enabled=True,controller_environment=self.env,window_seconds=600,project_id=settings['project_id'])
        path=r.RUN/'delivery-batch.json'
        if not path.exists():return None
        config=r.read(path)
        if config.get('enabled') is not True or config.get('controller_environment')!=self.env:return None
        return config

    def batch_window(self):
        return int((self.batch_config() or {}).get('window_seconds') or 60)

    def batch_project(self):
        settings=getattr(self,'release_settings',None)
        if settings and settings.get('configured'):return settings.get('project_id') or ''
        return (self.batch_config() or {}).get('project_id') or ''

    def refresh_release_settings(self):
        settings=self.api('release-agent/settings')
        if not isinstance(settings,dict) or not isinstance(settings.get('configured'),bool):
            raise RuntimeError('发布 Agent 设置响应无效')
        if settings['configured'] and not settings.get('project_id'):
            raise RuntimeError('发布 Agent 缺少项目身份')
        self.release_settings=settings

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
            project=self.batch_project()
            if not any(d['phase']=='approved' and (not project or d.get('project_id')==project) for d in rows):return False
            if self.publication_owner():return True
            body=dict(action='open',window_seconds=self.batch_window())
            if project:body['project_id']=project
            self.batch_call(body)
            return True
        token=batch.get('worker_token') or ''
        if batch['status']=='collecting':
            if time.time()<timestamp(batch['collect_until']):return True
            state=self.batch_call(dict(action='freeze',batch_id=batch['id']))
            batch=state['batch'];members=self.pending_members(state)
            if batch['status'] in ('cancelled','needs_changes'):return True
            token=batch.get('worker_token') or ''
        if batch['status']=='frozen':
            if batch.get('project_id'):
                stage='conflict' if batch.get('error') else 'batch'
                self.batch_call(dict(action='dispatch-agent',batch_id=batch['id'],worker_token=token,stage=stage))
                if batch.get('error'):
                    try:self.batch_call(dict(action='retry',batch_id=batch['id'],worker_token=token,automatic=True))
                    except urllib.error.HTTPError as error:
                        if error.code!=409:raise
                    return True
            return self.batch_prepare(batch,members,rows)
        # 修改 4. 进程在准备阶段退出会留下 preparing；这里接续原批次，不新建批次也不让状态永久卡住。
        if batch['status']=='preparing':return self.batch_resume(batch,members,rows)
        if batch['status']=='validating':
            if batch.get('project_id'):
                self.batch_call(dict(action='dispatch-agent',batch_id=batch['id'],worker_token=token,stage='integration'))
            return self.batch_validate(batch,members,rows)
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
                action='hold' if batch.get('project_id') else 'fail'
                self.batch_call(dict(action=action,batch_id=batch['id'],worker_token=batch['worker_token'],error=detail,conflict_delivery_id=conflict['delivery_id']))
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
                self.ensure_int_data(batch['id'],fence,mandatory=bool(batch.get('project_id')))
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
            snapshot=row.get('plan_snapshot') or {}
            if self.batch_project() and row.get('project_id')==self.batch_project() and snapshot.get('integration_skip_reason') and not any(c.get('stage')=='integration' for c in snapshot.get('required_checks') or []):
                continue
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

    def batch_acceptance(self,candidate,members,rows,project=False):
        row=self.batch_row(rows,candidate.get('delivery_id',''))
        if (row and row.get('candidate_id')==candidate['id'] and row.get('acceptance_submitted')
                and (not project or row.get('integration_accepted'))
                and row.get('plan_snapshot',{}).get('candidate_commit')==candidate['commit']):
            return row.get('evidence') or None
        return None

    def sync_environment_baselines(self,candidate,fence):
        """仅在生产实际运行固定候选后推进环境分支，并保存核对结果。"""
        fence()
        published=r.read(r.RUN/'published.json')
        meta=r.read(r.meta_path('prod'))
        commit=candidate.get('commit','');digest=candidate.get('source_digest','')
        if not digest or published.get('candidate')!=candidate.get('id') or published.get('commit')!=commit or published.get('source_digest')!=digest or meta.get('commit')!=commit or meta.get('source_digest')!=digest:
            raise r.RecoveryRequired('生产发布记录与候选身份不一致，不能同步环境分支')
        r.check_process_ownership('prod',meta)
        r.check_live_environment('prod',meta,r.read(r.config_path('prod')))
        if r.http(meta['backend_url']+'/health').get('commit')!=commit:
            raise r.RecoveryRequired('生产运行程序与候选提交不一致')
        manifest=r.read(Path(meta['release_dir'])/'release.json')
        if manifest.get('source_commit')!=commit or manifest.get('source_digest')!=digest or not isinstance(manifest.get('files'),dict) or not manifest['files']:
            raise r.RecoveryRequired('生产产物清单与候选不一致')
        if not all(name in manifest['files'] for name in ('server/bin/server','server/bin/multica')) or not any(name.startswith('apps/web/') for name in manifest['files']):
            raise r.RecoveryRequired('生产产物清单缺少 Web、Server 或 Daemon')
        actual=r.checksums(Path(meta['release_dir']))
        if any(actual.get(name)!=value for name,value in manifest.get('files',{}).items()):
            raise r.RecoveryRequired('生产产物校验失败，不能同步环境分支')
        refs=sync_environment_refs(r.ROOT,commit,fence)
        r.write(r.RUN/'environment-baselines.json',dict(candidate_id=candidate['id'],commit=commit,source_digest=digest,refs=refs,updated_at=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())),True)
        r.write(r.RUN/'environment-baseline-maintenance.json',dict(candidate_id=candidate['id'],commit=commit,source_digest=digest),True)

    def baseline_slot_stale(self,name,seed_id,commit):
        """已维护槽位是否还停在本次生产基线。

        同步期间该槽位仍会被下一个任务领取：释放只清空数据并留下候选程序，历史
        completed 记录不能证明现在的部署提交。按槽位实际元数据和安装记录判断。
        """
        if r.read(r.meta_path(name)).get('commit')!=commit:return True
        install=r.RUN/'verification-install'/(name+'.json')
        state=r.read(install) if install.exists() else {}
        return state.get('stage')!='cleaned' or state.get('baseline_cleanup_seed')!=seed_id

    def reconcile_environment_baseline(self):
        """以已发布归档生成种子，并逐个更新空闲槽位。"""
        import uuid
        journal=r.RUN/'environment-baseline-maintenance.json'
        if not journal.exists():return
        if r.read(journal).get('complete') is True:return
        try:
            with r.lock('verification-seed-maintenance'), r.lock('publisher'):
                job=r.read(journal);published=r.read(r.RUN/'published.json')
                if any(published.get(k)!=job.get(j) for k,j in (('candidate','candidate_id'),('commit','commit'),('source_digest','source_digest'))):
                    raise r.RecoveryRequired('环境基线任务与当前生产记录不符')
                commit=job['commit'];candidate=job['candidate_id']
                if not re.fullmatch(r'[0-9a-f]{40}',commit) or not re.fullmatch(r'[a-zA-Z0-9_.-]+',candidate):
                    raise r.RecoveryRequired('环境基线任务身份无效')
                archive=r.RUN/'accepted-artifacts'/candidate/commit
                package=r.read(archive/'release.json')
                if package.get('source_commit')!=commit or package.get('source_digest')!=job['source_digest'] or r.checksums(archive)!=package.get('files'):
                    raise r.RecoveryRequired('生产验收归档身份不符')
                seed_id='prod-'+commit
                destination=seed.SEEDS/seed_id
                pgconfig=r.RUN/'verification-postgres.json'
                if not destination.exists():
                    temporary='stage-'+commit+'-'+uuid.uuid4().hex[:8]
                    staged=seed.build_seed(temporary,None,archive,pgconfig)
                    manifest=r.read(staged/'seed.json')
                    manifest['seed_id']=seed_id;manifest['build']['baseline_commit']=commit
                    seed.write_manifest(staged,manifest)
                    seed.verify_sql_seed(staged,archive,pgconfig)
                    os.rename(staged,destination)
                path,manifest,artifact=seed.read_seed(destination)
                if (manifest.get('build') or {}).get('baseline_commit')!=commit or not seed.verified_sql_seed(path,manifest):
                    raise r.RecoveryRequired('生产基线种子身份不符')
                current=self.current_seed() or {}
                slots=self.api('delivery-queue/slots')
                if {x.get('name') for x in slots}!=set(r.VERIFICATION_ENVS):
                    raise r.RecoveryRequired('验收槽位清单不完整')
                completed=set(job.get('completed_slots') or [])
                pending=job.get('slot')
                if pending:
                    chosen=next((x for x in slots if x.get('name')==pending),None)
                    if not chosen or chosen.get('state')!='idle' or chosen.get('delivery_id'):
                        return
                else:
                    # 修改 1. 同步期间已维护的槽位仍会被下一个任务领取：释放只清空数据并留下候选
                    # 程序，历史 completed 不能证明当前部署提交。空闲时重新核对实际程序，不是基线
                    # 就重新维护，避免槽位最终停留在任务版本。
                    stale=[x['name'] for x in sorted(slots,key=lambda x:x['name'])
                           if x['name'] in completed and x['state']=='idle' and not x.get('delivery_id')
                           and self.baseline_slot_stale(x['name'],seed_id,commit)]
                    if stale:
                        completed.difference_update(stale);job['completed_slots']=sorted(completed)
                        r.write(journal,job,True)
                    chosen=next((x for x in sorted(slots,key=lambda x:x['name']) if x['state']=='idle' and not x.get('delivery_id') and x['name'] not in completed),None)
                    if not chosen:
                        # 修改 2. 被占用的槽位无法核对：只有三个槽位都空闲且都停在基线程序上，
                        # 才能判定整次同步完成，不能在仍有槽位跑任务版本时提前收尾。
                        occupied=[x for x in slots if x['state']!='idle' or x.get('delivery_id')]
                        if current.get('seed_id')==seed_id and completed==set(r.VERIFICATION_ENVS) and not stale and not occupied:
                            job['complete']=True;r.write(journal,job,True)
                        return
                    job.update(slot=chosen['name'],previous_enabled=chosen['enabled']);r.write(journal,job,True)
                install=r.RUN/'verification-install'/(chosen['name']+'.json')
                state=r.read(install) if install.exists() else {}
                cleaning=state.get('stage') in ('baseline_cleaning','cleaned') and state.get('baseline_cleanup_seed')==seed_id
                if cleaning and state.get('stage')=='cleaned':
                    cleaning=r.read(r.meta_path(chosen['name'])).get('commit')==commit
                if not cleaning:
                    if current.get('seed_id')!=seed_id or current.get('dump_sha256')!=artifact['sha256']:
                        seed.verify_seed(chosen['name'],path,pgconfig,accept=True)
                    else:
                        seed.ensure(chosen['name'],pgconfig)
                seed.clear_idle_baseline(chosen['name'],seed_id,artifact['sha256'],pgconfig)
                after=next(x for x in self.api('delivery-queue/slots') if x['name']==chosen['name'])
                if job.get('previous_enabled') and not after.get('enabled'):
                    seed.resume(chosen['name'],'生产基线维护完成')
                completed.add(chosen['name']);job['completed_slots']=sorted(completed)
                job.pop('slot',None);job.pop('previous_enabled',None);r.write(journal,job,True)
        except BlockingIOError:
            return

    def reusable_batch_checks(self,candidate,member_rows):
        """把成员已提交的验收证据复用到组合候选上，避免再等一次人工 accept。"""
        for row in member_rows or []:
            evidence=row.get('evidence') or []
            if evidence:
                return evidence
        return None

    def batch_validate(self,batch,members,rows):
        representative=self.batch_row(rows,members[0]['delivery_id']) if members else None
        candidate=r.candidate() if r.candidate_path().exists() else None
        if not candidate or candidate.get('batch_id')!=batch['id']:
            self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error='组合候选记录缺失，需要核对 int 现场'))
            return True
        # 队列通常不返回终态成员；按原交付查询，不能把缺行直接当作已取消。
        member_rows=[]
        for member in members:
            row=self.batch_row(rows,member['delivery_id'])
            if row is None:
                row=self.batch_row(self.api('delivery-queue?current='+member['delivery_id']),member['delivery_id'])
            if row:member_rows.append(row)
        if candidate['status']=='released' or any(row['phase'] in ('cancelled','published') for row in member_rows):
            self.batch_call(dict(action='abort',batch_id=batch['id'],worker_token=batch['worker_token'],error='组合候选已释放或成员已终态，结束失效批次并重排有效成员'))
            if candidate['status'] in ('verifying','accepted'):r.release_candidate(candidate['id'])
            representative=self.batch_row(member_rows,candidate.get('delivery_id',''))
            if representative:self.finish_publication(representative)
            return True
        representative=self.batch_row(rows,candidate.get('delivery_id',''))
        if representative:self.publication(representative,'heartbeat')
        self.notify_batch_ready(members)
        if candidate['status']=='verifying':
            # 修改 13. 组合验收由批次成员在同一个固定候选上提交，证据与候选一起归档后才进入发布。
            if batch.get('project_id'):
                if len(member_rows)!=len(members):return True
                required=[row for row in member_rows if any(c.get('stage')=='integration' for c in (row.get('plan_snapshot',{}).get('required_checks') or []))]
                if any(not row.get('integration_accepted') for row in required):return True
            checks=self.batch_acceptance(candidate,members,rows,project=bool(batch.get('project_id')))
            if batch.get('project_id') and checks and required:
                checks=[check for row in member_rows if row.get('integration_accepted') for check in (row.get('evidence') or [])]
            reused=checks is None
            if reused:
                # 成员在 testing 阶段提交过的业务验收证据对同一提交仍然有效：组合候选只是把它
                # 合并进新基线。若服务端仍要求针对组合候选的确认，就退回等待人工补交。
                # 项目批次始终需要当前组合候选上的实际检查。
                checks=None if batch.get('project_id') else self.reusable_batch_checks(candidate,member_rows)
            if not checks:return True
            evidence=r.RUN/'delivery-evidence'/(candidate['id']+'.json')
            r.write(evidence,dict(candidate=candidate['id'],summary='组合候选实际功能验收',checks=checks),True)
            try:
                r.accept(candidate['id'],evidence)
            except RuntimeError:
                if reused:return True
                raise
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
        if result['status']=='recovery_required':
            raise r.RecoveryRequired('生产更新后的恢复尚未确认：'+(result.get('error') or '请核对实际环境')[:1200])
        if result['status'] not in ('succeeded','failed'):
            raise r.RecoveryRequired('生产发布请求处于未知状态，保留发布占用')
        if result['status']=='succeeded':
            if batch.get('project_id'):
                if not representative:
                    representative=self.batch_row(self.api('delivery-queue?current='+candidate['delivery_id']),candidate['delivery_id'])
                if not representative:raise r.RecoveryRequired('生产已更新但发布占用对应的交付缺失')
                with self.publication_guard(representative) as (_,fence):
                    self.sync_environment_baselines(candidate,fence)
                    integration.clear_environment(r,batch['id'],fence)
            self.batch_call(dict(action='published',batch_id=batch['id'],worker_token=batch['worker_token'],candidate_commit=candidate['commit']))
        else:
            self.batch_call(dict(action='fail',batch_id=batch['id'],worker_token=batch['worker_token'],error=result.get('error','生产更新失败')[:1200]))
        if representative:self.finish_publication(representative)
        r.release_candidate(candidate['id'])
        return True

    def tick(self):
        # 固定 int/prod 只由生产控制队列调度，int 数据库不能另建同名占用抢写环境。
        if self.env!='prod':return
        self.refresh_release_settings()
        r.recover_release_windows()
        try:
            spec=importlib.util.spec_from_file_location('test_agent_control',Path(__file__).with_name('test-agent-control.py'))
            control=importlib.util.module_from_spec(spec);spec.loader.exec_module(control);control.tick(self)
        except Exception as error:
            print('测试 Agent 控制暂不可用：'+str(type(error).__name__),flush=True)
        with r.lock('delivery-worker-'+self.env):
            current=r.candidate() if r.candidate_path().exists() else None
            current_id=current.get('delivery_id') if current and current.get('status') not in ('released','succeeded','failed') else None
            rows=self.api('delivery-queue'+('?current='+current_id if current_id else ''))
            self.recover_finished_publications(rows)
            if self.pool_enabled():
                managed=self.pool_dispatch(rows,current_id)
            else:
                managed=self.pool_dispatch(rows,current_id,allow_new=False)
            if not current_id:
                self.reconcile_environment_baseline()
            rows=[d for d in rows if d['id'] not in managed]
            settings=getattr(self,'release_settings',None)
            if settings and settings.get('configured') and settings.get('enabled') is not True:
                rows=[d for d in rows if not (d['phase']=='approved' and d.get('project_id')==settings['project_id'])]
            # 2. 启用组批后，已批准交付统一由批次发布；单项流程不再单独领取，避免同一任务被发布两次。
            if self.batch_config():
                try:
                    if self.tick_batches(rows):return
                except Busy:return
                project=self.batch_project()
                rows=[d for d in rows if d['phase']!='approved' or (project and d.get('project_id')!=project)]
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
            # 修改 1. 优先推进可运行交付，避免历史结果通知重试阻塞固定 int。
            runnable=[d for d in active if d['phase'] not in ('published','needs_changes')]
            notices=[d for d in active if d['phase'] in ('published','needs_changes')]
            for d in runnable+queued+notices:
                try:
                    phase=d['phase']
                    if phase in ('published','needs_changes'):
                        result=self.advance(d,'notify-result')
                        if result.get('notification_deferred'):continue
                        return
                    if phase=='awaiting_review':
                        # Modification 2. The server decides eligibility; pool stays enabled for other deliveries.
                        if d.get('integration_pending') is True:
                            if self.busy(d):return
                            self.advance(d,'claim-integration')
                            d.setdefault('plan_snapshot',{})['integration_only']=True
                            self.prepare(d);return
                        if self.publication_path(d).exists():self.publication(d,'heartbeat')
                        self.advance(d,'notify-authorization');return
                    if phase in ('waiting_int','approved'):
                        if self.busy(d):return
                        self.advance(d,'claim');self.prepare(d);return
                    if phase=='testing':
                        if self.publication_path(d).exists():self.publication(d,'heartbeat')
                        if d['acceptance_submitted']:self.accept(d)
                        elif not d['candidate_id']:self.prepare(d)
                        else:self.verification_pending(d)
                        return
                    if phase in ('preparing_release','publishing'):
                        self.publication(d,'heartbeat')
                    if phase=='preparing_release':
                        if not d['candidate_id'] or not self.own_candidate(d):self.prepare(d)
                        elif d.get('release_validated'):self.publish(d)
                        elif d['acceptance_submitted']:self.accept(d,release=True)
                        else:self.verification_pending(d)
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
    names=r.operation_files(Path(__file__).parent)
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
