#!/usr/bin/env python3
"""固定版本发布：配置与数据属于环境，发布包只包含代码和构建产物。"""
import argparse, contextlib, fcntl, hashlib, json, os, re, shutil, signal, subprocess, sys, time, urllib.request, urllib.error, urllib.parse
from pathlib import Path

ROOT = Path(os.environ.get('MULTICA_RELEASE_ROOT', Path(__file__).resolve().parent.parent)).resolve()
RUN = ROOT / '.run'
ENVS = ('int', 'prod')

def require_environment(env):
    if env not in ENVS: raise RuntimeError(f'未知环境: {env}')

def run(args, **kw):
    return subprocess.run([str(a) for a in args], check=True, **kw)

def output(args, **kw):
    return subprocess.check_output([str(a) for a in args], **kw).decode().strip()

def read(path):
    return json.loads(Path(path).read_text())

def write(path, data, private=False):
    path = Path(path); path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + '.tmp')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600 if private else 0o644)
    with os.fdopen(fd, 'w') as f: json.dump(data, f, indent=2); f.write('\n')
    os.replace(tmp, path)

def meta_path(env): return RUN / 'deployments' / f'goal-test-{env}.json'
def config_path(env): return RUN / 'env' / f'{env}-services.json'
def process_env(pid):
    return dict(x.split('=', 1) for x in Path(f'/proc/{pid}/environ').read_text().split('\0') if '=' in x)
def exists(pid):
    try: return Path(f'/proc/{pid}/stat').read_text().split(') ')[1][0] != 'Z'
    except FileNotFoundError: return False

def http(url, method='GET'):
    req = urllib.request.Request(url, method=method)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=5) as r:
        data = r.read()
        try: return json.loads(data)
        except ValueError: return data.decode()

def ready(url, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try: return http(url)
        except Exception: time.sleep(.5)
    raise RuntimeError('服务启动失败: ' + url)

@contextlib.contextmanager
def lock(env,wait=False):
    p = RUN / 'locks' / f'release-{env}.lock'; p.parent.mkdir(parents=True, exist_ok=True)
    with p.open('a') as f:
        fcntl.flock(f, fcntl.LOCK_EX | (0 if wait else fcntl.LOCK_NB))
        yield

def copy_tree(src, dst, excludes=()):
    Path(dst).mkdir(parents=True, exist_ok=True)
    run(['rsync', '-a', *[f'--exclude={x}' for x in excludes], str(src) + '/', str(dst) + '/'])

def source_digest(root):
    names = sorted(set(subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).decode().split('\0')))
    h = hashlib.sha256(); files = []
    for name in names:
        p = Path(root) / name
        if not name or not p.is_file() or name.startswith('.run/'): continue
        h.update(name.encode() + b'\0'); h.update(p.read_bytes()); files.append(name)
    return h.hexdigest(), files

def checksums(root):
    result = {}
    for base, dirs, files in os.walk(root):
        dirs.sort()
        for name in dirs + files:
            link = Path(base) / name
            if link.is_symlink() and (not link.exists() or Path(root).resolve() not in link.resolve().parents):
                raise RuntimeError('发布目录包含越界或失效的符号链接: ' + str(link.relative_to(root)))
        for name in sorted(files):
            p = Path(base) / name; rel = str(p.relative_to(root))
            if rel == 'release.json': continue
            if p.is_symlink(): result[rel] = 'link:' + os.readlink(p)
            else:
                h = hashlib.sha256()
                with p.open('rb') as f:
                    for chunk in iter(lambda: f.read(1024*1024), b''): h.update(chunk)
                result[rel] = h.hexdigest()
    return result

def relocate_dependency_links(stage, source):
    """构建快照中的依赖只能指向快照，避免加载两份共享包及其单例。"""
    stage, source = Path(stage).resolve(), Path(source).resolve()
    for base, dirs, files in os.walk(stage):
        for name in dirs + files:
            link = Path(base) / name
            if not link.is_symlink(): continue
            target = link.resolve()
            if target.is_relative_to(stage): continue
            if not target.is_relative_to(source):
                raise RuntimeError('构建依赖符号链接越界: ' + str(link))
            replacement = stage / target.relative_to(source)
            if not replacement.exists():
                raise RuntimeError('构建依赖未包含在快照: ' + str(link))
            link.unlink()
            link.symlink_to(os.path.relpath(replacement, link.parent))

