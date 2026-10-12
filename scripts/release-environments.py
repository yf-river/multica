#!/usr/bin/env python3
"""固定版本发布：配置与数据属于环境，发布包只包含代码和构建产物。"""
import argparse, contextlib, fcntl, hashlib, json, os, re, shutil, signal, subprocess, sys, time, urllib.request, urllib.error
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
RUN = ROOT / '.run'
ENVS = ('int', 'prod')

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
def lock(env):
    p = RUN / 'locks' / f'release-{env}.lock'; p.parent.mkdir(parents=True, exist_ok=True)
    with p.open('a') as f:
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
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

def initialize(env):
    """一次性导入当前运行配置，不重建用户或工作区。"""
    with lock(env):
        if config_path(env).exists(): raise RuntimeError('环境已初始化；发布不会覆盖配置')
        m=read(meta_path(env)); services={role:process_env(m['pids'][role]) for role in ('server','web','daemon')}
        write(RUN/'env'/f'{env}-previous-server.json',services['server'],True)
        services['server']['JWT_SECRET']=os.urandom(32).hex()
        state = RUN/'state'/env; state.mkdir(parents=True,exist_ok=True)
        for role in services:
            tmp=state/'tmp'/role; tmp.mkdir(parents=True,exist_ok=True)
            services[role].update(MULTICA_ENV_ID=env,TMPDIR=str(tmp))
        services['daemon'].update(MULTICA_DAEMON_AUTO_UPDATE='false', MULTICA_DAEMON_AUTO_RELOAD='false')
        services['web'].pop('__NEXT_PRIVATE_STANDALONE_CONFIG',None)
        services['web'].update(PORT=str(m['frontend_port']),HOSTNAME='0.0.0.0')
        # CodeBuddy 的全局配置与会话各自可写，授权初次复制后独立持久保存。
        cb=state/'codebuddy'; cb.mkdir(exist_ok=True,mode=0o700)
        source=Path.home()/'.codebuddy'
        for name in ('.codebuddy.json','.credentials.json','settings.json','settings.local.json','.mcp.json','mcp.json','user-state.json','CODEBUDDY.md'):
            if (source/name).is_file(): shutil.copy2(source/name,cb/name); (cb/name).chmod(0o600)
        for name in ('skills','plugins','hooks'):
            if (source/name).is_dir(): copy_tree(source/name,cb/name)
        services['daemon']['CODEBUDDY_CONFIG_DIR']=str(cb)
        services['daemon']['MULTICA_ENV_WRITABLE_ROOT']=m['daemon_workspaces_root']
        write(config_path(env),services,True)
        print(env, '运行配置已独立持久保存；数据库未修改')

def health_url(env): return 'http://127.0.0.1:' + ('19803' if env=='int' else '19909')

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

def drain(env,m):
    h=http(health_url(env)+'/health')
    if h['profile'] != m['daemon_profile'] or h['pid'] != m['pids']['daemon']: raise RuntimeError('Daemon 所属环境不匹配')
    try: http(health_url(env)+'/drain','POST')
    except urllib.error.HTTPError as e:
        if e.code!=404: raise
        return False
    deadline=time.monotonic()+900
    while time.monotonic()<deadline:
        s=http(health_url(env)+'/drain')
        if s['ready_to_stop']: return True
        print(env,'等待任务完成',s,flush=True);time.sleep(5)
    http(health_url(env)+'/drain','DELETE')
    raise RuntimeError('等待已有任务完成超时，已恢复接单')

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
    deadline=time.monotonic()+60
    while h.get('status')!='running' and time.monotonic()<deadline:
        time.sleep(.5); h=http(health_url(env)+'/health')
    if h.get('status')!='running': raise RuntimeError('Daemon 未就绪')
    result['daemon']=h['pid']
    return result

