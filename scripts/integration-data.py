"""固定 int 数据维护：核对共享实例中的专属数据库。"""
import json
import hashlib
import importlib.util
import os
import re
import secrets
import shutil
import subprocess
import time
import urllib.parse
import urllib.request
from pathlib import Path


def identity(r, *, check_process=True):
    """核对配置、运行进程和 PostgreSQL 容器，不返回凭据。"""
    if os.environ.get('MULTICA_TASK_ID'):
        raise r.RecoveryRequired('联调数据维护必须由外部发布器执行')
    cfg = r.read(r.config_path('int'))
    prod = r.read(r.config_path('prod'))
    meta = r.read(r.meta_path('int'))
    target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
    production = urllib.parse.urlparse(prod['server']['DATABASE_URL'])
    if (cfg['server'].get('MULTICA_ENV_ID') != 'int' or prod['server'].get('MULTICA_ENV_ID') != 'prod'
            or meta.get('environment') != 'int' or target.scheme not in ('postgres', 'postgresql')
            or production.scheme not in ('postgres', 'postgresql') or not production.username or not production.path.strip('/')
            or target.hostname not in ('localhost', '127.0.0.1') or production.hostname not in ('localhost', '127.0.0.1')
            or not target.port or target.port != production.port
            or target.path != '/multica_goal_test_int' or target.path == production.path
            or not re.fullmatch(r'[a-z_][a-z0-9_]*', target.username or '')):
        raise r.RecoveryRequired('联调数据库配置与生产隔离身份不符')
    if check_process:
        r.check_process_ownership('int', meta)
        r.check_live_environment('int', meta, cfg)
    containers = r.output(['docker', 'ps', '--filter', 'publish=' + str(target.port), '--format', '{{.ID}}']).splitlines()
    if len(containers) != 1:
        raise r.RecoveryRequired('无法唯一确认联调 PostgreSQL 实例')
    inspected = json.loads(r.output(['docker', 'inspect', containers[0]]))[0]
    labels = inspected['Config'].get('Labels') or {}
    ports = inspected['NetworkSettings']['Ports'].get('5432/tcp') or []
    if (labels.get('com.docker.compose.project') != 'multica-postgres'
            or labels.get('com.docker.compose.service') != 'postgres'
            or len(ports) != 1 or ports[0].get('HostIp') != '127.0.0.1'
            or ports[0].get('HostPort') != str(target.port)):
        raise r.RecoveryRequired('联调 PostgreSQL 容器或端口身份不符')
    return dict(environment='int', container_id=inspected['Id'], port=target.port,
                database=target.path[1:], database_user=target.username,
                release_id=meta['release_id'])