def build():
    with lock('build'):
        digest, files = source_digest(ROOT)
        sha = output(['git', 'rev-parse', 'HEAD'], cwd=ROOT)
        release_id = sha[:12] + '-' + digest[:12]
        artifact = RUN / 'artifacts' / release_id
        if artifact.exists():
            if checksums(artifact) != read(artifact / 'release.json')['files']: raise RuntimeError('发布包校验失败')
            print(release_id); return
        stage = RUN / 'build' / release_id
        if stage.exists(): raise RuntimeError('构建目录已存在，检查上次失败后单独清理: ' + str(stage))
        stage.mkdir(parents=True)
        for name in files:
            dest = stage / name; dest.parent.mkdir(parents=True, exist_ok=True); shutil.copy2(ROOT / name, dest)
        if source_digest(ROOT)[0] != digest: raise RuntimeError('构建快照期间源码发生变化，请重新构建')
        copy_tree(ROOT / 'node_modules', stage / 'node_modules')
        for parent in ('apps', 'packages'):
            for src in (ROOT / parent).glob('*/node_modules'):
                copy_tree(src, stage / src.relative_to(ROOT))
        relocate_dependency_links(stage, ROOT)
        env = os.environ.copy()
        for key in list(env):
            if key.startswith(('NEXT_PUBLIC_', 'MULTICA_', 'GOAL_TEST_')) or key in ('REMOTE_API_URL','FRONTEND_ORIGIN','DATABASE_URL','JWT_SECRET'): env.pop(key)
        env.update(STANDALONE='true', NEXT_PUBLIC_APP_VERSION=release_id, NEXT_TELEMETRY_DISABLED='1')
        for binary in ('server', 'multica', 'migrate'):
            run(['go', 'build', '-ldflags', f'-X main.version={release_id} -X main.commit={sha}', '-o', f'bin/{binary}', f'./cmd/{binary}'], cwd=stage/'server', env=env)
        run(['pnpm', '--filter', '@multica/web', 'build'], cwd=stage, env=env)
        pending = RUN / 'artifacts' / (release_id + '.pending'); pending.mkdir(parents=True)
        copy_tree(stage/'apps/web/.next/standalone', pending)
        copy_tree(stage/'apps/web/.next/static', pending/'apps/web/.next/static')
        copy_tree(stage/'apps/web/public', pending/'apps/web/public')
        copy_tree(stage/'server/bin', pending/'server/bin')
        copy_tree(stage/'server/migrations', pending/'server/migrations')
        manifest = dict(id=release_id, source_commit=sha, source_digest=digest, created_at=time.strftime('%FT%TZ',time.gmtime()), files=checksums(pending))
        write(pending/'release.json', manifest)
        os.rename(pending, artifact)
        print('发布包:', release_id)

def health_url(env):
    profile=read(meta_path(env))['daemon_profile']
    port=19514 if not profile else 19515 + sum(profile.encode()) % 1000
    return f'http://127.0.0.1:{port}'