def deploy(env,release_id):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',release_id): raise RuntimeError('非法版本')
    with lock(env):
        artifact=RUN/'artifacts'/release_id
        manifest=read(artifact/'release.json')
        if checksums(artifact)!=manifest['files']: raise RuntimeError('发布包内容被修改')
        cfg=read(config_path(env)); m=read(meta_path(env))
        check_process_ownership(env,m)
        if env=='prod' and release_id not in [read(meta_path('int')).get('release_id'),read(meta_path('int')).get('previous_release_id'),m.get('release_id'),m.get('previous_release_id')]:
            raise RuntimeError('生产只接受联调当前或上一版本，以及生产已部署过的版本')
        isolation_check(config_only=True)
        release=RUN/'releases'/env/release_id
        if release.exists():
            # 发布目录中的 .next/cache 为环境独立的可变缓存；只核验包内文件。
            current=checksums(release)
            if any(current.get(k)!=v for k,v in manifest['files'].items()): raise RuntimeError('已安装版本文件被修改')
        else: copy_tree(artifact,release)
        prior=json.loads(json.dumps(m)); oldcfg={r:process_env(pid) for r,pid in m['pids'].items()}
        write(RUN/'env'/f'{env}-before-deploy.json',oldcfg,True)
        old_cwds={r:str(Path(f'/proc/{pid}/cwd').resolve()) for r,pid in m['pids'].items()}
        drained=drain(env,m)
        if not drained:
            # 首次升级的旧 daemon 没有 drain API。先检查空闲，再关闭 API 入口，
            # 等待在途认领收敛后复查；忙碌时恢复旧 API，不取消用户任务。
            deadline=time.monotonic()+900
            while http(health_url(env)+'/health')['active_task_count']:
                if time.monotonic()>deadline: raise RuntimeError('旧版 Daemon 仍忙，未停止任何服务')
                print(env,'等待旧版 Daemon 自然空闲',flush=True);time.sleep(5)
            owned_stop(m['pids']['server'],'server',old_cwds['server'])
            time.sleep(6)
            if http(health_url(env)+'/health')['active_task_count']:
                oldserver=oldcfg['server']
                m['pids']['server']=start([Path(old_cwds['server'])/'server/bin/server'],old_cwds['server'],oldserver,m['log_paths']['server'])
                write(meta_path(env),m)
                raise RuntimeError('旧版认领尚未完成，已恢复 API，未停止 Daemon')
        try:
            http(health_url(env)+'/shutdown','POST')
            deadline=time.monotonic()+60
            while exists(m['pids']['daemon']) and time.monotonic()<deadline: time.sleep(.2)
            if exists(m['pids']['daemon']): raise RuntimeError('Daemon 未退出，拒绝强杀')
            owned_stop(m['pids']['web'],'web',old_cwds['web'])
            owned_stop(m['pids']['server'],'server',old_cwds['server'])
            m['pids']=launch(env,release,cfg,m)
        except Exception:
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
            print('发布失败，已恢复切换前版本；配置与数据未重置。',file=sys.stderr)
            raise
        m.update(release_id=release_id,release_dir=str(release),previous_release_id=prior.get('release_id') if prior.get('release_id')!=release_id else prior.get('previous_release_id'),commit=manifest['source_commit'],source_digest=manifest['source_digest'],deployed_at=time.strftime('%FT%TZ',time.gmtime()))
        m.update(build_version=release_id,frontend_mode='standalone',runtime_config_file=str(config_path(env)),binary_versions={'multica':output([release/'server/bin/multica','version'])})
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
    if len(cfg)!=2: raise RuntimeError('请先初始化 int 和 prod 的运行配置')
    checks={}
    for e,c in cfg.items():
        checks[e+'_environment']=all(x.get('MULTICA_ENV_ID')==e for x in c.values())
        checks[e+'_upstream']=c['web']['REMOTE_API_URL'].rstrip('/')=='http://127.0.0.1:'+c['server']['PORT']
        for k in ('REDIS_URL','REALTIME_RELAY_REDIS_URL','CHANNEL_WS_LEASE_REDIS_URL','S3_BUCKET'):
            if c['server'].get(k): raise RuntimeError(k+' 已启用，须先配置并验证独立资源')
        if not config_only:
            m=read(meta_path(e)); check_process_ownership(e,m); checks[e+'_api']=http(m['frontend_url']+'/api/config').get('environment_id')==e
            for role,keys in {'server':['MULTICA_ENV_ID','JWT_SECRET','DATABASE_URL','LOCAL_UPLOAD_DIR','PORT','PPROF_PORT'],'web':['MULTICA_ENV_ID','REMOTE_API_URL','PORT'],'daemon':['MULTICA_ENV_ID','CODEBUDDY_CONFIG_DIR','MULTICA_ENV_WRITABLE_ROOT','TMPDIR']}.items():
                actual=process_env(m['pids'][role]);checks[e+'_'+role+'_live_config']=all(actual.get(k)==c[role].get(k) for k in keys)
            h=http(health_url(e)+'/health'); p=read(m['daemon_profile_path'])
            checks[e+'_daemon']=h['profile']==m['daemon_profile'] and h['server_url'].rstrip('/')==m['backend_url'] and p['workspaces_root']==m['daemon_workspaces_root']
            checks[e+'_release']=all(str(Path(f'/proc/{pid}/cwd').resolve()).startswith(m['release_dir']+'/') or str(Path(f'/proc/{pid}/cwd').resolve())==m['release_dir'] for pid in m['pids'].values())
    for k in ('JWT_SECRET','DATABASE_URL','PORT','PPROF_PORT','LOCAL_UPLOAD_DIR'):
        checks[k+'_distinct']=cfg['int']['server'].get(k)!=cfg['prod']['server'].get(k)
    for role,k in [('daemon','CODEBUDDY_CONFIG_DIR'),('daemon','TMPDIR'),('daemon','MULTICA_ENV_WRITABLE_ROOT'),('server','LOCAL_UPLOAD_DIR')]:
        a=Path(cfg['int'][role][k]).resolve();b=Path(cfg['prod'][role][k]).resolve()
        checks[k+'_separate']=a!=b and a not in b.parents and b not in a.parents
    ports=[c[r][k] for c in cfg.values() for r,k in [('server','PORT'),('server','PPROF_PORT'),('web','PORT')]]
    checks['ports_distinct']=len(set(ports))==len(ports)
    print(json.dumps(checks,ensure_ascii=False))
    if not all(checks.values()): raise RuntimeError('环境隔离检查失败')

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['build','initialize','deploy','rollback','verify'])
    parser.add_argument('environment',nargs='?',choices=ENVS)
    parser.add_argument('release',nargs='?')
    a=parser.parse_args()
    if a.command in ('initialize','deploy','rollback') and not a.environment: parser.error('必须显式指定 int 或 prod')
    if a.command=='build': build()
    elif a.command=='initialize': initialize(a.environment)
    elif a.command=='verify': isolation_check()
    elif a.command=='deploy':
        if not a.release: parser.error('deploy 必须指定发布包版本')
        deploy(a.environment,a.release)
    elif a.command=='rollback':
        version=read(meta_path(a.environment)).get('previous_release_id')
        if not version or not (RUN/'artifacts'/version).exists(): raise RuntimeError('没有可回滚的隔离发布包')
        deploy(a.environment,version)
