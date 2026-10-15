#!/usr/bin/env python3
"""固定 int 验证后更新 prod；Git 保存历史，运行目录仅在切换期间保留旧副本。"""
import argparse, contextlib, fcntl, hashlib, json, os, re, shutil, signal, subprocess, sys, time, urllib.request, urllib.error, urllib.parse
from pathlib import Path

ROOT = Path(os.environ.get('MULTICA_RELEASE_ROOT', Path(__file__).resolve().parent.parent)).resolve()
RUN = ROOT / '.run'
ENVS = ('int', 'prod')

class RecoveryRequired(RuntimeError):
    """自动恢复未完成，禁止后续更新覆盖恢复资料。"""


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
    names = sorted(set(subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=root).decode().split('\0')))
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

def checked_source(source):
    source = Path(source).resolve()
    if Path(output(['git', 'rev-parse', '--show-toplevel'], cwd=source)).resolve() != source:
        raise RuntimeError('必须指定 Git checkout 根目录')
    if not (source/'server/cmd/server').is_dir() or not (source/'apps/web').is_dir():
        raise RuntimeError('构建来源不是 Multica 仓库')
    if output(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=source):
        raise RuntimeError('候选必须是干净提交；先保存改动再联调')
    for directory in (source, source/'apps/web'):
        for name in ('.env','.env.local','.env.production','.env.production.local'):
            if (directory/name).exists():
                raise RuntimeError('构建目录含本地环境文件，请将环境配置放在固定运行配置中: '+str(directory/name))
    sha = output(['git', 'rev-parse', 'HEAD'], cwd=source)
    published = RUN/'published.json'
    if published.exists():
        baseline = read(published)['commit']
        if subprocess.run(['git', 'merge-base', '--is-ancestor', baseline, sha], cwd=source).returncode:
            raise RuntimeError('候选未包含最新已发布提交，请先整合代码')
    else:
        # 首次切换必须证明旧生产记录对应真实 Git 内容，不能以一个 SHA 掩盖脏快照。
        old = read(meta_path('prod'))
        baseline = old['commit']
        if subprocess.run(['git', 'merge-base', '--is-ancestor', baseline, sha], cwd=source).returncode:
            raise RuntimeError('候选未包含现有生产提交')
        if git_digest(source, baseline) != old['source_digest']:
            raise RuntimeError('现有生产包含未提交内容；请先核对并保存生产源码基线')
    return source, sha

def establish_baseline(source):
    source=Path(source).resolve()
    if (RUN/'published.json').exists(): raise RuntimeError('已存在生产源码基线')
    if output(['git','status','--porcelain','--untracked-files=all'],cwd=source): raise RuntimeError('基线 checkout 必须干净')
    sha=output(['git','rev-parse','HEAD'],cwd=source)
    digest=source_digest(source)[0]
    if digest != read(meta_path('prod'))['source_digest']:
        raise RuntimeError('基线源码与当前生产构建内容不一致')
    write(RUN/'published.json',{'commit':sha,'source_digest':digest},True)

def git_digest(source, commit):
    names = output(['git', 'ls-tree', '-r', '--name-only', commit], cwd=source).splitlines()
    digest = hashlib.sha256()
    for name in sorted(names):
        if name.startswith('.run/'): continue
        digest.update(name.encode()+b'\0')
        digest.update(subprocess.check_output(['git', 'show', commit+':'+name], cwd=source))
    return digest.hexdigest()

def idle_slot(env):
    current = Path(read(meta_path(env))['release_dir']).resolve()
    slots = [RUN/'runtime'/env/name for name in ('a', 'b')]
    target = next(p for p in slots if p.resolve() != current)
    assert_unused(target)
    if target.exists(): shutil.rmtree(target)
    return target

def assert_unused(directory):
    directory = Path(directory).resolve()
    for proc in Path('/proc').glob('[0-9]*'):
        for name in ('cwd', 'exe'):
            try:
                resolved = (proc/name).resolve(strict=True)
                if resolved == directory or directory in resolved.parents:
                    raise RuntimeError('目录仍被进程使用: '+str(directory))
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                pass