def check_process_ownership(env, metadata):
    release = Path(metadata.get('release_dir', ROOT)).resolve()
    for role, pid in metadata['pids'].items():
        role_release = Path(metadata.get('role_release_dirs', {}).get(role, str(release)))
        expected_cwd = role_release / 'apps/web' if role == 'web' else role_release
        actual_cwd = Path(f'/proc/{pid}/cwd').resolve()
        # 首次接管旧 daemon 的目录仍为开发工作区，profile 与 health PID 另行核验。
        legacy_daemon = role == 'daemon' and not config_path(env).exists()
        if role == 'daemon' and metadata.get('release_id', '').startswith('baseline-'):
            legacy_daemon = True
        if actual_cwd != expected_cwd and not (legacy_daemon and actual_cwd == ROOT):
            raise RuntimeError(f'{env} {role} PID 工作目录不属于记录的版本')
        if role != 'web':
            binary = 'multica' if role == 'daemon' else 'server'
            binary_root = ROOT if legacy_daemon else release
            if Path(f'/proc/{pid}/exe').resolve() != binary_root / 'server/bin' / binary:
                raise RuntimeError(f'{env} {role} PID 执行文件不属于记录的版本')
        else:
            listener = output(['ss', '-ltnp', f"( sport = :{metadata['frontend_port']} )"])
            if f'pid={pid},' not in listener:
                raise RuntimeError(f'{env} Web PID 未监听记录的端口')
        if role == 'server':
            listener = output(['ss', '-ltnp', f"( sport = :{metadata['backend_port']} )"])
            if f'pid={pid},' not in listener:
                raise RuntimeError(f'{env} API PID 未监听记录的端口')
        identity = process_env(pid).get('MULTICA_ENV_ID')
        if identity and identity != env:
            raise RuntimeError(f'{env} {role} PID 环境身份不匹配')

def owned_stop(pid, role, expected_cwd):
    if not exists(pid): return
    cwd=Path(f'/proc/{pid}/cwd').resolve()
    if cwd != Path(expected_cwd).resolve(): raise RuntimeError(f'{role} PID {pid} 工作目录不匹配，拒绝停止')
    os.kill(pid,signal.SIGTERM)
    deadline=time.monotonic()+30
    while exists(pid) and time.monotonic()<deadline: time.sleep(.2)
    if exists(pid):
        # Next 关闭监听后可能仍保留后台句柄；Daemon 绝不强制结束。
        if role=='web': os.kill(pid,signal.SIGKILL)
        else: raise RuntimeError(f'{role} 未完成退出，发布终止')

def start(cmd,cwd,env,log):
    with open(log,'ab') as f:
        p=subprocess.Popen([str(x) for x in cmd],cwd=cwd,env=env,stdin=subprocess.DEVNULL,stdout=f,stderr=f,start_new_session=True)
    return p.pid

def drain(env,m,timeout=900):
    h=http(health_url(env)+'/health')
    if h['profile'] != m['daemon_profile'] or h['pid'] != m['pids']['daemon']: raise RuntimeError('Daemon 所属环境不匹配')
    try:
        http(health_url(env)+'/drain','POST')
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            s=http(health_url(env)+'/drain')
            if s['ready_to_stop']: return True
            print(env,'等待任务完成',s,flush=True);time.sleep(min(2,max(0,deadline-time.monotonic())))
        raise RuntimeError('等待已有任务完成超时')
    except BaseException:
        http(health_url(env)+'/drain','DELETE')
        print('已恢复接单',flush=True)
        raise

def launch(env, release, cfg, m):
    result={}
    m['pids']=result
    result['server']=start([release/'server/bin/server'],release,cfg['server'],m['log_paths']['server'])
    ready(m['backend_url']+'/health')
    webcmd=['node',release/'apps/web/server.js'] if (release/'apps/web/server.js').exists() else ['node',release/'apps/web/node_modules/next/dist/bin/next','start','-p',str(m['frontend_port']),'-H','0.0.0.0']
    result['web']=start(webcmd,release/'apps/web',cfg['web'],m['log_paths']['web'])
    ready(m['frontend_url']+'/login')
    webcfg=http(m['frontend_url']+'/api/config')
    if webcfg.get('environment_id','')!=cfg['server'].get('MULTICA_ENV_ID',''): raise RuntimeError('Web/API 环境身份不一致')
    # 以 CLI 管理的 profile 启动；PID 来自健康端点，不猜测子进程。
    run([release/'server/bin/multica','--profile',m['daemon_profile'],'daemon','start','--no-auto-update','--no-auto-reload'],cwd=release,env=cfg['daemon'],stdout=subprocess.DEVNULL)
    h=ready(health_url(env)+'/health')
    if h['profile']!=m['daemon_profile']: raise RuntimeError('Daemon profile 不匹配')
    result['daemon']=h['pid']
    deadline=time.monotonic()+60
    while h.get('status')!='running' and time.monotonic()<deadline:
        time.sleep(.5); h=http(health_url(env)+'/health')
    if h.get('status')!='running': raise RuntimeError('Daemon 未就绪')
    result['daemon']=h['pid']
    return result

