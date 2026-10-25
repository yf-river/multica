#!/usr/bin/env python3
"""固定 int 验证后更新 prod；Git 保存历史，运行目录仅在切换期间保留旧副本。"""
import pwd
import threading
import argparse, contextlib, fcntl, hashlib, json, os, re, shutil, signal, subprocess, sys, time, urllib.request, urllib.error, urllib.parse
from pathlib import Path

ROOT = Path(os.environ.get('MULTICA_RELEASE_ROOT', Path(__file__).resolve().parent.parent)).resolve()
RUN = ROOT / '.run'
ENVS = ('int', 'prod')
VERIFICATION_ENVS = ('verify-1', 'verify-2', 'verify-3')

class RecoveryRequired(RuntimeError):
    """自动恢复未完成，禁止后续更新覆盖恢复资料。"""


def require_environment(env):
    if env not in (*ENVS, *VERIFICATION_ENVS): raise RuntimeError(f'未知环境: {env}')

def run(args, **kw):
    return subprocess.run([str(a) for a in args], check=True, **kw)

def output(args, **kw):
    return subprocess.check_output([str(a) for a in args], **kw).decode().strip()

def read(path):
    return json.loads(Path(path).read_text())

def write(path, data, private=False):
    import tempfile
    path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
    fd,tmp=tempfile.mkstemp(prefix=path.name+'.',suffix='.tmp',dir=path.parent)
    try:
        os.fchmod(fd,0o600 if private else 0o644)
        with os.fdopen(fd,'w') as stream:
            json.dump(data,stream,indent=2);stream.write('\n');stream.flush();os.fsync(stream.fileno())
        os.replace(tmp,path)
    finally:
        if os.path.exists(tmp):os.unlink(tmp)

def meta_path(env): return RUN / 'deployments' / f'goal-test-{env}.json'
def config_path(env): return RUN / 'env' / f'{env}-services.json'
def current_seed():
    """1. 当前已验证种子指针：只由种子验收写入，缺失表示尚未启用种子生命周期。"""
    path = RUN / 'verification-seed.json'
    return read(path) if path.exists() else None
def process_env(pid):
    return dict(x.split('=', 1) for x in Path(f'/proc/{pid}/environ').read_text().split('\0') if '=' in x)
def process_identity(pid):
    try:return Path(f'/proc/{pid}/stat').read_text().split(') ')[1].split()[19]
    except FileNotFoundError:return None

def exists(pid):
    try: return Path(f'/proc/{pid}/stat').read_text().split(') ')[1][0] != 'Z'
    except FileNotFoundError: return False

def http(url, method='GET'):
    req = urllib.request.Request(url, method=method)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=5) as r:
        data = r.read()
        try: return json.loads(data)
        except ValueError: return data.decode()