def build(source, target):
    source, sha = checked_source(source)
    digest, files = source_digest(source)
    groups = {'go': hashlib.sha256(), 'web': hashlib.sha256()}
    for name in files:
        if name.startswith(('docs/','apps/docs/','.github/','.agents/')) or ('/' not in name and name.endswith('.md')):
            continue
        # 未知根目录输入同时影响两组；宁可多编译，不误复用。
        selected = ['go'] if name.startswith('server/') else ['web'] if name.startswith(('apps/', 'packages/')) else ['go', 'web']
        for group in selected:
            groups[group].update(name.encode()+b'\0'+(source/name).read_bytes())
    inputs = {name: value.hexdigest() for name, value in groups.items()}
    current = Path(read(meta_path('int'))['release_dir'])
    previous = read(current/'release.json') if (current/'release.json').exists() else {}
    # 运行目录只允许增加缓存，包内源码与程序必须仍然匹配。
    current_files = checksums(current) if previous.get('inputs') else {}
    reusable = all(current_files.get(k) == v for k, v in previous.get('files', {}).items())
    target.mkdir(parents=True)
    env = os.environ.copy()
    for key in list(env):
        if key.startswith(('NEXT_PUBLIC_', 'MULTICA_', 'GOAL_TEST_')) or key in ('REMOTE_API_URL','FRONTEND_ORIGIN','DATABASE_URL','JWT_SECRET'): env.pop(key)
    env.update(STANDALONE='true', NEXT_PUBLIC_APP_VERSION=sha[:12], NEXT_TELEMETRY_DISABLED='1')
    if reusable and previous.get('inputs', {}).get('web') == inputs['web']:
        copy_tree(current, target, excludes=('/server/', '/release.json', '**/.next/cache/***'))
    else:
        run(['pnpm', 'install', '--frozen-lockfile'], cwd=source, env=env)
        run(['pnpm', '--filter', '@multica/web', 'build'], cwd=source, env=env)
        copy_tree(source/'apps/web/.next/standalone', target)
        copy_tree(source/'apps/web/.next/static', target/'apps/web/.next/static')
        copy_tree(source/'apps/web/public', target/'apps/web/public')
    if reusable and previous.get('inputs', {}).get('go') == inputs['go']:
        copy_tree(current/'server/bin', target/'server/bin')
    else:
        (target/'server/bin').mkdir(parents=True, exist_ok=True)
        for binary in ('server', 'multica', 'migrate'):
            run(['go', 'build', '-ldflags', f'-X main.version={sha[:12]} -X main.commit={sha}', '-o', target/'server/bin'/binary, './cmd/'+binary], cwd=source/'server', env=env)
    copy_tree(source/'server/migrations', target/'server/migrations')
    if checked_source(source)[1] != sha or source_digest(source)[0] != digest:
        raise RuntimeError('构建期间源码变化，请重新联调')
    manifest = dict(id=sha, source_commit=sha, source_directory=str(source), source_digest=digest,
                    inputs=inputs, files=checksums(target))
    write(target/'release.json', manifest)
    for cache in (source/'apps/web/.next/cache', source/'.turbo'):
        if cache.exists() and sum(p.stat().st_size for p in cache.rglob('*') if p.is_file() and not p.is_symlink()) > 1024**3:
            shutil.rmtree(cache)
    return manifest

def health_url(env):
    profile=read(meta_path(env))['daemon_profile']
    port=19514 if not profile else 19515 + sum(profile.encode()) % 1000
    return f'http://127.0.0.1:{port}'

def check_process_ownership(env, metadata, allow_unhealthy=False):
    release = Path(metadata.get('release_dir', ROOT)).resolve()
    for role, pid in metadata['pids'].items():
        if allow_unhealthy and not exists(pid):
            continue
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
        elif not allow_unhealthy:
            listener = output(['ss', '-ltnp', f"( sport = :{metadata['frontend_port']} )"])
            if f'pid={pid},' not in listener:
                raise RuntimeError(f'{env} Web PID 未监听记录的端口')
        if role == 'server' and not allow_unhealthy:
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
    # 服务已停止后才整理日志，避免移动仍被写入的文件。
    logfile=Path(log)
    if logfile.exists() and logfile.stat().st_size > 64*1024*1024:
        with logfile.open('rb') as stream:
            stream.seek(-16*1024*1024,2); tail=stream.read()
        logfile.write_bytes(tail)
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