def sql(target, database, statement):
    """通过已核对的容器执行 SQL；调用者负责发布租约及进程围栏。"""
    if database not in ('postgres', target['database']):
        raise RuntimeError('联调数据库操作目标不符')
    result = subprocess.run(['docker', 'exec', '-i', target['container_id'], 'psql',
                             '-U', target['database_user'], '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'],
                            input=statement, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError('联调数据库操作失败：' + (result.stderr or result.stdout).strip()[:800])
    return result.stdout.strip()


def check_cleaned_idle(r, state, meta):
    """只接受完成清理且身份、进程和数据库均可证明空闲的固定 int。"""
    if state.get('stage') != 'cleaned' or state.get('environment') != 'int' or meta.get('pids') != {}:
        raise r.RecoveryRequired('联调待命记录或进程状态不完整')
    target = identity(r, check_process=False)
    if target != state.get('target'):
        raise r.RecoveryRequired('联调待命身份已变化')
    release = Path(meta.get('release_dir', '')).resolve()
    if release not in ((r.RUN/'runtime/int/a').resolve(), (r.RUN/'runtime/int/b').resolve()):
        raise r.RecoveryRequired('联调待命程序目录身份不符')
    manifest = r.read(release/'release.json')
    if (manifest.get('id') != meta.get('release_id') or manifest.get('source_commit') != meta.get('commit')
            or r.checksums(release) != manifest.get('files')):
        raise r.RecoveryRequired('联调待命程序身份或完整性不符')
    for key in ('release_dir', 'daemon_workspaces_root'):
        if not meta.get(key) or not Path(meta[key]).is_absolute():
            raise r.RecoveryRequired('联调待命目录身份不完整')
        r.assert_unused(meta[key])
    if sql(target, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='multica_goal_test_int'") != '0':
        raise r.RecoveryRequired('联调数据库仍有消费者')
    health_port = urllib.parse.urlparse(r.health_url('int')).port
    for port in (meta['backend_port'], health_port):
        if re.search(r'LISTEN', r.output(['ss', '-ltnp', f'( sport = :{port} )'])):
            raise r.RecoveryRequired('联调业务监听端口仍被占用')
    listener = r.output(['ss', '-ltnp', f"( sport = :{meta['frontend_port']} )"])
    if re.search(r'LISTEN', listener):
        marker = r.RUN/'int-maintenance.json'
        if not marker.is_file(): raise r.RecoveryRequired('联调前端监听没有维护身份')
        pid = r.read(marker).get('pid')
        listener_pids = {int(value) for value in re.findall(r'pid=(\d+),', listener)}
        if not isinstance(pid, int) or not r.exists(pid) or listener_pids != {pid}:
            raise r.RecoveryRequired('联调维护监听身份不匹配')
        args = Path('/proc', str(pid), 'cmdline').read_bytes().split(b'\0')
        if not any(Path(os.fsdecode(a)).name == 'release-maintenance.py' for a in args if a):
            raise r.RecoveryRequired('联调维护进程身份不匹配')
    return True


def clear_database(target):
    """只清理 int 的业务数据，迁移记录由下一次结构初始化维护。"""
    if target.get('environment') != 'int' or target.get('database') != 'multica_goal_test_int':
        raise RuntimeError('联调数据库身份不符')
    active = sql(target, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='multica_goal_test_int'")
    if active != '0':
        raise RuntimeError('联调数据库仍有消费者，拒绝清理')
    sql(target, target['database'], '''DO $$ DECLARE targets text; BEGIN
      IF EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class p ON p.oid=c.confrelid JOIN pg_namespace pn ON pn.oid=p.relnamespace JOIN pg_class child ON child.oid=c.conrelid JOIN pg_namespace cn ON cn.oid=child.relnamespace WHERE c.contype='f' AND pn.nspname='public' AND p.relname<>'schema_migrations' AND (cn.nspname<>'public' OR child.relname='schema_migrations')) THEN
        RAISE EXCEPTION '联调表被清理范围外的表引用';
      END IF;
      SELECT string_agg(format('%I.%I', n.nspname, c.relname), ', ' ORDER BY c.relname) INTO targets FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname<>'schema_migrations';
      IF targets IS NULL THEN RAISE EXCEPTION '联调数据库没有业务表'; END IF;
      EXECUTE 'TRUNCATE TABLE ' || targets || ' RESTART IDENTITY';
    END $$;''')


def initialize_database(r, seed, target, pointer, release, account, password, fence):
    """在服务停机且持有发布围栏时恢复基线结构与已验收 SQL 样本。"""
    if (not pointer or not re.fullmatch(r'[a-zA-Z0-9_-]+', pointer.get('seed_id', ''))
            or not re.fullmatch(r'[a-zA-Z0-9_.@-]+', account or '') or not password):
        raise r.RecoveryRequired('联调种子或测试账号身份不完整')
    path, manifest, artifact = seed.read_seed(pointer['seed_id'])
    source = seed.verified_sql_seed(path, manifest)
    if not source or pointer.get('dump_sha256') != artifact['sha256']:
        raise r.RecoveryRequired('联调 SQL 种子未验收或身份不匹配')
    release = Path(release).resolve()
    seed.validate_seed_release(manifest, release)
    if target.get('environment') != 'int' or target.get('database') != 'multica_goal_test_int':
        raise r.RecoveryRequired('联调数据库身份不符')
    def check():
        fence()
        current = identity(r, check_process=False)
        if current != target:
            raise r.RecoveryRequired('联调数据库维护期间环境身份已变化')
    check()
    if sql(target, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='multica_goal_test_int'") != '0':
        raise r.RecoveryRequired('联调数据库仍有消费者，拒绝初始化')
    # 使用独立语句，允许中断后在同一围栏与同一容器上重新开始；不使用 FORCE。
    sql(target, 'postgres', 'DROP DATABASE IF EXISTS multica_goal_test_int')
    check()
    sql(target, 'postgres', 'CREATE DATABASE multica_goal_test_int OWNER ' + target['database_user'])
    cfg = r.read(r.config_path('int'))
    r.run([release/'server/bin/migrate', 'up'], cwd=release/'server',
          env={**cfg['server'], 'MULTICA_MIGRATION_BASELINE':'true'})
    check()
    content = Path(source).read_text()
    sql(target, target['database'], 'BEGIN;\nSET LOCAL session_replication_role=replica;\n' + content + '\nSET LOCAL session_replication_role=origin;\nCOMMIT;\n')
    r.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), 'int', 'direct', 'unused'],
          cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})
    migrations = sql(target, target['database'], "SELECT count(*)||':'||COALESCE(md5(string_agg(version::text,'|' ORDER BY version)),'') FROM schema_migrations")
    columns = sql(target, target['database'], "SELECT COALESCE(md5(string_agg(table_name||'.'||column_name||':'||data_type,'|' ORDER BY table_name,column_name)),'') FROM information_schema.columns WHERE table_schema='public'")
    fingerprint = hashlib.sha256((migrations+'|'+columns).encode()).hexdigest()
    if fingerprint != (manifest.get('schema') or {}).get('fingerprint'):
        raise r.RecoveryRequired('联调数据库结构与 SQL 种子不一致')
    for table in ('workspace', 'user', 'issue', 'comment'):
        count = int(sql(target, target['database'], 'SELECT count(*) FROM public."'+table+'"'))
        if count < max(1, int((manifest.get('fixture', {}).get('row_counts') or {}).get(table, 0))):
            raise r.RecoveryRequired('联调 SQL 种子缺少样本：'+table)
    check()
    password_hash = seed.password_hash(password)
    updated = sql(target, target['database'], "UPDATE public.\"user\" SET account='%s',password_hash='%s' WHERE account='verification'" % (account, password_hash))
    if updated != 'UPDATE 1':
        raise r.RecoveryRequired('联调测试账号绑定失败')
    return dict(seed_id=pointer['seed_id'], seed_sha256=artifact['sha256'], schema_fingerprint=fingerprint)