def ready(url, timeout=90, pid=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if pid is not None and not exists(pid): raise RuntimeError("服务启动进程已退出: "+url)
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

def validation_identity(source, env='int'):
    """业务输入和验收程序独立于版本戳；缺少可靠映射时选择完整验收。"""
    _, names = source_digest(Path(source))
    runtime = hashlib.sha256(); schema = hashlib.sha256(); suite = hashlib.sha256()
    for name in names:
        data = (Path(source)/name).read_bytes()
        if name.startswith(('docs/', 'apps/docs/', '.github/', '.agents/')) or ('/' not in name and name.endswith('.md')):
            continue
        runtime.update(name.encode()+b'\0'+data)
        if name.startswith('server/migrations/'):schema.update(name.encode()+b'\0'+data)
        if name.startswith('scripts/') or 'test' in Path(name).name:suite.update(name.encode()+b'\0'+data)
    identity = dict(runtime_inputs=runtime.hexdigest(), schema=schema.hexdigest(), suite=suite.hexdigest(),
                    configuration=hashlib.sha256(config_path(env).read_bytes()).hexdigest())
    # 2. 槽位候选的数量身份还要绑定测试数据：同一程序在不同种子上的结果不能算同一份证据。
    if env != 'int':
        seed = current_seed() or {}
        identity.update(environment=env, seed=seed.get('seed_id', ''), seed_sha256=seed.get('dump_sha256', ''))
    return identity

def validation_plan(source, previous):
    identity=validation_identity(source)
    if previous and identity==previous.get('validation_identity'):
        return dict(mode='reuse',reason='业务输入、验收程序、结构与配置相同',identity=identity,checks=[])
    changed=[]
    if previous:
        changed=output(['git','diff','--name-only',previous['commit'],'HEAD'],cwd=source).splitlines()
    recipe=Path(source)/'scripts/delivery-acceptance.json'
    groups=read(recipe).get('groups',[]) if recipe.exists() else []
    checks=[]; uncovered=[]
    for name in changed:
        matched=[g for g in groups if any(name.startswith(p) for p in g.get('paths',[]))]
        if not matched:uncovered.append(name)
        for g in matched:
            if g['name'] not in checks:checks.append(g['name'])
    full=not previous or not changed or bool(uncovered) or any(n.startswith(('server/migrations/','pnpm-lock.yaml')) for n in changed)
    return dict(mode='full' if full else 'affected',reason='缺少完整覆盖映射或存在结构/依赖变化' if full else '按声明的覆盖范围补测',
                identity=identity,changed_paths=changed,checks=checks,uncovered=uncovered)

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

def checked_source(source, *, verification=False):
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
    if not published.exists(): raise RuntimeError('缺少已发布源码记录，停止构建并检查运行配置')
    baseline = read(published)['commit']
    # 独立验收固定候选；最终 int 与生产发布才要求跟随已发布基线。
    if not verification and subprocess.run(['git', 'merge-base', '--is-ancestor', baseline, sha], cwd=source).returncode:
        raise RuntimeError('候选未包含最新已发布提交，请先整合代码')
    return source, sha

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


def build_version(source, sha):
    """本次构建注入的版本串：daemon 上报的 cli_version、web 展示的 app 版本都用它。

    必须走 scripts/build-version.sh（与 `make build` 同一个来源），不能直接塞提交
    哈希。daemon 用这个串告诉前端/服务端"我是哪个版本、支不支持交接说明/快速创建"，
    而两边的解析器只认 `vX.Y.Z` 和 git-describe 的 `vX.Y.Z-N-g<sha>`；裸哈希按
    fail-closed 处理，于是新构建的运行时被当成旧版本（交接说明输入框置灰、快速创建
    422）。这里再加一道格式校验，让这类版本串在构建阶段就失败，而不是等用户撞上。
    """
    script = source/'scripts/build-version.sh'
    # 更早的任务分支没有这个脚本：保持改动前的裸哈希行为，不用新校验挡住它们构建。
    if not script.exists(): return sha[:12]
    version = output(['bash', str(script)], cwd=source)
    if not re.match(r'^v?\d+\.\d+\.\d+(-\d+-g[0-9a-fA-F]+)?(-dirty)?$', version):
        raise RuntimeError('构建版本串不可被版本门槛解析: '+version+'（scripts/build-version.sh 应输出 vX.Y.Z 或 vX.Y.Z-N-g<sha>）')
    return version

def reusable_build(previous, current_files, version):
    """当前 int 运行目录里的产物能否整体复用。

    源码输入一致还不够：产物里的 `-X main.version` 与 web 的 NEXT_PUBLIC_APP_VERSION
    都不在源码里，只按 inputs 判断会把上一轮用旧规则构建的二进制原样搬进新候选，
    联调核对版本串时才发现修复没有进产物。所以版本串也要一致。
    """
    if previous.get('version') != version: return False
    return all(current_files.get(k) == v for k, v in previous.get('files', {}).items())

def executor_preflight(source, *, env=None, integration=False):
    """只读核对当前执行器；不读取或输出凭据，不触发安装、迁移或环境切换。"""
    source=Path(source).resolve()
    effective=os.environ.copy() if env is None else env.copy()
    if not effective.get('HOME'): effective['HOME']=pwd.getpwuid(os.getuid()).pw_dir
    home=Path(effective['HOME'])
    if not home.is_absolute() or not home.is_dir() or not os.access(home,os.R_OK|os.W_OK|os.X_OK):
        raise RuntimeError('发布预检失败：当前执行用户无法使用 HOME')
    def probe(name,args,cwd=source):
        executable=shutil.which(name,path=effective.get('PATH'))
        if not executable: raise RuntimeError('发布预检失败：缺少 '+name)
        try:
            value=subprocess.run([executable,*args],cwd=cwd,env=effective,check=True,capture_output=True,text=True,timeout=30).stdout.strip()
        except (subprocess.SubprocessError,OSError) as error:
            # 依赖的 stderr 可能含仓库 URL 或代理凭据，只返回检查名。
            raise RuntimeError('发布预检失败：'+name+' '+args[0]+' 执行失败') from error
        return executable,value
    versions={}
    for name,args in [('git',['--version']),('go',['env','GOVERSION']),('node',['--version']),('pnpm',['--version'])]:
        executable,value=probe(name,args,source/'server' if name=='go' else source)
        versions[name]=dict(path=executable,version=value)
    if integration:
        probe('git',['var','GIT_AUTHOR_IDENT']);probe('git',['var','GIT_COMMITTER_IDENT'])
    _,cache_json=probe('go',['env','-json','GOCACHE','GOPATH'],source/'server')
    for name,directory in json.loads(cache_json).items():
        path=Path(directory)
        if not path.is_absolute(): raise RuntimeError('发布预检失败：'+name+' 不是绝对目录')
        while not path.exists() and path!=path.parent: path=path.parent
        if not path.is_dir() or not os.access(path,os.W_OK|os.X_OK):
            raise RuntimeError('发布预检失败：当前执行用户无法写入 '+name)
    package=read(source/'package.json')
    node_min=re.fullmatch(r'>=(\d+)',package.get('engines',{}).get('node',''))
    if node_min and int(versions['node']['version'].lstrip('v').split('.')[0])<int(node_min.group(1)):
        raise RuntimeError('发布预检失败：Node 版本低于 engines.node')
    go_min=re.search(r'^go (\d+)\.(\d+)\.(\d+)$',(source/'server/go.mod').read_text(),re.M)
    go_actual=re.fullmatch(r'go(\d+)\.(\d+)\.(\d+)',versions['go']['version'])
    if go_min and (not go_actual or tuple(map(int,go_actual.groups()))<tuple(map(int,go_min.groups()))):
        raise RuntimeError('发布预检失败：Go 版本低于 go.mod')
    expected=package.get('packageManager','')
    if expected.startswith('pnpm@') and versions['pnpm']['version']!=expected.split('@',1)[1].split('+',1)[0]:
        raise RuntimeError('发布预检失败：pnpm 版本与 packageManager 不一致')
    required=('pnpm-lock.yaml','pnpm-workspace.yaml','server/go.mod','server/go.sum',
              'scripts/release-environments.py','scripts/issue-delivery-worker.py','scripts/release-maintenance.py',
              'scripts/verification-environments.py','scripts/verification-seed.py','scripts/verification-seed-copy.mjs',
              'scripts/test-agent-policy.py','scripts/test-agent-models.py','scripts/test-agent-models.mjs',
              'scripts/verification-scrub.json','scripts/lib/verification-scrub.mjs','scripts/check-release-database.mjs',
              'scripts/verify-release-browser.mjs','scripts/lib/goal-test-audit-env.mjs',
              'scripts/lib/goal-test-browser-audit.mjs','scripts/lib/acceptance-artifacts.mjs')
    missing=[name for name in required if not (source/name).is_file()]
    if missing: raise RuntimeError('发布预检失败：构建/发布依赖文件缺失：'+', '.join(missing))
    return dict(status='passed',executor_pid=os.getpid(),uid=os.getuid(),python=sys.version.split()[0],
                controller_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                worker_sha256=hashlib.sha256(Path(__file__).with_name('issue-delivery-worker.py').read_bytes()).hexdigest(),
                controller_matches_source=Path(__file__).read_bytes()==(source/'scripts/release-environments.py').read_bytes(),
                worker_matches_source=Path(__file__).with_name('issue-delivery-worker.py').read_bytes()==(source/'scripts/issue-delivery-worker.py').read_bytes(),
                source_commit=output(['git','rev-parse','HEAD'],cwd=source),tools=versions,
                home_usable=True,git_identity_checked=integration,dependency_files_checked=len(required))

@contextlib.contextmanager
def build_stage(name):
    started=time.monotonic()
    print(json.dumps(dict(build_stage=name,status='started')),file=sys.stderr,flush=True)
    try:
        yield
    except BaseException:
        print(json.dumps(dict(build_stage=name,status='failed',elapsed_ms=round((time.monotonic()-started)*1000))),file=sys.stderr,flush=True)
        raise
    else:
        print(json.dumps(dict(build_stage=name,status='passed',elapsed_ms=round((time.monotonic()-started)*1000))),file=sys.stderr,flush=True)

def operation_files(directory):
    """候选自己声明完整操作包，构建器与安装器使用同一清单。"""
    directory=Path(directory).resolve()
    manifest=directory/'delivery-operations.json'
    record=read(manifest)
    names=record.get('files')
    if record.get('version')!=1 or not isinstance(names,list) or not names or any(not isinstance(name,str) for name in names):
        raise RuntimeError('发布操作包清单无效')
    if len(set(names))!=len(names) or not {'issue-delivery-worker.py','release-environments.py'}.issubset(names):
        raise RuntimeError('发布操作包清单重复或缺少发布入口')
    for name in names:
        file=directory/name
        if Path(name).is_absolute() or '..' in Path(name).parts or not file.resolve().is_relative_to(directory) or not file.is_file():
            raise RuntimeError('发布操作包缺少文件或路径越界：'+name)
    return (manifest.name,*names)

def build(source, target, *, verification=False):
    source, sha = checked_source(source, verification=verification)
    with build_stage('preflight'):
        operation_files(source/'scripts')
        print(json.dumps(executor_preflight(source),ensure_ascii=False),file=sys.stderr)
    version = build_version(source, sha)
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
    # 运行目录只允许增加缓存，包内源码与程序必须仍然匹配，注入的版本串也要一致。
    current_files = checksums(current) if previous.get('inputs') else {}
    reusable = reusable_build(previous, current_files, version)
    target.mkdir(parents=True)
    env = os.environ.copy()
    if not env.get('HOME'): env['HOME'] = pwd.getpwuid(os.getuid()).pw_dir
    for key in list(env):
        if key.startswith(('NEXT_PUBLIC_', 'MULTICA_', 'GOAL_TEST_')) or key in ('REMOTE_API_URL','FRONTEND_ORIGIN','DATABASE_URL','JWT_SECRET'): env.pop(key)
    env.update(STANDALONE='true', NEXT_PUBLIC_APP_VERSION=version, NEXT_TELEMETRY_DISABLED='1')
    if reusable and previous.get('inputs', {}).get('web') == inputs['web']:
        copy_tree(current, target, excludes=('/server/', '/release.json', '**/.next/cache/***'))
    else:
        with build_stage('dependencies'):
            run(['pnpm', 'install', '--frozen-lockfile'], cwd=source, env=env)
        with build_stage('web'):
            run(['pnpm', '--filter', '@multica/web', 'build'], cwd=source, env=env)
        copy_tree(source/'apps/web/.next/standalone', target)
        copy_tree(source/'apps/web/.next/static', target/'apps/web/.next/static')
        copy_tree(source/'apps/web/public', target/'apps/web/public')
    if reusable and previous.get('inputs', {}).get('go') == inputs['go']:
        copy_tree(current/'server/bin', target/'server/bin')
    else:
        (target/'server/bin').mkdir(parents=True, exist_ok=True)
        for binary in ('server', 'multica', 'migrate'):
            with build_stage('go-'+binary):
                run(['go', 'build', '-ldflags', f'-X main.version={version} -X main.commit={sha}', '-o', target/'server/bin'/binary, './cmd/'+binary], cwd=source/'server', env=env)
    copy_tree(source/'server/migrations', target/'server/migrations')
    operations=target/'operations';operations.mkdir(exist_ok=True)
    for name in operation_files(source/'scripts'):
        destination=operations/name;destination.parent.mkdir(parents=True,exist_ok=True)
        destination.write_bytes((source/'scripts'/name).read_bytes())
    if checked_source(source, verification=verification)[1] != sha or source_digest(source)[0] != digest:
        raise RuntimeError('构建期间源码变化，请重新联调')
    queries = []
    for file in sorted((source/'server/pkg/db/generated').glob('*.sql.go')):
        for name, sql in re.findall(r'^const (\w+) = `(.*?)`\n', file.read_text(), re.M | re.S):
            queries.append(dict(name=name, sql=sql.replace('` + "`" + `', '`')))
    query_files = [*(source/'server/internal/handler').glob('issue_delivery*.go'), *(source/'server/internal/handler').glob('verification_slot.go'), *(source/'server/internal/verification').glob('slots.go')]
    for file in query_files:
        for sql in re.findall(r'`([^`]+)`',file.read_text()):
            if re.match(r'^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\b',sql) and not sql.rstrip().endswith('FROM'):
                queries.append(dict(name='delivery_'+str(len(queries)),sql=sql))
    write(target/'server/queries.json', queries)
    manifest = dict(id=sha+'-'+str(time.time_ns()), source_commit=sha, source_directory=str(source), source_digest=digest,
                    version=version, inputs=inputs, files=checksums(target), built_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()))
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
        if actual_cwd != expected_cwd:
            raise RuntimeError(f'{env} {role} PID 工作目录不属于记录的版本')
        if role != 'web':
            binary = 'multica' if role == 'daemon' else 'server'
            if Path(f'/proc/{pid}/exe').resolve() != role_release / 'server/bin' / binary:
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
        if identity != env:
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