def migrate(release, cfg, action='up', previous=None):
    args=[Path(release)/'server/bin/migrate', action]
    if action=='up':
        if previous is None: raise RuntimeError('迁移需要切换前版本作为基线')
        old={p.name for p in (Path(previous)/'server/migrations').glob('*.up.sql')}
        versions=sorted(p.name.removesuffix('.up.sql') for p in (Path(release)/'server/migrations').glob('*.up.sql') if p.name not in old)
        if not versions: return
        args.extend(['--versions', ','.join(versions)])
    run(args, cwd=Path(release)/'server', env=cfg['server'])

def repository_transition_needed(previous, target):
    migration='server/migrations/525_repository_migration_guard.up.sql'
    return (Path(target)/migration).exists() and not (Path(previous)/migration).exists()

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

def deploy(env, release, timeout=900):
    require_environment(env)
    release = Path(release).resolve()
    manifest = read(release/'release.json')
    release_id = manifest['id']
    actual = checksums(release)
    if any(actual.get(k) != v for k,v in manifest['files'].items()): raise RuntimeError('运行文件校验失败')
    with lock(env):
        cfg=read(config_path(env)); m=read(meta_path(env))
        check_process_ownership(env,m)
        isolation_check(config_only=True)
        if m.get('release_id')==release_id:
            isolation_check()
            print(env,'版本已运行，无需重启',release_id)
            return
        prior=json.loads(json.dumps(m))
        if env=='prod':
            prior['published_before']=read(RUN/'published.json') if (RUN/'published.json').exists() else None
        oldcfg={r:process_env(pid) for r,pid in m['pids'].items()}
        write(RUN/'env'/f'{env}-before-deploy.json',oldcfg,True)
        old_cwds={r:str(Path(f'/proc/{pid}/cwd').resolve()) for r,pid in m['pids'].items()}
        write(RUN/'deployments'/f'{env}-previous.json', prior, True)
        prior['repository_transition']=repository_transition_needed(old_cwds['server'],release)
        write(RUN/'deployments'/f'{env}-previous.json', prior, True)
        drain(env,m,timeout)
        offsets={role:Path(log).stat().st_size if Path(log).exists() else 0 for role,log in m['log_paths'].items()}
        try:
            http(health_url(env)+'/shutdown','POST')
            deadline=time.monotonic()+60
            while exists(m['pids']['daemon']) and time.monotonic()<deadline: time.sleep(.2)
            if exists(m['pids']['daemon']): raise RuntimeError('Daemon 未退出，拒绝强杀')
            owned_stop(m['pids']['web'],'web',old_cwds['web'])
            owned_stop(m['pids']['server'],'server',old_cwds['server'])
            migrate(release,cfg,previous=old_cwds['server'])
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
                if prior.get('repository_transition'): migrate(release,cfg,'restore-repositories')
                prior['pids']=launch(env,oldrelease,oldcfg,prior)
            write(meta_path(env),prior)
            isolation_check()
            print('发布失败，已恢复切换前版本；配置与数据未重置。',file=sys.stderr)
            raise
        m.update(release_id=release_id,release_dir=str(release),previous_release_id=prior.get('release_id') if prior.get('release_id')!=release_id else prior.get('previous_release_id'),commit=manifest['source_commit'],source_digest=manifest['source_digest'],deployed_at=time.strftime('%FT%TZ',time.gmtime()))
        m.update(build_version=release_id,frontend_mode='standalone',runtime_config_file=str(config_path(env)),binary_versions={'multica':output([release/'server/bin/multica','version'])})
        m['log_offsets']={role: min(offsets[role],16*1024*1024) if offsets[role]>64*1024*1024 and role in ('server','web') else offsets[role] for role in offsets}
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
        print(env,'已切换',release_id)

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

def verify_browser(env='prod'):
    child_env=os.environ.copy(); child_env['GOAL_TEST_ENV']=env
    run(['node',ROOT/'scripts/verify-release-browser.mjs'],cwd=ROOT,env=child_env)