def clear_uploads(r, cfg):
    """只清理固定 int 的上传目录，不跟随符号链接。"""
    expected = r.RUN/'uploads'/'goal-test-int'
    target = Path(cfg['server']['LOCAL_UPLOAD_DIR'])
    if target != expected or target.resolve() != target or target.is_symlink():
        raise r.RecoveryRequired('联调上传目录身份不符')
    descriptor = os.open(target, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        with os.scandir(descriptor) as entries:
            for item in entries:
                if item.is_dir(follow_symlinks=False): shutil.rmtree(item.name, dir_fd=descriptor)
                else: os.unlink(item.name, dir_fd=descriptor)
    finally:
        os.close(descriptor)


def clear_environment(r, batch, fence):
    """排空并清理固定 int；记录各阶段，允许同一批次中断后接续。"""
    if not re.fullmatch(r'[a-zA-Z0-9_-]+', batch or ''):
        raise r.RecoveryRequired('联调清理缺少批次身份')
    journal = r.RUN/'integration-data.json'
    with r.lock('publisher', wait=True):
        fence()
        state = r.read(journal) if journal.exists() else {}
        resuming = state.get('stage') in ('draining', 'stopping', 'cleaning', 'cleaned')
        if resuming:
            if state.get('batch') != batch:
                raise r.RecoveryRequired('未完成的联调清理属于其他批次')
            target = identity(r, check_process=False)
            if target != state.get('target'):
                raise r.RecoveryRequired('联调清理中环境身份已变化')
            if state['stage'] == 'cleaned': return state
            meta = r.read(r.meta_path('int'))
            r.check_process_ownership('int', meta, allow_unhealthy=True)
        else:
            if state and state.get('stage') != 'ready':
                raise r.RecoveryRequired('联调初始化尚未完成，不能启动清理')
            target = identity(r)
            meta = r.read(r.meta_path('int'))
            state = dict(environment='int', batch=batch, stage='draining', target=target)
            r.write(journal, state, True)
        cfg = r.read(r.config_path('int'))
        if state['stage'] == 'draining':
            daemon = meta.get('pids', {}).get('daemon')
            if not daemon or not r.exists(daemon):
                raise r.RecoveryRequired('联调 Daemon 已退出，无法证明任务排空')
            r.drain('int', meta, timeout=110)
            fence()
            try:
                pending = sql(target, target['database'], "SELECT count(*) FROM agent_task_queue WHERE status NOT IN ('completed','failed','cancelled')")
                if pending != '0':
                    raise r.RecoveryRequired('联调环境仍有未完成任务，保留现场')
                accounts = [part.strip() for part in cfg['server'].get('ALLOWED_ACCOUNTS', '').split(',') if part.strip()]
                if len(accounts) != 1 or not re.fullmatch(r'[a-zA-Z0-9_.@-]+', accounts[0]):
                    raise r.RecoveryRequired('联调环境需要明确唯一的测试账号')
                users=json.loads(sql(target,target['database'],"SELECT COALESCE(json_agg(json_build_object('account',account,'password_hash',password_hash)),'[]'::json)::text FROM public.\"user\" WHERE COALESCE(password_hash,'')<>''"))
                if len(users)>1:
                    raise r.RecoveryRequired('联调环境存在多个密码账号，无法自动确定恢复身份')
                login=users[0] if users else dict(account=accounts[0],password_hash='')
                if not re.fullmatch(r'[a-z0-9._-]{3,64}',login.get('account') or ''):
                    raise r.RecoveryRequired('联调登录账号无法安全恢复')
                state['login'] = login
                r.write(journal,state,True)
            except Exception:
                fence()
                r.http(r.health_url('int')+'/drain', 'DELETE')
                raise
            state['stage'] = 'stopping';r.write(journal, state, True)
        if state['stage'] == 'stopping':
            daemon = meta.get('pids', {}).get('daemon')
            if daemon and r.exists(daemon):
                r.http(r.health_url('int')+'/shutdown', 'POST')
                deadline = time.monotonic()+30
                while r.exists(daemon) and time.monotonic()<deadline: time.sleep(.2)
                if r.exists(daemon): raise r.RecoveryRequired('联调 Daemon 未退出')
            release = Path(meta['release_dir'])
            web = meta.get('pids', {}).get('web')
            if web: r.owned_stop(web, 'web', release/'apps/web')
            r.start_maintenance('int', meta)
            server = meta.get('pids', {}).get('server')
            if server: r.owned_stop(server, 'server', release)
            meta['pids'] = {};r.write(r.meta_path('int'), meta, True)
            state['stage'] = 'cleaning';r.write(journal, state, True)
        fence()
        if identity(r, check_process=False) != target:
            raise r.RecoveryRequired('联调清理前环境身份已变化')
        clear_database(target)
        fence()
        clear_uploads(r, cfg)
        state['stage'] = 'cleaned';r.write(journal, state, True)
        return state


def bind_account(r, seed, release, account, password, fence):
    """启动临时 API，绑定该联调环境自己的登录令牌和工作区。"""
    meta = r.read(r.meta_path('int'))
    cfg = r.read(r.config_path('int'))
    if any(r.exists(pid) for pid in meta.get('pids', {}).values()):
        raise r.RecoveryRequired('联调环境仍有运行进程，不能绑定测试账号')
    if Path(meta['release_dir']).resolve() != Path(release).resolve():
        raise r.RecoveryRequired('联调账号绑定的程序版本不符')
    profile_path = Path(meta['daemon_profile_path'])
    if not profile_path.is_absolute() or profile_path.is_symlink() or not profile_path.is_file():
        raise r.RecoveryRequired('联调 Daemon 配置身份不符')
    profile_stat = profile_path.stat()
    profile = r.read(profile_path)
    if profile.get('server_url', '').rstrip('/') != meta['backend_url'].rstrip('/') or profile.get('workspaces_root') != meta['daemon_workspaces_root']:
        raise r.RecoveryRequired('联调 Daemon 配置与环境不一致')
    fence()
    pid = r.start([Path(release)/'server/bin/server'], Path(release), cfg['server'], meta['log_paths']['server'])
    try:
        meta['pids'] = {'server':pid};r.write(r.meta_path('int'),meta,True)
        r.ready(meta['backend_url']+'/health', pid=pid)
        login = seed.i.api(meta['backend_url'], '/auth/login', dict(account=account,password=password))
        seed.i.api(meta['backend_url'], '/api/me/onboarding/complete', dict(completion_path='skip_existing'), login['token'])
        request = urllib.request.Request(meta['backend_url']+'/api/workspaces', headers={'Authorization':'Bearer '+login['token']})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=30) as response:
            workspaces = json.load(response)
        if len(workspaces) != 1 or not workspaces[0].get('id'):
            raise r.RecoveryRequired('联调种子必须包含一个可登录工作区')
        fence()
        profile.update(token=login['token'], workspace_id=workspaces[0]['id'])
        r.write(profile_path, profile, True)
        os.chown(profile_path, profile_stat.st_uid, profile_stat.st_gid)
        return workspaces[0]['id']
    finally:
        r.owned_stop(pid, 'server', Path(release))
        meta['pids'] = {};r.write(r.meta_path('int'),meta,True)