def deploy(env,release_id,timeout=900):
    require_environment(env)
    if os.environ.get('MULTICA_TASK_ID'): raise RuntimeError('任务内请使用 request，不能同步等待自身结束')
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',release_id): raise RuntimeError('非法版本')
    with lock(env):
        artifact=RUN/'artifacts'/release_id
        manifest=read(artifact/'release.json')
        if checksums(artifact)!=manifest['files']: raise RuntimeError('发布包内容被修改')
        cfg=read(config_path(env)); m=read(meta_path(env))
        check_process_ownership(env,m)
        isolation_check(config_only=True)
        if m.get('release_id')==release_id:
            isolation_check()
            print(env,'版本已运行，无需重启',release_id)
            return
        release=RUN/'releases'/env/release_id
        if release.exists():
            # 发布目录中的 .next/cache 为环境独立的可变缓存；只核验包内文件。
            current=checksums(release)
            if any(current.get(k)!=v for k,v in manifest['files'].items()): raise RuntimeError('已安装版本文件被修改')
        else: copy_tree(artifact,release)
        prior=json.loads(json.dumps(m)); oldcfg={r:process_env(pid) for r,pid in m['pids'].items()}
        write(RUN/'env'/f'{env}-before-deploy.json',oldcfg,True)
        old_cwds={r:str(Path(f'/proc/{pid}/cwd').resolve()) for r,pid in m['pids'].items()}
        drain(env,m,timeout)
        offsets={role:Path(log).stat().st_size if Path(log).exists() else 0 for role,log in m['log_paths'].items()}
        try:
            http(health_url(env)+'/shutdown','POST')
            deadline=time.monotonic()+60
            while exists(m['pids']['daemon']) and time.monotonic()<deadline: time.sleep(.2)
            if exists(m['pids']['daemon']): raise RuntimeError('Daemon 未退出，拒绝强杀')
            owned_stop(m['pids']['web'],'web',old_cwds['web'])
            owned_stop(m['pids']['server'],'server',old_cwds['server'])
            m['pids']=launch(env,release,cfg,m)
        except Exception as failure:
            # 启动失败时只回收本次创建的进程，并恢复切换前的程序与运行配置。
            for role,pid in list(m['pids'].items()):
                if pid not in prior['pids'].values() and exists(pid):
                    owned_stop(pid,role,str(Path(f'/proc/{pid}/cwd').resolve()))
            if exists(prior['pids']['daemon']):
                try: http(health_url(env)+'/drain','DELETE')
                except Exception: pass
            else:
                oldrelease=Path(old_cwds['server'])
                for role in ('web','server'):
                    if exists(prior['pids'][role]): owned_stop(prior['pids'][role],role,old_cwds[role])
                prior['pids']=launch(env,oldrelease,oldcfg,prior)
            write(meta_path(env),prior)
            isolation_check()
            print('发布失败，已恢复切换前版本；配置与数据未重置。',file=sys.stderr)
            raise
        m.update(release_id=release_id,release_dir=str(release),previous_release_id=prior.get('release_id') if prior.get('release_id')!=release_id else prior.get('previous_release_id'),commit=manifest['source_commit'],source_digest=manifest['source_digest'],deployed_at=time.strftime('%FT%TZ',time.gmtime()))
        m.update(build_version=release_id,frontend_mode='standalone',runtime_config_file=str(config_path(env)),binary_versions={'multica':output([release/'server/bin/multica','version'])})
        m['log_offsets']=offsets
        m.pop('role_release_dirs', None)
        write(RUN/'deployments'/f'{env}-previous.json',prior)
        write(meta_path(env),m)
        # 旧审计工具继续读取 .env；它是运行配置的派生产物，不再承担生成真源。
        legacy=RUN/'env'/f'goal-test-{env}.env'
        keys={line.split('=',1)[0] for line in legacy.read_text().splitlines() if '=' in line and not line.startswith('#')} if legacy.exists() else set()
        values={**cfg['web'],**cfg['server'], 'MULTICA_ENV_ID':env,'CODEBUDDY_CONFIG_DIR':cfg['daemon']['CODEBUDDY_CONFIG_DIR']}
        keys.update(['MULTICA_ENV_ID','CODEBUDDY_CONFIG_DIR'])
        previous={line.split('=',1)[0]:line.split('=',1)[1] for line in legacy.read_text().splitlines() if '=' in line and not line.startswith('#')} if legacy.exists() else {}
        previous.update({k:values[k] for k in keys if k in values})
        legacy.write_text('# Generated from '+str(config_path(env))+'; edit the service configuration instead.\n'+'\n'.join(k+'='+v for k,v in sorted(previous.items()))+'\n');legacy.chmod(0o600)
        for role,pid in m['pids'].items(): (RUN/f'{env}-{role}.pid').write_text(str(pid)+'\n')
        print(env,'已发布',release_id)