def restore(env, timeout=900):
    prior = read(RUN/'deployments'/f'{env}-previous.json')
    current = read(meta_path(env))
    old = Path(prior['release_dir'])
    if old.resolve() == Path(current['release_dir']).resolve(): return
    if not old.exists(): raise RuntimeError('临时恢复目录已删除；请从 Git 重新构建')
    # 旧安装也可能没有新格式 manifest，恢复只使用切换前的记录与配置。
    check_process_ownership(env, current, allow_unhealthy=True)
    daemon_pid=current['pids']['daemon']
    if exists(daemon_pid):
        drain(env, current, timeout)
        http(health_url(env)+'/shutdown', 'POST')
        deadline=time.monotonic()+60
        while exists(daemon_pid) and time.monotonic()<deadline: time.sleep(.2)
        if exists(daemon_pid): raise RuntimeError('Daemon 未退出，无法恢复')
    for role in ('web', 'server'):
        cwd=Path(current['release_dir'])/ 'apps/web' if role=='web' else Path(current['release_dir'])
        owned_stop(current['pids'][role], role, cwd)
    if prior.get('repository_transition'): migrate(Path(current['release_dir']),read(config_path(env)),'restore-repositories')
    baseline=prior.pop('published_before',None)
    prior['pids']=launch(env, old, read(config_path(env)), prior)
    write(meta_path(env), prior)
    if env=='prod':
        if baseline is not None: write(RUN/'published.json',baseline,True)
        elif (RUN/'published.json').exists(): (RUN/'published.json').unlink()
    verify_browser(env)

def discard_previous(env):
    file=RUN/'deployments'/f'{env}-previous.json'
    if not file.exists(): return
    prior=Path(read(file)['release_dir']).resolve()
    current=Path(read(meta_path(env))['release_dir']).resolve()
    # 首次接管留下的历史目录由单独授权的清理处理。
    if prior != current and prior.parent == (RUN/'runtime'/env).resolve():
        assert_unused(prior)
        if prior.exists(): shutil.rmtree(prior)
    file.unlink()
    metadata=read(meta_path(env));metadata.pop('previous_release_id',None);write(meta_path(env),metadata)

def publish(env, directory, timeout=900):
    try:
        deploy(env, directory, timeout)
    except Exception:
        try:
            check_process_ownership(env, read(meta_path(env)))
        except Exception as recovery_error:
            raise RecoveryRequired(f'{env} 启动或恢复未完成: {recovery_error}') from recovery_error
        raise
    try:
        verify_browser(env)
        verify_logs(env)
        if read(RUN/'deployments'/f'{env}-previous.json').get('repository_transition'):
            migrate(directory,read(config_path(env)),'accept-repositories')
    except Exception:
        try:
            restore(env, timeout)
        except Exception as recovery_error:
            raise RecoveryRequired(f'{env} 验收失败且恢复未完成: {recovery_error}') from recovery_error
        raise

def candidate_path(): return RUN/'candidate.json'

def candidate(version=None):
    record=read(candidate_path())
    if version and record['id'] != version: raise RuntimeError('int 已由其他候选占用')
    return record

def prepare_int(source, timeout=900):
    if os.environ.get('MULTICA_TASK_ID') and os.environ.get('MULTICA_SERVER_URL','').rstrip('/') == read(meta_path('int'))['backend_url'].rstrip('/'):
        raise RuntimeError('int 中运行的任务不能同步更新自身，请从生产运行时发起')
    # 生产升级需要等待所有运行退出，不能让其他运行阻塞在联调锁上。
    try:
        with lock('publisher'):
            active=candidate() if candidate_path().exists() else None
            if active and active['status'] not in ('released','succeeded','failed'):
                raise RuntimeError('int 正被其他候选占用；结束当前运行后重试，不能同步等待生产发布')
            source, sha=checked_source(source)
            version=sha+'-'+str(time.time_ns())
            record=dict(id=version, commit=sha, source=str(source),
                        git_common=output(['git','rev-parse','--path-format=absolute','--git-common-dir'],cwd=source),
                        branch=output(['git','symbolic-ref','HEAD'],cwd=source), status='building',
                        task_id=os.environ.get('MULTICA_TASK_ID',''), server_url=os.environ.get('MULTICA_SERVER_URL',''),
                        workspace_id=os.environ.get('MULTICA_WORKSPACE_ID',''))
            write(candidate_path(), record, True)
            try:
                directory=idle_slot('int')
                manifest=build(source, directory)
                publish('int', directory, timeout)
                record.update(status='verifying', release_id=manifest['id'], source_digest=manifest['source_digest'],
                              deployed_at=read(meta_path('int'))['deployed_at'])
                discard_previous('int')
            except Exception as error:
                record.update(status='recovery_required' if isinstance(error,RecoveryRequired) else 'failed', error=str(error))
                raise
            finally: write(candidate_path(), record, True)
            print(json.dumps(record,ensure_ascii=False));return record
    except BlockingIOError:
        raise RuntimeError('其他候选正在构建或发布；结束当前运行后重试') from None