def service_credentials(env):
    identity = env.get('MULTICA_ENV_ID', '')
    if identity not in VERIFICATION_ENVS:
        return {}
    name = env.get('MULTICA_SERVICE_USER', '')
    if name not in (f'multica-{identity}-app', f'multica-{identity}-agent'):
        raise RuntimeError('验收服务缺少专用执行身份')
    account = pwd.getpwnam(name)
    if account.pw_uid == 0 or account.pw_gid == 0:
        raise RuntimeError('验收服务不能使用 root 身份')
    return dict(user=account.pw_uid, group=account.pw_gid, extra_groups=[])


def validate_verification_config(env, cfg):
    if env not in VERIFICATION_ENVS:
        return
    for role in ('server','web','daemon'):
        expected = f'multica-{env}-'+('agent' if role == 'daemon' else 'app')
        if cfg[role].get('MULTICA_ENV_ID') != env or cfg[role].get('MULTICA_SERVICE_USER') != expected:
            raise RuntimeError('验收环境执行身份不匹配')
        service_credentials(cfg[role])
    database = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
    if database.scheme not in ('postgres', 'postgresql') or not database.hostname or not database.username or not database.path.strip('/'):
        raise RuntimeError('验收数据库必须使用明确实例、账号和库名')
    if database.username != env.replace('-', '_') or database.path != '/multica_'+env.replace('-', '_'):
        raise RuntimeError('验收数据库不属于登记槽位')
    for other in (*ENVS, *VERIFICATION_ENVS):
        if other == env or not config_path(other).exists():
            continue
        other_cfg = read(config_path(other))
        if cfg['server']['JWT_SECRET'] == other_cfg['server']['JWT_SECRET']:
            raise RuntimeError('验收环境不能共享认证密钥')
        own_ports = {cfg[r][k] for r,k in [('server','PORT'),('server','PPROF_PORT'),('web','PORT')]}
        other_ports = {other_cfg[r][k] for r,k in [('server','PORT'),('server','PPROF_PORT'),('web','PORT')]}
        if own_ports & other_ports or len(own_ports) != 3:
            raise RuntimeError('验收环境端口冲突')
        for role, key in [('server','LOCAL_UPLOAD_DIR'),('daemon','MULTICA_ENV_WRITABLE_ROOT'),('daemon','HOME')]:
            own = Path(cfg[role][key]).resolve()
            foreign = Path(other_cfg[role][key]).resolve()
            if own == foreign or own in foreign.parents or foreign in own.parents:
                raise RuntimeError('验收环境目录重叠')


def start(cmd,cwd,env,log):
    # 服务已停止后才整理日志，避免移动仍被写入的文件。
    logfile=Path(log)
    if logfile.exists() and logfile.stat().st_size > 64*1024*1024:
        with logfile.open('rb') as stream:
            stream.seek(-16*1024*1024,2); tail=stream.read()
        logfile.write_bytes(tail)
    with open(log,'ab') as f:
        p=subprocess.Popen([str(x) for x in cmd],cwd=cwd,env=env,stdin=subprocess.DEVNULL,stdout=f,stderr=f,start_new_session=True,**service_credentials(env))
    return p.pid

def drain(env,m,timeout=900):
    h=http(health_url(env)+'/health')
    if h['profile'] != m['daemon_profile'] or h['pid'] != m['pids']['daemon']: raise RuntimeError('Daemon 所属环境不匹配')
    try:
        http(health_url(env)+'/drain','POST')
        started=time.monotonic();deadline=started+timeout;pause_requested=False
        while time.monotonic()<deadline:
            s=http(health_url(env)+'/drain')
            if s['ready_to_stop']:
                if pause_requested:
                    window=release_window_api(env)
                    if recover_pause_acknowledgements(env,window):window=release_window_api(env)
                    if any(t['state']=='requested' and t.get('task_status')=='deferred' and t.get('failure_reason')=='release_pause' for t in window.get('tasks',[])):raise RecoveryRequired('任务已退出但暂停检查点尚未确认，禁止切换')
                return True
            if s.get('release_pause_supported') and time.monotonic()-started>=120 and not pause_requested:
                if s['claims_in_flight']!=0:
                    time.sleep(.2);continue
                window=read(release_window_path(env))
                release_window_api(env,dict(id=window['id'],action='pause'))
                pause_requested=True;deadline=min(deadline,time.monotonic()+60)
            print(env,'等待任务完成',s,flush=True);time.sleep(min(2,max(0,deadline-time.monotonic())))
        raise RuntimeError('等待已有任务完成超时')
    except BaseException:
        http(health_url(env)+'/drain','DELETE')
        print('已恢复接单',flush=True)
        raise