def apply_test_runtime(r):
    spec = importlib.util.spec_from_file_location('test_agent_control', Path(__file__).with_name('test-agent-control.py'))
    control = importlib.util.module_from_spec(spec);spec.loader.exec_module(control)
    control.refresh_default(r.ROOT, 'int')
    r.run(['node', str(Path(__file__).with_name('test-agent-models.mjs')), 'int', 'apply'],
          cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})


def restore_login(r, target, state, account, fence):
    """初始化使用临时口令绑定 Daemon，随后恢复联调账号原有密码。"""
    login=state.get('login') or {}
    if not login.get('password_hash'):return
    if login.get('account')!=account:
        raise r.RecoveryRequired('联调账号与清理前身份不一致')
    fence()
    value=login['password_hash'].replace("'", "''")
    count=sql(target,target['database'],"WITH changed AS (UPDATE public.\"user\" SET password_hash='%s' WHERE account='%s' RETURNING 1) SELECT count(*) FROM changed" % (value,account))
    if count!='1':raise r.RecoveryRequired('联调账号密码恢复失败')


def restore_environment(r, seed, batch, pointer, fence):
    """按已验收的 SQL 种子恢复已清理的 int；中断时重入同一批次。"""
    if not re.fullmatch(r'[a-zA-Z0-9_-]+', batch or ''):
        raise r.RecoveryRequired('联调恢复缺少批次身份')
    journal = r.RUN/'integration-data.json'
    with r.lock('publisher', wait=True):
        fence()
        if not journal.exists(): raise r.RecoveryRequired('联调环境没有清理记录')
        state = r.read(journal)
        if state.get('stage') == 'ready' and state.get('batch') == batch:
            if state.get('seed') != pointer:
                raise r.RecoveryRequired('联调批次已开始，不能更换 SQL 种子')
            identity(r)
            return state
        if state.get('stage') not in ('cleaned', 'initializing', 'starting'):
            raise r.RecoveryRequired('联调环境未完成清理或正在执行其他维护')
        if state['stage'] != 'cleaned' and state.get('batch') != batch:
            raise r.RecoveryRequired('未完成的联调恢复属于其他批次')
        restarting = state['stage'] == 'starting'
        if not pointer or not re.fullmatch(r'[a-zA-Z0-9_-]+', pointer.get('seed_id', '')):
            raise r.RecoveryRequired('未配置联调 SQL 种子')
        path, manifest, artifact = seed.read_seed(pointer['seed_id'])
        if artifact['sha256'] != pointer.get('dump_sha256') or not seed.verified_sql_seed(path, manifest):
            raise r.RecoveryRequired('联调 SQL 种子身份不符或未验收')
        meta = r.read(r.meta_path('int'))
        cfg = r.read(r.config_path('int'))
        if state['stage'] == 'cleaned':
            target = identity(r, check_process=False)
            if target != state.get('target') or any(r.exists(pid) for pid in meta.get('pids', {}).values()):
                raise r.RecoveryRequired('联调清理后运行身份已变化')
            accounts = [part.strip() for part in cfg['server'].get('ALLOWED_ACCOUNTS', '').split(',') if part.strip()]
            if len(accounts) != 1 or not re.fullmatch(r'[a-zA-Z0-9_.@-]+', accounts[0]):
                raise r.RecoveryRequired('联调环境需要明确唯一的测试账号')
            account=(state.get('login') or {}).get('account') or accounts[0]
            if not re.fullmatch(r'[a-z0-9._-]{3,64}',account):
                raise r.RecoveryRequired('联调登录账号无法安全恢复')
            running = Path(meta['release_dir']).resolve()
            selected = seed.restore_release(manifest, running)
            package = r.read(Path(selected)/'release.json')
            if r.checksums(selected) != package.get('files'):
                raise r.RecoveryRequired('联调种子基线产物校验失败')
            if Path(selected).resolve() != running:
                destination = r.idle_slot('int')
                r.copy_tree(selected, destination)
                if r.checksums(destination) != package.get('files'):
                    raise r.RecoveryRequired('联调种子基线复制校验失败')
                selected = destination
            state.update(stage='initializing', batch=batch, seed=pointer, restore_release=str(Path(selected).resolve()),
                         restore_release_id=package['id'], account=account, password=secrets.token_urlsafe(24))
            r.write(journal, state, True)
        elif state.get('seed') != pointer:
            raise r.RecoveryRequired('联调恢复中不能更换 SQL 种子')
        # 修复中断记录里超出登录接口 32 字符上限的临时口令；数据库会按同一目标重新初始化。
        if not isinstance(state.get('password'),str) or not 1 <= len(state['password']) <= 32:
            state['password']=secrets.token_urlsafe(24);r.write(journal,state,True)
        release = Path(state['restore_release']).resolve()
        package = r.read(release/'release.json')
        if package.get('id') != state.get('restore_release_id') or r.checksums(release) != package.get('files'):
            raise r.RecoveryRequired('联调恢复中的程序产物已变化')
        seed.validate_seed_release(manifest, release)
        r.check_process_ownership('int', meta, allow_unhealthy=True)
        daemon = meta.get('pids', {}).get('daemon')
        if daemon and r.exists(daemon):
            r.drain('int', meta, timeout=110)
            r.http(r.health_url('int')+'/shutdown', 'POST')
            deadline = time.monotonic()+30
            while r.exists(daemon) and time.monotonic()<deadline: time.sleep(.2)
            if r.exists(daemon): raise r.RecoveryRequired('联调 Daemon 未退出')
        for role in ('web', 'server'):
            pid = meta.get('pids', {}).get(role)
            if pid and r.exists(pid):
                old = Path(meta['release_dir'])
                r.owned_stop(pid, role, old/'apps/web' if role=='web' else old)
        meta['pids'] = {}
        current = identity(r, check_process=False)
        previous = state.get('target') or {}
        if any(current.get(key) != previous.get(key) for key in ('environment','container_id','port','database','database_user')):
            raise r.RecoveryRequired('联调恢复期间数据库身份已变化')
        if meta.get('release_id') != package['id']:
            r.update_release_metadata('int', meta, release, dict(meta))
        r.write(r.meta_path('int'), meta, True)
        target = identity(r, check_process=False)
        if any(target.get(key) != previous.get(key) for key in ('environment','container_id','port','database','database_user')) or target['release_id'] != package['id']:
            raise r.RecoveryRequired('联调恢复期间数据库或程序身份已变化')
        if restarting:
            pending = sql(target, target['database'], "SELECT count(*) FROM agent_task_queue WHERE status NOT IN ('completed','failed','cancelled')")
            if pending != '0':
                raise r.RecoveryRequired('联调恢复期间出现未完成任务，保留现场')
        state['target'] = target;state['stage'] = 'initializing';r.write(journal,state,True)
        initialize_database(r, seed, target, pointer, release, state['account'], state['password'], fence)
        bind_account(r, seed, release, state['account'], state['password'], fence)
        restore_login(r, target, state, state['account'], fence)
        fence()
        state['stage'] = 'starting';r.write(journal,state,True)
        meta = r.read(r.meta_path('int'));meta['pids'] = {}
        try:
            meta['pids'] = r.launch('int', release, cfg, meta)
        finally:
            r.write(r.meta_path('int'),meta,True)
        r.check_process_ownership('int',meta)
        r.check_live_environment('int',meta,cfg)
        apply_test_runtime(r)
        r.verify_browser('int')
        fence()
        state['stage'] = 'ready';r.write(journal,state,True)
        return state