def validate_candidate(record, allow_finalized=False):
    current=read(meta_path('int'))
    if any(current.get(k) != record.get(k) for k in ('release_id','source_digest','deployed_at')):
        raise RuntimeError('int 在验收后发生变化，必须重新验证')
    if Path(record['source']).exists():
        source, sha=checked_source(record['source'])
        if sha != record['commit'] or source_digest(source)[0] != record['source_digest']:
            raise RuntimeError('候选源码已变化，必须重新联调')
    elif not allow_finalized:
        raise RuntimeError('任务源码目录不存在')
    common=record['git_common']
    if output(['git','--git-dir',common,'rev-parse',record['branch']]) != record['commit']:
        raise RuntimeError('候选分支发生变化')
    published=RUN/'published.json'
    if published.exists() and subprocess.run(['git','--git-dir',common,'merge-base','--is-ancestor',read(published)['commit'],record['commit']]).returncode:
        raise RuntimeError('候选未包含最新生产提交')
    return current

def accept(version, evidence_file):
    with lock('publisher', wait=True):
        record=candidate(version)
        if record['status'] != 'verifying': raise RuntimeError('候选不在功能验证阶段')
        validate_candidate(record)
        evidence=read(evidence_file)
        if evidence.get('candidate') != version or not evidence.get('summary') or not evidence.get('checks'):
            raise RuntimeError('验收记录需要 candidate、summary 和 checks')
        if any(not isinstance(c,dict) or not c.get('name') or c.get('status')!='passed' or not c.get('evidence') for c in evidence['checks']):
            raise RuntimeError('每项功能检查需要 name、passed 状态和实际 evidence')
        record.update(status='accepted', acceptance=evidence)
        write(candidate_path(), record, True)

def release_candidate(version):
    with lock('publisher', wait=True):
        record=candidate(version)
        if record['status'] in ('queued','publishing'): raise RuntimeError('生产更新已提交，不能释放')
        if record['status']=='recovery_required':
            isolation_check()
            published=read(RUN/'published.json')
            current=read(meta_path('prod'))
            if published['commit'] != current['commit'] or published['source_digest'] != current['source_digest']:
                raise RuntimeError('生产尚未恢复到已发布基线')
        record.update(status='released');write(candidate_path(),record,True)

def request_path(request_id):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',request_id): raise RuntimeError('非法请求编号')
    return RUN/'publish-requests'/f'{request_id}.json'

def request_release(version,timeout=900):
    with lock('publisher',wait=True):
        record=candidate(version)
        target=request_path(version)
        if target.exists():
            previous=read(target)
            if previous['status'] in ('queued','publishing','succeeded'):
                print(json.dumps(previous,ensure_ascii=False));return previous
            raise RuntimeError('上次生产更新未成功，请重新联调')
        if record['status'] != 'accepted': raise RuntimeError('先完成本次功能验收并 accept')
        validate_candidate(record)
        # 发布来源为提交请求的这轮运行，可能与开始联调的运行不同。
        record.update(task_id=os.environ.get('MULTICA_TASK_ID',''), server_url=os.environ.get('MULTICA_SERVER_URL',''),
                      workspace_id=os.environ.get('MULTICA_WORKSPACE_ID',''), timeout=timeout,
                      status='queued', unit='multica-publish-'+version)
        runner=RUN/'publisher'/version/'release.py'
        runner.parent.mkdir(parents=True,exist_ok=True)
        runner.write_bytes(Path(__file__).read_bytes());runner.chmod(0o700)
        write(target,record,True);write(candidate_path(),record,True)
        try:
            run(['systemd-run','--quiet','--collect','--unit',record['unit'],'--property=Type=exec','--property=KillMode=process',
                 '--working-directory',ROOT,'--setenv=MULTICA_RELEASE_ROOT='+str(ROOT),sys.executable,runner,'worker',version])
        except Exception:
            record.update(status='accepted');write(candidate_path(),record,True)
            target.unlink();raise
        print(json.dumps(record,ensure_ascii=False));return record

def task_api(record, route, body=None):
    url=record.get('server_url','').rstrip('/')
    metadata=next((read(meta_path(e)) for e in ENVS if read(meta_path(e))['backend_url'].rstrip('/')==url),None)
    if not metadata: raise RuntimeError('任务来源不是已配置的 int/prod')
    token=read(metadata['daemon_profile_path'])['token']
    headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'}
    if record.get('workspace_id'): headers['X-Workspace-ID']=record['workspace_id']
    req=urllib.request.Request(url+'/api/daemon/tasks/'+record['task_id']+'/'+route,
                               data=None if body is None else json.dumps(body).encode(),headers=headers)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=15) as response:
        return json.load(response)