def isolation_check(config_only=False):
    cfg={e:read(config_path(e)) for e in ENVS if config_path(e).exists()}
    if any(e not in cfg for e in ENVS): raise RuntimeError('缺少 int 或 prod 配置；请先初始化联调环境')
    checks={}
    for e,c in cfg.items():
        checks[e+'_environment']=all(x.get('MULTICA_ENV_ID')==e for x in c.values())
        checks[e+'_upstream']=c['web']['REMOTE_API_URL'].rstrip('/')=='http://127.0.0.1:'+c['server']['PORT']
        if not config_only:
            m=read(meta_path(e)); check_process_ownership(e,m); checks[e+'_api']=http(m['frontend_url']+'/api/config').get('environment_id')==e
            for role,keys in {'server':['MULTICA_ENV_ID','JWT_SECRET','DATABASE_URL','LOCAL_UPLOAD_DIR','PORT','PPROF_PORT'],'web':['MULTICA_ENV_ID','REMOTE_API_URL','PORT'],'daemon':['MULTICA_ENV_ID','CODEBUDDY_CONFIG_DIR','MULTICA_ENV_WRITABLE_ROOT','TMPDIR']}.items():
                actual=process_env(m['pids'][role]);checks[e+'_'+role+'_live_config']=all(actual.get(k)==c[role].get(k) for k in keys)
            h=http(health_url(e)+'/health'); p=read(m['daemon_profile_path'])
            checks[e+'_daemon']=h['profile']==m['daemon_profile'] and h['server_url'].rstrip('/')==m['backend_url'] and p['workspaces_root']==m['daemon_workspaces_root']
            checks[e+'_release']=all(str(Path(f'/proc/{pid}/cwd').resolve()).startswith(m['release_dir']+'/') or str(Path(f'/proc/{pid}/cwd').resolve())==m['release_dir'] for pid in m['pids'].values())
    ports=[c[r][k] for c in cfg.values() for r,k in [('server','PORT'),('server','PPROF_PORT'),('web','PORT')]]
    checks['ports_distinct']=len(set(ports))==len(ports)
    print(json.dumps(checks,ensure_ascii=False))
    if not all(checks.values()): raise RuntimeError('生产运行检查失败')

def rollback(timeout=900):
    version=read(meta_path('prod')).get('previous_release_id')
    if not version or not (RUN/'artifacts'/version).exists(): raise RuntimeError('没有可回滚的发布包')
    deploy('prod',version,timeout)

def verify_browser(env='prod'):
    child_env=os.environ.copy(); child_env['GOAL_TEST_ENV']=env
    run(['node',ROOT/'scripts/verify-release-browser.mjs'],cwd=ROOT,env=child_env)