def release_window_path(env):return RUN/'release-windows'/(env+'.json')

def release_window_api(env,body=None):
    meta=read(meta_path(env));profile=read(meta['daemon_profile_path'])
    req=urllib.request.Request(meta['backend_url'].rstrip('/')+'/api/delivery-queue/window',
        data=None if body is None else json.dumps(body).encode(),
        headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json'})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=20) as response:return json.load(response)

def recover_pause_acknowledgements(env,window):
    pending={t['task_id'] for t in window.get('tasks',[]) if t['state']=='requested' and t.get('task_status')=='deferred' and t.get('failure_reason')=='release_pause'}
    if not pending:return False
    state=http(health_url(env)+'/drain')
    if state['active_tasks'] or state['claims_in_flight']:return False
    meta=read(meta_path(env));root=Path(meta['daemon_workspaces_root']).resolve();recovered=False
    for file in root.glob('*/*/.release-pause.json'):
        saved=read(file);task=saved.get('task',{}).get('id')
        if task not in pending:continue
        directory=file.parent.resolve()
        if root not in directory.parents or str(directory)!=saved.get('root'):raise RecoveryRequired('暂停检查点目录归属不符')
        if not Path(saved['work_dir']).is_dir():raise RecoveryRequired('暂停任务工作目录缺失')
        # 不能只根据 daemon 的计数认定旧 Agent 或工具子进程已经停止。
        for process in Path('/proc').glob('[0-9]*'):
            try:
                if exists(int(process.name)) and process_env(int(process.name)).get('MULTICA_TASK_ID')==task:
                    raise RecoveryRequired('原任务进程仍存活，不能补记停止确认')
            except (FileNotFoundError,ProcessLookupError):continue
        profile=read(meta['daemon_profile_path'])
        request=urllib.request.Request(meta['backend_url'].rstrip('/')+'/api/daemon/tasks/'+task+'/release-pause-ack',
            data=json.dumps(dict(session_id=saved.get('session_id',''),work_dir=saved['work_dir'],root_dir=str(directory))).encode(),
            headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json'})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=20) as response:json.load(response)
        recovered=True
    return recovered

def resume_release_window(env):
    path=release_window_path(env)
    if not path.exists():return
    window=read(path)
    if window.get('state')=='finished':return
    live=read(meta_path(env));check_process_ownership(env,live)
    ready(live['backend_url']+'/health',timeout=10)
    ready(live['frontend_url']+'/login',timeout=10)
    recover_pause_acknowledgements(env,release_window_api(env))
    # 任务停止确认与恢复入队在同一数据库窗口中核对，失败时保留清单供下次恢复。
    release_window_api(env,dict(id=window['id'],action='resume'))
    window.update(state='finished',resumed_at=time.time());write(path,window,True)
    http(health_url(env)+'/drain','DELETE')

@contextlib.contextmanager
def release_task_window(env,directory):
    path=release_window_path(env);meta=read(meta_path(env))
    h=http(health_url(env)+'/health')
    if h.get('pid')!=meta['pids']['daemon'] or h.get('profile')!=meta['daemon_profile']:raise RecoveryRequired('暂停前运行环境身份不符')
    if not http(health_url(env)+'/drain').get('release_pause_supported'):
        # 首次升级由旧协议正常排空；不能假设旧 daemon 会保存暂停检查点。
        yield;return
    if path.exists() and read(path).get('state')!='finished':raise RecoveryRequired('上次发布的暂停清单尚未恢复')
    import uuid
    window=dict(id=str(uuid.uuid4()),daemon_id=h['daemon_id'],release_id=read(Path(directory)/'release.json')['id'],state='draining',publisher_pid=os.getpid(),publisher_started=process_identity(os.getpid()))
    write(path,window,True)
    try:
        try:release_window_api(env,dict(window,action='begin'))
        except Exception:
            # begin 没成立时清掉本地意向：否则 finally 会去 resume 一个服务端不存在的窗口，
            # 把"已有未完成窗口"这类真实原因掩盖成恢复失败。
            path.unlink(missing_ok=True);raise
        yield
    finally:
        if path.exists():
            try:resume_release_window(env)
            except Exception as error:
                window.update(error=str(error));write(path,window,True)
                raise RecoveryRequired('发布暂停任务等待自动恢复：'+str(error)) from error

def recover_release_windows():
    for env in (*ENVS,*VERIFICATION_ENVS):
        path=release_window_path(env)
        if not path.exists():continue
        window=read(path)
        if window.get('state')=='finished' or (exists(window.get('publisher_pid',0)) and process_identity(window['publisher_pid'])==window.get('publisher_started')):continue
        # 发布器独占锁以及真实服务检查都通过，才允许恢复任务。
        with lock('publisher'):
            resume_release_window(env)

def migrate(release, cfg, action='up', previous=None):
    args=[Path(release)/'server/bin/migrate', action]
    if action=='up':
        if previous is None: raise RuntimeError('迁移需要切换前版本作为基线')
        old={p.name for p in (Path(previous)/'server/migrations').glob('*.up.sql')}
        versions=sorted(p.name.removesuffix('.up.sql') for p in (Path(release)/'server/migrations').glob('*.up.sql') if p.name not in old)
        if not versions: return
        args.extend(['--versions', ','.join(versions)])
    run(args, cwd=Path(release)/'server', env=cfg['server'])

def start_maintenance(env, metadata):
    marker=RUN/(env+'-maintenance.json')
    if marker.exists():
        state=read(marker)
        if exists(state['pid']):
            args=Path('/proc/'+str(state['pid'])+'/cmdline').read_bytes()
            if b'release-maintenance.py' not in args: raise RuntimeError('维护进程身份不匹配')
            return state['pid']
    helper=Path(__file__).with_name('release-maintenance.py')
    pid=start([sys.executable,helper,'--port',str(metadata['frontend_port'])],ROOT,os.environ,RUN/(env+'-maintenance.log'))
    write(marker,{'pid':pid},True)
    deadline=time.monotonic()+10
    while time.monotonic()<deadline:
        if not exists(pid):raise RuntimeError('维护提示启动失败')
        try:
            http(metadata['frontend_url']+'/api/config')
        except urllib.error.HTTPError as e:
            if e.code==503:return pid
        except (OSError,urllib.error.URLError):pass
        time.sleep(.1)
    raise RuntimeError('维护提示未就绪')

def stop_maintenance(env):
    marker=RUN/(env+'-maintenance.json')
    if not marker.exists():return
    pid=read(marker)['pid']
    if exists(pid):
        args=Path('/proc/'+str(pid)+'/cmdline').read_bytes()
        if b'release-maintenance.py' not in args:raise RuntimeError('维护进程身份不匹配')
        owned_stop(pid,'maintenance',str(ROOT))
    marker.unlink(missing_ok=True)

def reviewed_snapshot_column_change(migration):
    # 精确审查的增量只补齐快照已存在的六个列，不更新或删除业务数据。
    return (migration.name == '564_snapshot_column_compatibility.up.sql' and
            hashlib.sha256(migration.read_bytes()).hexdigest() == '9d630812839f1a60141f22e3bf54403d81f5074cd0770593cae233a4344aac52')

def reviewed_supervision_index_change(release, migration, statements):
    # 本次已评审的索引替换：配置行锁仍保护旧程序的串行派发，恢复程序无需恢复旧索引。
    replacement=Path(release)/'server/migrations/550_supervision_active_issue.up.sql'
    return (migration.name=='551_supervision_parallel.up.sql'
        and statements==['DROP INDEX CONCURRENTLY IF EXISTS supervision_active_workspace']
        and replacement.exists()
        and replacement.read_text().strip()=="CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_active_issue ON supervision_batch(workspace_id,issue_id) WHERE status IN ('preparing','running');")


def reviewed_supervision_settings_change(migration):
    # 精确兼容迁移：追加提示词列，按现有配置标记托管身份；旧程序仍按原ID读取。
    reviewed = {
        '617_supervision_instructions.up.sql': '695c87f6f451cd1aeb233262e4845a8301798f1fbe538b9d1daf2d8b70e46d3e',
        '618_supervision_managed_agent.up.sql': 'ba9490794ae9c5717ed1c89e08a8de46c195a5203989e929707afbb9fda6469c',
    }
    return migration.name in reviewed and hashlib.sha256(migration.read_bytes()).hexdigest() == reviewed[migration.name]


def reviewed_trace_preparation_change(migration):
    # Modification 1. Permit only the exact backward-compatible trace schema reviewed in quality-agents.md.
    reviewed = {
        '643_supervision_material_trace_usage.up.sql': '5d61987590982a5b32cc9122d2baae983db20f416c19b894a9b120a5c48bba6b',
        '645_supervision_round_preparation_budget.up.sql': 'b4f5e9f057fa6f4f0e2cc51cbe9c5051bcf26aecb43c6fe3a8bcc8fe2e67c047',
        '649_supervision_trace_scope.up.sql': '2b6d874a52e55da706199f7fca2d6dcca98ef42cf40cf8fc2f0ecc32fe8f905e',
    }
    return migration.name in reviewed and hashlib.sha256(migration.read_bytes()).hexdigest() == reviewed[migration.name]


def additive_statement(statement):
    # 通用路径只接受新建表和并发索引；普通 CREATE INDEX 一律拒绝，新表也不例外。
    return bool(re.match(r'^CREATE\s+(TABLE\b|(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\b)',statement,re.I))


def prepare_database(release, cfg, previous):
    old={p.name for p in (Path(previous)/'server/migrations').glob('*.up.sql')}
    for migration in sorted((Path(release)/'server/migrations').glob('*.up.sql')):
        if migration.name in old:continue
        sql=re.sub(r'/\*.*?\*/','',migration.read_text(),flags=re.S)
        sql=re.sub(r'--[^\n]*','',sql)
        statements=[part.strip() for part in sql.split(';') if part.strip()]
        for statement in statements:
            if additive_statement(statement): continue
            if reviewed_snapshot_column_change(migration) or reviewed_supervision_index_change(release,migration,statements) or reviewed_supervision_settings_change(migration) or reviewed_trace_preparation_change(migration):
                continue
            raise RuntimeError('迁移 '+migration.name+' 需要单独的数据库兼容方案；旧站继续运行')
    # 通用路径只允许新增；上述精确索引替换另有恢复证明，旧程序查询仍须准备成功。
    migrate(release,cfg,previous=previous)
    check_database(previous,cfg)
    check_database(release,cfg)

def readiness_snapshot(url):
    try:
        return dict(http_status=200, body=http(url))
    except urllib.error.HTTPError as error:
        if error.code != 503: raise
        body=json.loads(error.read())
        if body.get('status')!='not_ready' or not isinstance(body.get('checks'),dict):
            raise RuntimeError('无法确认切换前的真实就绪状态')
        return dict(http_status=503, body=body)

def check_readiness(metadata,timeout=30,prior_readiness=None):
    url=metadata['backend_url']+'/healthz'
    previous=(prior_readiness or {}).get('body',{})
    if (prior_readiness and prior_readiness.get('http_status')==503
            and previous.get('status')=='not_ready'
            and previous.get('checks')=={'db':'ok','migrations':'out_of_date'}):
        current=readiness_snapshot(url)
        if current['http_status']!=200:
            if current!=prior_readiness:
                raise RuntimeError('恢复后的就绪状态不同于切换前，拒绝接受新增故障')
            metadata['readiness']={**current,'restored_not_ready':True}
            print('恢复版本仍有已记录的迁移就绪缺口',file=sys.stderr)
            return
        metadata['readiness']=current
        return
    metadata['readiness']=dict(http_status=200,body=ready(url,timeout=timeout))

def launch(env, release, cfg, m,prior_readiness=None):
    result={}
    m['pids']=result
    server_env=dict(cfg['server'])
    if env=='prod':
        server_env['MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID']=read(m['daemon_profile_path'])['workspace_id']
    result['server']=start([release/'server/bin/server'],release,server_env,m['log_paths']['server'])
    ready(m['backend_url']+'/health',pid=result['server'])
    check_readiness(m,prior_readiness=prior_readiness)
    stop_maintenance(env)
    webcmd=['node',release/'apps/web/server.js'] if (release/'apps/web/server.js').exists() else ['node',release/'apps/web/node_modules/next/dist/bin/next','start','-p',str(m['frontend_port']),'-H','0.0.0.0']
    result['web']=start(webcmd,release/'apps/web',{**cfg['web'], 'MULTICA_RELEASE_MANIFEST': str(release/'release.json')},m['log_paths']['web'])
    ready(m['frontend_url']+'/login',pid=result['web'])
    webcfg=http(m['frontend_url']+'/api/config')
    if webcfg.get('environment_id','')!=cfg['server'].get('MULTICA_ENV_ID',''): raise RuntimeError('Web/API 环境身份不一致')
    # 以 CLI 管理的 profile 启动；PID 来自健康端点，不猜测子进程。
    run([release/'server/bin/multica','--profile',m['daemon_profile'],'daemon','start','--no-auto-update','--no-auto-reload'],cwd=release,env=cfg['daemon'],stdout=subprocess.DEVNULL,**service_credentials(cfg['daemon']))
    h=ready(health_url(env)+'/health')
    if h['profile']!=m['daemon_profile']: raise RuntimeError('Daemon profile 不匹配')
    result['daemon']=h['pid']
    deadline=time.monotonic()+60
    while h.get('status')!='running' and time.monotonic()<deadline:
        time.sleep(.5); h=http(health_url(env)+'/health')
    if h.get('status')!='running': raise RuntimeError('Daemon 未就绪')
    result['daemon']=h['pid']
    return result

def check_database(release, cfg):
    run(['node', Path(__file__).with_name('check-release-database.mjs'), Path(release)/'server/queries.json'],
        env={**os.environ, **cfg['server']})


def check_live_environment(env, metadata, cfg):
    for role, keys in {'server': ('DATABASE_URL', 'PORT'), 'web': ('REMOTE_API_URL', 'PORT')}.items():
        actual = process_env(metadata['pids'][role])
        if any(actual.get(key) != cfg[role].get(key) for key in keys):
            raise RuntimeError(f'{env} {role} 运行配置与目标环境不一致，拒绝停止服务')
    if http(metadata['frontend_url']+'/api/config').get('environment_id') != env:
        raise RuntimeError('Web/API 环境身份不一致，拒绝停止服务')


def update_release_metadata(env, metadata, release, previous):
    manifest=read(Path(release)/'release.json');release_id=manifest['id']
    metadata.update(release_id=release_id,release_dir=str(release),
        previous_release_id=previous.get('release_id') if previous.get('release_id')!=release_id else previous.get('previous_release_id'),
        commit=manifest['source_commit'],source_digest=manifest['source_digest'],deployed_at=time.strftime('%FT%TZ',time.gmtime()),
        build_version=release_id,frontend_mode='standalone',runtime_config_file=str(config_path(env)),
        binary_versions={'multica':output([Path(release)/'server/bin/multica','version'])})
    metadata.pop('role_release_dirs',None)


def deploy(env, release, timeout=900):
    require_environment(env)
    if env == 'prod' and os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('任务只能通过 pnpm release request 请求生产更新')
    release = Path(release).resolve()
    manifest = read(release/'release.json')
    release_id = manifest['id']
    actual = checksums(release)
    if any(actual.get(k) != v for k,v in manifest['files'].items()): raise RuntimeError('运行文件校验失败')
    with lock(env):
        cfg=read(config_path(env)); m=read(meta_path(env))
        validate_verification_config(env, cfg)
        check_process_ownership(env,m)
        isolation_check(config_only=True)
        check_live_environment(env, m, cfg)
        prepare_database(release, cfg, m['release_dir'])
        if m.get('release_id')==release_id:
            isolation_check()
            print(env,'版本已运行，无需重启',release_id)
            return
        prior=json.loads(json.dumps(m))
        prior['readiness_before']=readiness_snapshot(m['backend_url']+'/healthz')
        if env=='prod':
            prior['published_before']=read(RUN/'published.json') if (RUN/'published.json').exists() else None
        oldcfg={r:process_env(pid) for r,pid in m['pids'].items()}
        write(RUN/'env'/f'{env}-before-deploy.json',oldcfg,True)
        old_cwds={r:str(Path(f'/proc/{pid}/cwd').resolve()) for r,pid in m['pids'].items()}
        write(RUN/'deployments'/f'{env}-previous.json', prior, True)
        drain(env,m,timeout)
        offsets={role:Path(log).stat().st_size if Path(log).exists() else 0 for role,log in m['log_paths'].items()}
        try:
            http(health_url(env)+'/shutdown','POST')
            deadline=time.monotonic()+60
            while exists(m['pids']['daemon']) and time.monotonic()<deadline: time.sleep(.2)
            if exists(m['pids']['daemon']): raise RuntimeError('Daemon 未退出，拒绝强杀')
            owned_stop(m['pids']['web'],'web',old_cwds['web'])
            start_maintenance(env,m)
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
                start_maintenance(env,prior)
                prior['pids']=launch(env,oldrelease,oldcfg,prior,prior_readiness=prior['readiness_before'])
            write(meta_path(env),prior)
            isolation_check()
            print('发布失败，已恢复切换前版本；配置与数据未重置。',file=sys.stderr)
            raise
        update_release_metadata(env,m,release,prior)
        m['log_offsets']={role: min(offsets[role],16*1024*1024) if offsets[role]>64*1024*1024 and role in ('server','web') else offsets[role] for role in offsets}
        write(RUN/'deployments'/f'{env}-previous.json',prior)
        write(meta_path(env),m)
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

# 外部接力器与发布子进程使用同一版本的浏览器验收及其依赖。
BROWSER_OPERATIONS = ('verify-release-browser.mjs', 'lib/goal-test-audit-env.mjs',
                      'lib/goal-test-browser-audit.mjs', 'lib/acceptance-artifacts.mjs')

def copy_browser_operations(destination):
    source = Path(__file__).parent
    for name in BROWSER_OPERATIONS:
        target = Path(destination)/name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes((source/name).read_bytes())

def verify_browser(env='prod',prior_readiness=None):
    check_readiness(read(meta_path(env)),timeout=10,prior_readiness=prior_readiness)
    child_env=os.environ.copy(); child_env['GOAL_TEST_ENV']=env
    run(['node',Path(__file__).with_name('verify-release-browser.mjs')],cwd=ROOT,env=child_env)

def restore(env, timeout=900):
    require_environment(env)
    if os.environ.get('MULTICA_TASK_ID'): raise RuntimeError('恢复应由运行环境外部执行')
    prior = read(RUN/'deployments'/f'{env}-previous.json')
    current = read(meta_path(env))
    old = Path(prior['release_dir'])
    if old.resolve() == Path(current['release_dir']).resolve(): return
    if not old.exists(): raise RuntimeError('临时恢复目录已删除；请从 Git 重新构建')
    # 恢复使用切换前保存的运行目录与配置。
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
    baseline=prior.pop('published_before',None)
    prior['pids']=launch(env, old, read(config_path(env)), prior,prior_readiness=prior.get('readiness_before'))
    write(meta_path(env), prior)
    if env=='prod':
        if baseline is not None: write(RUN/'published.json',baseline,True)
        elif (RUN/'published.json').exists(): (RUN/'published.json').unlink()
    verify_browser(env,prior_readiness=prior.get('readiness_before'))

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
    with release_task_window(env,directory):
        return publish_in_window(env,directory,timeout)

def publish_in_window(env, directory, timeout=900):
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
        operations=Path(directory)/'operations'
        if env in ENVS and (operations/'issue-delivery-worker.py').exists():
            bundle=RUN/'publisher'/('activate-delivery-'+env)
            bundle.mkdir(parents=True,exist_ok=True)
            copy_tree(operations,bundle)
            run([sys.executable,bundle/'issue-delivery-worker.py','--env',env,'--install'],env={**os.environ,'MULTICA_RELEASE_ROOT':str(ROOT)})
    except Exception:
        try:
            restore(env, timeout)
        except Exception as recovery_error:
            raise RecoveryRequired(f'{env} 验收失败且恢复未完成: {recovery_error}') from recovery_error
        raise

def candidate_path(env='int'):
    if env not in ('int', *VERIFICATION_ENVS):
        raise RuntimeError('非法验收环境')
    # 旧 int 记录仍由原入口读取，活动交付不因升级丢失。
    return RUN/'candidate.json' if env == 'int' else RUN/'candidates'/env/'current.json'


def candidate_record_path(version):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+', version):
        raise RuntimeError('非法候选编号')
    return RUN/'candidates'/'records'/f'{version}.json'


def save_candidate(record):
    env = record.get('environment', 'int')
    target = candidate_path(env)
    # 调用者持有该环境的候选锁；历史记录先保存，当前引用随后原子替换。
    write(candidate_record_path(record['id']), record, True)
    write(target, record, True)


def candidate(version=None, env='int'):
    record=read(candidate_path(env))
    if record.get('environment', 'int') != env:
        raise RuntimeError('候选环境与登记不一致')
    if version and record['id'] != version:
        raise RuntimeError('验收环境已由其他候选占用')
    return record


def candidate_lock(env):
    candidate_path(env)
    # int 仍与已有生产发布器共用锁；任务验收槽位使用独立锁。
    return 'publisher' if env == 'int' else 'candidate-'+env

def prepare_int(source, timeout=900, delivery=None, fence=None):
    if not delivery or not delivery.get('publication_lease') or fence is None: raise RuntimeError('int 只能由持有最终集成占用的交付队列准备')
    fence()
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
                        workspace_id=os.environ.get('MULTICA_WORKSPACE_ID',''),publisher_pid=os.getpid(),previous_int_release=read(meta_path('int'))['release_id'])
            if delivery:record.update(delivery)
            save_candidate(record)
            try:
                directory=idle_slot('int')
                manifest=build(source, directory)
                fence()
                publish('int', directory, timeout)
                record.update(status='verifying', validation_identity=validation_identity(source), release_id=manifest['id'], source_digest=manifest['source_digest'],
                              deployed_at=read(meta_path('int'))['deployed_at'])
                discard_previous('int')
            except Exception as error:
                record.update(status='recovery_required' if isinstance(error,RecoveryRequired) else 'failed', error=str(error))
                raise
            finally: save_candidate(record)
            print(json.dumps(record,ensure_ascii=False));return record
    except BlockingIOError:
        raise RuntimeError('其他候选正在构建或发布；结束当前运行后重试') from None

def build_verification(source, delivery_id):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+', delivery_id):
        raise RuntimeError('非法交付编号')
    source, sha = checked_source(source, verification=True)
    target = RUN/'verification-builds'/'commits'/sha
    with lock('verification-build', wait=True):
        if (target/'release.json').exists():
            manifest = read(target/'release.json')
            if manifest['source_commit'] != sha or checksums(target) != manifest['files']:
                raise RuntimeError('验收构建产物身份不匹配')
            return target
        if target.exists():
            assert_unused(target)
            shutil.rmtree(target)
        build(source, target, verification=True)
    return target


def prepare_verification(source, artifact, lease):
    env = lease['name']
    if env not in VERIFICATION_ENVS:
        raise RuntimeError('非法任务验收槽位')
    with lock(candidate_lock(env)):
        active = candidate(env=env) if candidate_path(env).exists() else None
        if active and active['status'] not in ('released','failed','succeeded'):
            raise RuntimeError('验收槽位仍被候选占用')
        source, sha = checked_source(source, verification=True)
        manifest = read(Path(artifact)/'release.json')
        if manifest['source_commit'] != sha or manifest['files'] != checksums(artifact):
            raise RuntimeError('构建产物与候选提交不匹配')
        record = dict(id=sha+'-'+str(time.time_ns()), environment=env,
                      generation=lease['generation'], delivery_id=lease['delivery_id'],
                      commit=sha, source=str(source), status='building',
                      git_common=output(['git','rev-parse','--path-format=absolute','--git-common-dir'],cwd=source),
                      branch=output(['git','symbolic-ref','HEAD'],cwd=source),
                      previous_release=read(meta_path(env))['release_id'])
        save_candidate(record)
        try:
            target = idle_slot(env)
            copy_tree(artifact, target)
            publish(env, target)
            # 3. 候选登记时带上含种子的身份，证据才能区分同一程序在不同测试数据上的结果。
            record.update(status='verifying',release_id=manifest['id'],source_digest=manifest['source_digest'],
                          validation_identity=validation_identity(source, env),deployed_at=read(meta_path(env))['deployed_at'])
        except Exception as error:
            record.update(status='recovery_required',error=str(error))
            raise
        finally:
            save_candidate(record)
        return record


def validate_candidate(record, allow_finalized=False):
    env = record.get('environment', 'int')
    candidate_path(env)
    verification = env in VERIFICATION_ENVS
    current=read(meta_path(env))
    if any(current.get(k) != record.get(k) for k in ('release_id','source_digest','deployed_at')):
        raise RuntimeError('int 在验收后发生变化，必须重新验证')
    if Path(record['source']).exists():
        source, sha=checked_source(record['source'], verification=verification)
        if sha != record['commit'] or source_digest(source)[0] != record['source_digest']:
            raise RuntimeError('候选源码已变化，必须重新联调')
    elif not allow_finalized:
        raise RuntimeError('任务源码目录不存在')
    common=record['git_common']
    if output(['git','--git-dir',common,'rev-parse',record['branch']]) != record['commit']:
        raise RuntimeError('候选分支发生变化')
    published=RUN/'published.json'
    if not verification and published.exists() and subprocess.run(['git','--git-dir',common,'merge-base','--is-ancestor',read(published)['commit'],record['commit']]).returncode:
        raise RuntimeError('候选未包含最新生产提交')
    return current

def accept(version, evidence_file, env='int'):
    with lock(candidate_lock(env), wait=True):
        record=candidate(version, env)
        if record['status'] != 'verifying': raise RuntimeError('候选不在功能验证阶段')
        validate_candidate(record)
        evidence=read(evidence_file)
        if evidence.get('candidate') != version or not evidence.get('summary') or not evidence.get('checks'):
            raise RuntimeError('验收记录需要 candidate、summary 和 checks')
        if any(not isinstance(c,dict) or not c.get('name') or c.get('status')!='passed' or not c.get('evidence') for c in evidence['checks']):
            raise RuntimeError('每项功能检查需要 name、passed 状态和实际 evidence')
        archive=RUN/'accepted-artifacts'/record['id']
        current=read(meta_path(env))
        if not (archive/'release.json').exists():copy_tree(current['release_dir'],archive,excludes=('**/.next/cache/***',))
        manifest=read(archive/'release.json')
        if manifest['files']!=checksums(archive):raise RuntimeError('验收归档产物身份不匹配')
        record.update(status='accepted', acceptance=evidence, accepted_artifact=str(archive))
        save_candidate(record)

def release_candidate(version, env='int'):
    with lock(candidate_lock(env), wait=True):
        record=candidate(version, env)
        if record['status'] in ('queued','publishing'): raise RuntimeError('生产更新已提交，不能释放')
        if record['status']=='recovery_required':
            isolation_check()
            published=read(RUN/'published.json')
            current=read(meta_path('prod'))
            if published['commit'] != current['commit'] or published['source_digest'] != current['source_digest']:
                raise RuntimeError('生产尚未恢复到已发布基线')
        record.update(status='released');save_candidate(record)

def request_path(request_id):
    if not re.fullmatch(r'[a-zA-Z0-9_-]+',request_id): raise RuntimeError('非法请求编号')
    return RUN/'publish-requests'/f'{request_id}.json'

def batch_approval_records(record, records, meta, profile):
    """1. 组合发布逐个成员核对：每个成员都要有自己的有效授权、固定提交和同一候选。"""
    members=record.get('batch_members') or []
    backend=meta['backend_url'].rstrip('/')
    for member in members:
        delivery=next((d for d in records if d['id']==member['delivery_id']),None)
        url=backend+'/api/delivery-queue/'+member['delivery_id']+'/authorization'
        request=urllib.request.Request(url,headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id']})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=15) as response:authorization=json.load(response)
        if authorization.get('publication_generation')!=record.get('publication_lease',{}).get('generation'):
            raise RuntimeError('最终发布占用代次不匹配')
        if authorization.get('business_error'):raise RuntimeError(authorization['business_error'])
        if not authorization.get('ready'):
            raise RuntimeError('成员 %s 的发布授权或用户要求已变化，需要负责人重新核对'%member['delivery_id'])
        if (not delivery or delivery['phase']!='publishing' or not delivery.get('approved_by')
                or not delivery.get('release_validated') or delivery['source_commit']!=member['source_commit']
                or delivery['candidate_id']!=record['id']):
            raise RuntimeError('成员 %s 的生产批准或组合复核已失效'%member['delivery_id'])
        if subprocess.run(['git','--git-dir',record['git_common'],'merge-base','--is-ancestor',member['source_commit'],record['commit']]).returncode:
            raise RuntimeError('发布候选未包含成员 %s 的批准提交'%member['delivery_id'])

def check_delivery_approval(record):
    if record.get('environment', 'int') != 'int':
        raise RuntimeError('任务验收候选不能直接发布生产，必须完成 int 集成验收')
    if not record.get('delivery_id') or record.get('delivery_env')!='prod':
        raise RuntimeError('生产更新需要原任务的交付审核记录，请在任务中确认生产发布')
    meta=read(meta_path('prod'));profile=read(meta['daemon_profile_path'])
    req=urllib.request.Request(meta['backend_url'].rstrip('/')+'/api/delivery-queue',headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id']})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=15) as response: records=json.load(response)
    if record.get('batch_members'):
        return batch_approval_records(record, records, meta, profile)
    delivery=next((d for d in records if d['id']==record['delivery_id']),None)
    authorization_url=meta['backend_url'].rstrip('/')+'/api/delivery-queue/'+record['delivery_id']+'/authorization'
    authorization_request=urllib.request.Request(authorization_url,headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id']})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(authorization_request,timeout=15) as response: authorization=json.load(response)
    if authorization.get('publication_generation') != record.get('publication_lease',{}).get('generation'):raise RuntimeError('最终发布占用代次不匹配')
    if authorization.get('business_error'):raise RuntimeError(authorization['business_error'])
    if not authorization.get('ready'):raise RuntimeError('发布授权或用户要求已变化，需要负责人重新核对')
    if not delivery or delivery['phase']!='publishing' or not delivery.get('approved_by') or not delivery.get('release_validated') or delivery['source_commit']!=record.get('approved_source') or delivery['candidate_id']!=record['id']:
        raise RuntimeError('生产批准或联调复核已失效')
    if subprocess.run(['git','--git-dir',record['git_common'],'merge-base','--is-ancestor',delivery['source_commit'],record['commit']]).returncode:
        raise RuntimeError('发布候选未包含用户批准的任务提交')

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
        check_delivery_approval(record)
        validate_candidate(record)
        # 发布来源为提交请求的这轮运行，可能与开始联调的运行不同。
        record.update(task_id=os.environ.get('MULTICA_TASK_ID',''), server_url=os.environ.get('MULTICA_SERVER_URL',''),
                      workspace_id=os.environ.get('MULTICA_WORKSPACE_ID',''), timeout=timeout,
                      status='queued', unit='multica-publish-'+version)
        runner=RUN/'publisher'/version/'release.py'
        runner.parent.mkdir(parents=True,exist_ok=True)
        runner.write_bytes(Path(__file__).read_bytes());runner.chmod(0o700)
        copy_browser_operations(runner.parent)
        (runner.parent/'check-release-database.mjs').write_bytes(Path(__file__).with_name('check-release-database.mjs').read_bytes())
        (runner.parent/'release-maintenance.py').write_bytes(Path(__file__).with_name('release-maintenance.py').read_bytes())

        write(target,record,True);save_candidate(record)
        try:
            run(['systemd-run','--quiet','--collect','--unit',record['unit'],'--property=Type=exec','--property=KillMode=process',
                 '--working-directory',ROOT,'--setenv=MULTICA_RELEASE_ROOT='+str(ROOT),sys.executable,runner,'worker',version])
        except Exception:
            record.update(status='accepted');save_candidate(record)
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


def publication_api(record,action):
    meta=read(meta_path('prod')); profile=read(meta['daemon_profile_path'])
    body=dict(record['publication_lease'],action=action)
    req=urllib.request.Request(meta['backend_url'].rstrip('/')+'/api/delivery-queue/publication', data=json.dumps(body).encode(),
        headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json'})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=10) as response:return json.load(response)

@contextlib.contextmanager
def publication_guard(record):
    publication_api(record,'check')
    stopped=threading.Event(); failures=[]
    def renew():
        last=time.monotonic()
        while not stopped.wait(10):
            try:publication_api(record,'heartbeat');last=time.monotonic()
            except Exception as e:
                if time.monotonic()-last>45:failures.append(str(e));return
    thread=threading.Thread(target=renew,daemon=True);thread.start()
    def check():
        if failures:raise RecoveryRequired('发布占用续期失败：'+failures[0])
        publication_api(record,'check')
    try:yield check
    finally:stopped.set();thread.join(timeout=11)

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
                check_delivery_approval(record)
                record.update(status='publishing');write(target,record,True);save_candidate(record)
                directory=idle_slot('prod')
                copy_tree(Path(current['release_dir']),directory,excludes=('**/.next/cache/***',))
                with publication_guard(record) as fence:
                    fence()
                    publish('prod',directory,record['timeout'])
                write(RUN/'published.json',{'commit':record['commit'],'source_digest':record['source_digest'],'candidate':request_id},True)
                record.update(status='succeeded');write(target,record,True);save_candidate(record)
                finish_cleanup(record)
        except Exception as error:
            record.update(status='recovery_required' if isinstance(error,RecoveryRequired) else 'failed',error=str(error))
            write(target,record,True)
            with lock('publisher',wait=True):
                if candidate_path().exists() and candidate()['id']==request_id: save_candidate(record)
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
        # Daemon 使用独立 profile 日志，标准发布日志路径可能没有文件；缺失本身不是致命日志。
        if not p.exists():
            results[role]={'exists':False,'fatal_errors':0}
            continue
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

def require_control_directory():
    control = Path('/data/ida/multica').resolve()
    script = Path(__file__).resolve()
    if ROOT != control or (script != control/'scripts/release-environments.py' and
                           control/'.run/publisher' not in script.parents):
        raise RuntimeError('请通过 pnpm --dir /data/ida/multica release 调用固定发布入口；任务目录只用作 --source')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['preflight','int','accept','release','request','request-status','worker','cleanup','restore','verify','status','verify-logs','backup','reset','delivery-worker'])
    parser.add_argument('target',nargs='?')
    parser.add_argument('--source')
    parser.add_argument('--evidence')
    parser.add_argument('--timeout',type=float,default=900)
    parser.add_argument('--env',choices=ENVS,default='prod')
    a=parser.parse_args()
    if a.timeout<=0: parser.error('timeout 必须大于 0')
    require_control_directory()
    if a.command=='preflight':
        if not a.source: parser.error('preflight 需要 --source 任务 worktree')
        result=executor_preflight(a.source,integration=True)
        result['readiness']=readiness_snapshot(read(meta_path(a.env))['backend_url']+'/healthz')
        result['readiness_note']='当前运行源的就绪快照；候选新增迁移仍由原生发布门禁核验'
        if result['readiness']['http_status']!=200: result['status']='blocked'
        print(json.dumps(result,ensure_ascii=False))
        if result['status']=='blocked': raise SystemExit(1)
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
    elif a.command=='delivery-worker':
        run([sys.executable,Path(__file__).with_name('issue-delivery-worker.py'),'--env',a.env,'--install'],env={**os.environ,'MULTICA_RELEASE_ROOT':str(ROOT)})
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