def wait_requested_task(record):
    if not record.get('task_id'): return
    deadline=time.monotonic()+record['timeout']
    while time.monotonic()<deadline:
        status=task_api(record,'status')['status']
        if status=='completed': return
        if status in ('failed','cancelled'): raise RuntimeError('发布来源运行未成功完成: '+status)
        time.sleep(2)
    raise RuntimeError('等待发起运行结束超时')

def report_result(record):
    if not record.get('task_id'): return
    marker='生产更新 '+record['id']
    if task_api(record,'status')['status'] not in ('completed','failed','cancelled'):
        raise RuntimeError('来源运行仍在执行，结果回写待重试')
    messages=task_api(record,'messages')
    if isinstance(messages,dict): messages=messages['messages']
    content=marker+'：'+('成功' if record['status']=='succeeded' else '失败：'+record.get('error',''))
    if record.get('cleanup_error'): content+='；清理待处理：'+record['cleanup_error']
    elif record.get('worktree_cleanup'): content+='；工作目录：'+record['worktree_cleanup']
    if any(content == m.get('content','') for m in messages): return
    seq=max((m['seq'] for m in messages),default=0)+1
    task_api(record,'messages',{'messages':[{'seq':seq,'type':'text','content':content}]})

def cleanup_worktree(record):
    source=Path(record['source']).resolve()
    if not source.exists(): return 'already_removed'
    # 普通 checkout、发布控制目录和没有分支引用的目录不属于自动删除对象。
    gitdir=Path(output(['git','rev-parse','--absolute-git-dir'],cwd=source)).resolve()
    common=Path(output(['git','rev-parse','--path-format=absolute','--git-common-dir'],cwd=source)).resolve()
    if source==ROOT or gitdir==common: return 'main_checkout_retained'
    if output(['git','rev-parse','HEAD'],cwd=source)!=record['commit']: raise RuntimeError('worktree 已产生后续提交')
    branch=output(['git','symbolic-ref','--quiet','HEAD'],cwd=source)
    if branch != record['branch'] or str(common) != record['git_common']:
        raise RuntimeError('worktree 归属与发布请求不一致')
    if not branch.startswith('refs/heads/feature/'): raise RuntimeError('不是任务 feature 分支')
    if output(['git','status','--porcelain','--untracked-files=all'],cwd=source): raise RuntimeError('worktree 存在未保存成果')
    published=read(RUN/'published.json')['commit']
    if subprocess.run(['git','--git-dir',common,'merge-base','--is-ancestor',record['commit'],published]).returncode:
        raise RuntimeError('提交不在当前已发布历史中')
    ignored=output(['git','ls-files','--others','--ignored','--exclude-standard'],cwd=source).splitlines()
    for name in ignored:
        parts=Path(name).parts
        if not any(p in ('node_modules','.next','.turbo','__pycache__') for p in parts) and not name.startswith(('server/bin/','server/tmp/','apps/docs/.source/','apps/web/.source/')):
            raise RuntimeError('worktree 存在非构建类忽略文件: '+name)
    assert_unused(source)
    run(['git','--git-dir',common,'worktree','remove',source])
    return 'removed'

def finish_cleanup(record):
    try:
        # 清理重试可能晚于下一次发布，只能处理本次发布的恢复目录。
        if read(RUN/'published.json').get('candidate') == record['id']:
            discard_previous('prod')
        record['worktree_cleanup']=cleanup_worktree(record)
        record.pop('cleanup_error',None)
        runner=RUN/'publisher'/record['id']
        if runner.exists(): shutil.rmtree(runner)
    except Exception as error:
        record['cleanup_error']=str(error)
    write(request_path(record['id']),record,True)
    try:
        report_result(record)
        record.pop('report_error',None)
    except Exception as error:
        record['report_error']=str(error)
    write(request_path(record['id']),record,True)