def publish(env, version, timeout=900):
    prior=read(meta_path(env))['release_id']
    deploy(env,version,timeout)
    try:
        verify_browser(env)
    except Exception:
        if prior!=version:
            deploy(env,prior,timeout)
            verify_browser(env)
        raise RuntimeError(f'{env} 业务验收失败；已恢复并复查切换前程序，数据库未回滚')

def request_path(request_id):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',request_id): raise RuntimeError('非法请求编号')
    return RUN/'publish-requests'/f'{request_id}.json'

def request_release(version,timeout=900,retry=False):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',version): raise RuntimeError('非法版本')
    manifest=read(RUN/'artifacts'/version/'release.json')
    if checksums(RUN/'artifacts'/version)!=manifest['files']: raise RuntimeError('发布包校验失败')
    with lock('publish-request'):
        request_id=version
        target=request_path(request_id)
        if target.exists():
            existing=read(target)
            if not retry or existing['status'] not in ('failed','rolled_back'):
                print(json.dumps(existing,ensure_ascii=False));return existing
        code=Path(__file__).read_bytes()
        runner=RUN/'publisher'/hashlib.sha256(code).hexdigest()/'release.py'
        runner.parent.mkdir(parents=True,exist_ok=True)
        if not runner.exists(): runner.write_bytes(code);runner.chmod(0o700)
        unit='multica-publish-'+request_id+'-'+str(time.time_ns())
        record=dict(id=request_id,release_id=version,status='queued',stage='int',unit=unit,requested_at=time.strftime('%FT%TZ',time.gmtime()),requested_by_task=os.environ.get('MULTICA_TASK_ID',''),timeout=timeout)
        write(target,record,True)
        try:
            # systemd owns the publisher independently of the task's process group and cgroup.
            run(['systemd-run','--quiet','--collect','--unit',unit,'--property=Type=exec','--property=KillMode=process',
                 '--working-directory',ROOT,'--setenv=MULTICA_RELEASE_ROOT='+str(ROOT),
                 sys.executable,runner,'worker',request_id])
        except Exception:
            record.update(status='failed',error='外部发布进程未启动');write(target,record,True);raise
        print(json.dumps(record,ensure_ascii=False));return record

def worker(request_id):
    if os.environ.get('MULTICA_TASK_ID'): raise RuntimeError('任务内只能 request；worker 必须由外部进程启动')
    target=request_path(request_id);record=read(target)
    log=target.with_suffix('.log')
    with log.open('a') as stream,contextlib.redirect_stdout(stream),contextlib.redirect_stderr(stream):
        try:
            with lock('publisher',wait=True):
                prior=read(meta_path('prod'))['release_id']
                record.update(status='running',previous_release_id=prior,publisher_pid=os.getpid())
                write(target,record,True)
                version=record['release_id']
                record.update(stage='deploying_int');write(target,record,True)
                deploy('int',version,record['timeout'])
                record.update(stage='verifying_int');write(target,record,True)
                verify_browser('int')
                os.environ['MULTICA_TASK_COMPLETED']='1'
                os.environ['MULTICA_REQUESTED_TASK_ID']=record.get('requested_by_task','') or 'manual-release-request'
                run(['node',ROOT/'scripts/verify-integration-release.mjs',version],cwd=ROOT)
                record.update(stage='deploying_prod');write(target,record,True)
                publish('prod',version,record['timeout'])
                record.update(status='succeeded',stage='succeeded',result='联调验收及生产发布通过')
        except Exception as error:
            current=read(meta_path('prod'))['release_id']
            record.update(status='rolled_back' if '业务验收失败；已恢复' in str(error) else 'failed',error=str(error),current_release_id=current)
        record['finished_at']=time.strftime('%FT%TZ',time.gmtime());write(target,record,True)

def request_status(request_id):
    target=request_path(request_id);record=read(target)
    if record['status'] in ('queued','running'):
        active=subprocess.run(['systemctl','is-active','--quiet',record['unit']]).returncode==0
        if not active:
            record.update(status='failed',error='外部发布进程已退出且没有最终结果；检查请求日志后显式重试')
            write(target,record,True)
    print(json.dumps(record,ensure_ascii=False))