def worker(request_id):
    if os.environ.get('MULTICA_TASK_ID'): raise RuntimeError('worker 必须由外部进程启动')
    target=request_path(request_id);record=read(target)
    with target.with_suffix('.log').open('a') as stream,contextlib.redirect_stdout(stream),contextlib.redirect_stderr(stream):
        try:
            if record['status']!='succeeded':
                wait_requested_task(record)
            with lock('publisher',wait=True):
                record=read(target)
                if record['status']=='succeeded':
                    finish_cleanup(record);return
                current_record=candidate(request_id)
                if current_record['status']!='queued': raise RuntimeError('候选已释放或更新不在队列中')
                current=validate_candidate(record, allow_finalized=True)
                record.update(status='publishing');write(target,record,True);write(candidate_path(),record,True)
                directory=idle_slot('prod')
                copy_tree(Path(current['release_dir']),directory,excludes=('**/.next/cache/***',))
                publish('prod',directory,record['timeout'])
                write(RUN/'published.json',{'commit':record['commit'],'source_digest':record['source_digest'],'candidate':request_id},True)
                record.update(status='succeeded');write(target,record,True);write(candidate_path(),record,True)
                finish_cleanup(record)
        except Exception as error:
            record.update(status='recovery_required' if isinstance(error,RecoveryRequired) else 'failed',error=str(error))
            write(target,record,True)
            with lock('publisher',wait=True):
                if candidate_path().exists() and candidate()['id']==request_id: write(candidate_path(),record,True)
            try: report_result(record)
            except Exception as report_error:
                record['report_error']=str(report_error);write(target,record,True)


def request_status(request_id):
    record=read(request_path(request_id))
    if record['status'] in ('queued','publishing') and subprocess.run(['systemctl','is-active','--quiet',record['unit']]).returncode:
        record['warning']='外部更新进程已退出；检查日志和运行目录后恢复，不能自动释放联调占用'
    print(json.dumps(record,ensure_ascii=False))


def verify_logs(env='prod'):
    require_environment(env)
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
    cfg=read(config_path('prod'))
    if not cfg['server'].get('S3_BUCKET'):
        run(['node', ROOT/'scripts/backup-skill-assets.mjs', 'prod'])
        return
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
    parser.add_argument('command',choices=['baseline','int','accept','release','request','request-status','worker','cleanup','restore','verify','status','verify-logs','backup','reset'])
    parser.add_argument('target',nargs='?')
    parser.add_argument('--source')
    parser.add_argument('--evidence')
    parser.add_argument('--timeout',type=float,default=900)
    parser.add_argument('--env',choices=ENVS,default='prod')
    a=parser.parse_args()
    if a.timeout<=0: parser.error('timeout 必须大于 0')
    if a.command=='baseline':
        if not a.source: parser.error('baseline 需要 --source 已核对的生产源码')
        with lock('publisher',wait=True): establish_baseline(a.source)
    elif a.command=='int':
        if not a.source: parser.error('int 需要 --source 任务 worktree')
        prepare_int(a.source,a.timeout)
    elif a.command=='accept':
        if not a.target or not a.evidence: parser.error('accept 需要候选编号和 --evidence 文件')
        accept(a.target,a.evidence)
    elif a.command in ('request','request-status','worker','release','cleanup'):
        if not a.target: parser.error('需要候选编号')
        if a.command=='request': request_release(a.target,a.timeout)
        elif a.command=='request-status': request_status(a.target)
        elif a.command=='worker': worker(a.target)
        elif a.command=='release': release_candidate(a.target)
        else:
            with lock('publisher',wait=True):
                record=read(request_path(a.target))
                if record['status']!='succeeded': raise RuntimeError('只清理成功发布的任务')
                finish_cleanup(record)
                print(json.dumps(record,ensure_ascii=False))
    elif a.command=='restore':
        if os.environ.get('MULTICA_TASK_ID'): raise RuntimeError('恢复应由运行环境外部执行')
        with lock('publisher',wait=True): restore(a.env,a.timeout)
    elif a.command=='verify': isolation_check()
    elif a.command=='status':
        print(json.dumps({'environments':{e:{k:read(meta_path(e)).get(k) for k in ('release_id','commit','release_dir')} for e in ENVS},
                          'candidate':candidate() if candidate_path().exists() else None},ensure_ascii=False))
    elif a.command=='verify-logs': verify_logs(a.env)
    elif a.command=='backup': backup()
    elif a.command=='reset':
        if a.env!='int': raise RuntimeError('只允许重置 int')
        run(['node',ROOT/'scripts/reset-integration.mjs'],cwd=ROOT)

if __name__=='__main__': main()