def verify_logs():
    env=os.environ.get('GOAL_TEST_ENV','prod'); require_environment(env)
    m=read(meta_path(env));results={}
    for role,path in m['log_paths'].items():
        p=Path(path)
        with p.open('rb') as f:
            f.seek(m.get('log_offsets',{}).get(role,0));data=f.read().decode(errors='replace')
        errors=[line for line in data.splitlines() if re.search(r'panic:|fatal error:|UnhandledPromiseRejection|EADDRINUSE|Cannot find module',line)]
        results[role]={'exists':True,'fatal_errors':len(errors)}
    ok=all(not v['fatal_errors'] for v in results.values())
    print(json.dumps(dict(environment=env,release_id=m['release_id'],services=results,ok=ok)))
    if not ok: raise RuntimeError('发布日志存在致命错误；请在私有日志中查看')

def backup():
    cfg=read(config_path('prod'));url=urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
    database=url.path.lstrip('/')
    destination=RUN/'backups'/('prod-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime())+'.dump')
    destination.parent.mkdir(parents=True,exist_ok=True)
    env=os.environ.copy();env['PGPASSWORD']=urllib.parse.unquote(url.password or '')
    args=['pg_dump','--format=custom','--host',url.hostname,'--port',url.port or '5432','--username',urllib.parse.unquote(url.username or ''),database]
    if not shutil.which('pg_dump'):
        containers=json.loads(output(['docker','inspect',*output(['docker','ps','-q']).splitlines()]))
        matches=[c for c in containers if url.hostname in ('localhost','127.0.0.1') and any(b['HostPort']==str(url.port or 5432) and b['HostIp'] in ('127.0.0.1','0.0.0.0','') for b in (c['NetworkSettings']['Ports'].get('5432/tcp') or []))]
        if len(matches)!=1: raise RuntimeError('缺少 pg_dump，无法唯一识别目标 PostgreSQL 容器')
        args=['docker','exec','-e','PGPASSWORD',matches[0]['Id'],'pg_dump','--format=custom','--host','127.0.0.1','--username',urllib.parse.unquote(url.username or ''),database]
    with destination.open('xb') as stream:
        destination.chmod(0o600)
        run(args,env=env,stdout=stream)
    print(json.dumps(dict(database=database,backup=str(destination),bytes=destination.stat().st_size)))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['build','deploy','request','request-status','worker','rollback','verify','status','verify-logs','backup','initialize','reset'])
    parser.add_argument('target',nargs='?')
    parser.add_argument('legacy_release',nargs='?')
    parser.add_argument('--timeout',type=float,default=900)
    parser.add_argument('--retry',action='store_true')
    parser.add_argument('--env',choices=ENVS,default='prod')
    a=parser.parse_args()
    if a.timeout<=0: parser.error('timeout 必须大于 0')
    if a.command=='initialize': raise RuntimeError('请使用联调 bootstrap 入口；生产禁止重新初始化')
    target=a.legacy_release if a.target in ENVS else a.target
    if a.command=='build': build()
    elif a.command=='verify': isolation_check()
    elif a.command=='status':
        isolation_check();m=read(meta_path('prod'));print(json.dumps({k:m[k] for k in ('release_id','previous_release_id','frontend_url','backend_url','pids','deployed_at')},ensure_ascii=False))
    elif a.command=='verify-logs': os.environ['GOAL_TEST_ENV']=a.env; verify_logs()
    elif a.command=='backup': backup()
    elif a.command=='reset':
        if a.env != 'int': raise RuntimeError('只允许重置 int')
        run(['node',ROOT/'scripts/reset-integration.mjs'],cwd=ROOT)
    elif a.command=='rollback': rollback(a.timeout);verify_browser('prod')
    elif a.command in ('deploy','request'):
        if not target: parser.error('必须指定发布包版本')
        if a.command=='request': request_release(target,a.timeout,a.retry)
        else: publish(a.target if a.target in ENVS else 'prod',target,a.timeout)
    elif a.command=='worker': worker(target)
    elif a.command=='request-status': request_status(target)

if __name__=='__main__': main()
