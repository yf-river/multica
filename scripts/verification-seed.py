#!/usr/bin/env python3
"""固定验收种子生命周期：唯一当前版本、按目标结构重建、恢复验收与按槽位维护。

种子目录只增不改：`<seeds>/<seed-id>/seed.dump` 与 `seed.json`。只有按目标候选结构重建、
导入清理并通过恢复验收的种子才会成为 `.run/verification-seed.json` 指向的当前版本；
构建或恢复失败不替换当前版本，旧版本继续可用。
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('verification-environments.py'))
i = importlib.util.module_from_spec(spec)
spec.loader.exec_module(i)
r = i.r

SEEDS = Path(os.environ.get('MULTICA_VERIFICATION_SEEDS', '/data/cbs/multica-verification/seeds'))
SCRUB = Path(__file__).with_name('verification-scrub.json')
BUILD_PREFIX = 'multica_seed_build_'
FIXTURE_VERSION = 'business-samples-v1'


def current_path():
    return r.RUN/'verification-seed.json'


def now():
    return time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())


def digest(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def postgres(config):
    record = r.read(config)
    inspected = json.loads(r.output(['docker', 'inspect', record['container']]))[0]
    if inspected['Id'] != record['container_id'] or inspected['Config'].get('Labels', {}).get('multica.purpose') != 'verification-pool':
        raise RuntimeError('验收数据库实例身份已变化')
    ports = inspected['NetworkSettings']['Ports']['5432/tcp']
    if len(ports) != 1 or ports[0]['HostIp'] != '127.0.0.1':
        raise RuntimeError('验收数据库端口身份不匹配')
    credentials = Path(record['credentials_file']).read_text().strip()
    if not credentials.startswith('POSTGRES_PASSWORD='):
        raise RuntimeError('验收实例管理凭据格式错误')
    return record['container'], ports[0]['HostPort'], credentials.split('=', 1)[1]


def sql(container, database, statement):
    completed = subprocess.run(['docker', 'exec', '-i', container, 'psql', '-U', 'postgres', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'], input=statement, text=True, capture_output=True)
    if completed.returncode:
        # 数据库报错要带回具体语句结果，否则恢复失败只能看到退出码。
        raise RuntimeError('数据库操作失败: ' + (completed.stderr or completed.stdout).strip()[:800])
    return completed.stdout.strip()


def table_exists(container, database, table):
    return sql(container, database, "SELECT to_regclass('public.%s') IS NOT NULL" % table) == 't'


def counter(container, database, statement):
    return int(sql(container, database, statement) or 0)


def password_hash(password):
    salt = secrets.token_bytes(16)
    value = hashlib.pbkdf2_hmac('sha256', password.encode(), salt, 210000, 32)
    return 'pbkdf2_sha256$210000$%s$%s' % (
        base64.b64encode(salt).decode().rstrip('='),
        base64.b64encode(value).decode().rstrip('='),
    )


def scrub_identity():
    rules = json.loads(SCRUB.read_text())
    return dict(version=rules['version'], sha256=digest(SCRUB))


def schema_identity(container, database):
    """结构指纹同时覆盖已应用迁移和实际表列；只有迁移号相同不算同结构。"""
    migrations = sql(container, database, "SELECT count(*)||':'||COALESCE(md5(string_agg(version::text,'|' ORDER BY version)),'') FROM schema_migrations")
    columns = sql(container, database, "SELECT COALESCE(md5(string_agg(table_name||'.'||column_name||':'||data_type,'|' ORDER BY table_name,column_name)),'') FROM information_schema.columns WHERE table_schema='public'")
    tables = counter(container, database, "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
    applied = int(migrations.split(':')[0]) if ':' in migrations else 0
    return dict(fingerprint=hashlib.sha256((migrations + '|' + columns).encode()).hexdigest(), migrations=applied, tables=tables)


CONTROL_ENVIRONMENT = 'prod'


def control():
    # 修改 1. 维护和种子登记走控制端接口；演练可在隔离槽位上跑同一套接口，生产仍默认 prod。
    meta = r.read(r.meta_path(CONTROL_ENVIRONMENT))
    profile = r.read(meta['daemon_profile_path'])
    return meta, profile


def control_api(route, body=None):
    """维护与种子身份登记走控制端已有接口，脚本不直接写控制库。"""
    meta, profile = control()
    request = urllib.request.Request(meta['backend_url'].rstrip('/') + '/api/' + route,
                                     data=None if body is None else json.dumps(body).encode(),
                                     method='GET' if body is None else 'POST',
                                     headers={'Authorization': 'Bearer ' + profile['token'],
                                              'X-Workspace-ID': profile['workspace_id'],
                                              'Content-Type': 'application/json'})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=30) as response:
        return json.load(response)


def maintain(env, reason):
    return control_api('delivery-queue/slots/configure', dict(name=env, action='maintain', reason=reason))


def resume(env, reason):
    return control_api('delivery-queue/slots/configure', dict(name=env, action='resume-maintenance', reason=reason))


def slot_seed(env):
    try:
        return control_api('delivery-queue/slots/seed?name=' + urllib.parse.quote(env))
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def record_slot_seed(env, seed_id, sha256, verified):
    return control_api('delivery-queue/slots/seed', dict(name=env, seed_id=seed_id, seed_sha256=sha256, verified=verified))


def seed_dir(seed):
    path = Path(seed)
    if not path.is_absolute():
        path = SEEDS / path
    if not (path / 'seed.json').exists():
        raise RuntimeError('种子目录缺少 seed.json: ' + str(path))
    return path


def read_seed(seed):
    path = seed_dir(seed)
    manifest = r.read(path / 'seed.json')
    artifact = manifest.get('artifact') or {'dump': 'seed.dump', 'sha256': manifest.get('dump_sha256')}
    if not artifact.get('sha256'):
        raise RuntimeError('种子缺少校验和: ' + str(path))
    if digest(path / artifact['dump']) != artifact['sha256']:
        raise RuntimeError('种子文件校验失败: ' + str(path))
    return path, manifest, artifact


def source_identity(manifest):
    source = manifest.get('source') or {}
    return dict(environment=source.get('environment') or manifest.get('source_environment', 'int'),
                commit=source.get('commit') or manifest.get('source_commit', ''),
                database=source.get('database') or manifest.get('database', ''),
                collected_at=source.get('collected_at') or manifest.get('collected_at', ''))


def row_counts(container, database):
    counts = {}
    for table in ('workspace', 'user', 'issue', 'comment', 'agent', 'project'):
        name = '"user"' if table == 'user' else table
        if table_exists(container, database, name.strip('"')):
            counts[table] = counter(container, database, 'SELECT count(*) FROM public.%s' % name)
    return counts


def data_checks(container, database, manifest):
    """恢复后的结构与样本检查：必需数据与结构指纹。外键在导入脚本中单独核对。"""
    checks = []
    required = manifest.get('fixture', {}).get('row_counts') or {}

    def add(name, passed, evidence):
        checks.append(dict(name=name, status='passed' if passed else 'failed', evidence=evidence))

    for table, minimum in (('workspace', 1), ('user', 1), ('issue', 1), ('comment', 1)):
        name = '"user"' if table == 'user' else table
        count = counter(container, database, 'SELECT count(*) FROM public.%s' % name)
        floor = max(minimum, int(required.get(table, 0)))
        add('必需样本 ' + table, count >= floor, '%d 行，期望至少 %d' % (count, floor))
    if table_exists(container, database, 'project'):
        count = counter(container, database, 'SELECT count(*) FROM public.project')
        add('必需样本 project', count >= int(required.get('project', 0)), '%d 行' % count)
    sequences = sql(container, database, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='S' AND n.nspname='public'")
    add('序列已重建', int(sequences) > 0, '%s 个序列' % sequences)
    identity = schema_identity(container, database)
    expected = (manifest.get('schema') or {}).get('fingerprint')
    add('数据库结构与种子一致', not expected or identity['fingerprint'] == expected,
        '当前 %s，种子 %s' % (identity['fingerprint'][:12], (expected or '未记录')[:12]))
    return checks


def scrub_checks(container, database):
    """清理结果检查：任何残留都说明规则没有覆盖新功能。"""
    checks = []
    rules = [
        ('个人访问令牌已清空', 'personal_access_token', 'SELECT count(*) FROM public.personal_access_token', 0),
        ('任务与 Daemon 令牌已清空', 'task_token', 'SELECT (SELECT count(*) FROM public.task_token)+(SELECT count(*) FROM public.daemon_token)', 0),
        ('执行队列无待执行任务', 'agent_task_queue', "SELECT count(*) FROM public.agent_task_queue WHERE status NOT IN ('completed','failed','cancelled')", 0),
        ('执行任务未绑定自动化', 'agent_task_queue', 'SELECT count(*) FROM public.agent_task_queue WHERE autopilot_run_id IS NOT NULL', 0),
        ('自动化无运行记录', 'autopilot_run', 'SELECT count(*) FROM public.autopilot_run', 0),
        ('自动化已暂停', 'autopilot', "SELECT count(*) FROM public.autopilot WHERE status<>'paused'", 0),
        ('自动化触发器已停用', 'autopilot_trigger', 'SELECT count(*) FROM public.autopilot_trigger WHERE enabled', 0),
        ('插件钩子已停用', 'plugin_hook_schedule', 'SELECT count(*) FROM public.plugin_hook_schedule WHERE enabled', 0),
        ('监督已关闭', 'supervision_config', 'SELECT count(*) FROM public.supervision_config WHERE enabled', 0),
        ('代码审查已关闭', 'review_config', 'SELECT count(*) FROM public.review_config WHERE enabled', 0),
        ('上游同步已暂停', 'upstream_sync_config', "SELECT count(*) FROM public.upstream_sync_config WHERE sync_state<>'paused'", 0),
        ('历史交付不再执行', 'issue_delivery', "SELECT count(*) FROM public.issue_delivery WHERE phase NOT IN ('needs_changes','published','cancelled')", 0),
        ('发布占用已清空', 'delivery_publication', 'SELECT count(*) FROM public.delivery_publication', 0),
        ('验收槽位占用未带入', 'verification_slot', 'SELECT count(*) FROM public.verification_slot', 0),
    ]
    for name, table, statement, expected in rules:
        if not table_exists(container, database, table):
            checks.append(dict(name=name, status='passed', evidence='表不存在，无需清理'))
            continue
        count = counter(container, database, statement)
        checks.append(dict(name=name, status='passed' if count == expected else 'failed', evidence='%d 处残留' % count))
    return checks


def api_checks(env):
    """账号与核心业务读路径：登录、工作区、任务列表和 Agent 列表。"""
    meta = r.read(r.meta_path(env))
    record = r.read(r.RUN/'verification-install'/(env + '.json'))
    checks = []

    def call(url, body=None, token=None):
        request = urllib.request.Request(url, data=None if body is None else json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json', **({'Authorization': 'Bearer ' + token} if token else {})})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=30) as response:
            return json.load(response)

    login = call(meta['backend_url'] + '/auth/login', dict(account=record['account'], password=record['password']))
    checks.append(dict(name='槽位账号可登录', status='passed' if login.get('token') else 'failed', evidence='账号 ' + record['account']))
    workspaces = call(meta['backend_url'] + '/api/workspaces', token=login['token'])
    workspace = next((w for w in workspaces if w['id'] == r.read(meta['daemon_profile_path'])['workspace_id']), None)
    checks.append(dict(name='工作区身份可用', status='passed' if workspace else 'failed', evidence=(workspace or {}).get('slug', '缺少槽位工作区')))
    workspace_id = (workspace or {}).get('id', '')
    issues = call(meta['backend_url'] + '/api/issues?workspace_id=' + workspace_id, token=login['token'])
    checks.append(dict(name='任务列表可读取', status='passed' if isinstance(issues, list) and issues else 'failed', evidence='%d 个任务' % (len(issues) if isinstance(issues, list) else 0)))
    agents = call(meta['backend_url'] + '/api/agents?workspace_id=' + workspace_id, token=login['token'])
    checks.append(dict(name='Agent 列表可读取', status='passed' if isinstance(agents, list) and agents else 'failed', evidence='%d 个 Agent' % (len(agents) if isinstance(agents, list) else 0)))
    return checks


def write_manifest(path, manifest):
    r.write(path / 'seed.json', manifest, True)


def restore(env, seed, pgconfig):
    """把种子装进专属槽位；调用方必须先让槽位进入维护态。"""
    if env not in r.VERIFICATION_ENVS or os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('仅外部维护身份可重置固定验收环境')
    path, manifest, artifact = read_seed(seed)
    container, port, _ = postgres(pgconfig)
    with r.lock(r.candidate_lock(env), wait=True):
        meta = r.read(r.meta_path(env)); cfg = r.read(r.config_path(env))
        r.validate_verification_config(env, cfg)
        target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
        dbname = 'multica_' + env.replace('-', '_'); role = env.replace('-', '_')
        if target.hostname != '127.0.0.1' or str(target.port) != port or target.path != '/' + dbname or target.username != role:
            raise RuntimeError('槽位实际连接与专属实例不一致')
        if r.candidate_path(env).exists() and r.candidate(env=env)['status'] not in ('released', 'failed', 'succeeded'):
            raise RuntimeError('槽位存在未完成候选')
        record_path = r.RUN/'verification-install'/(env + '.json')
        record = r.read(record_path)
        release = Path(meta['release_dir'])
        if record.get('stage') == 'resetting':
            if any(r.exists(pid) for pid in meta['pids'].values()):
                raise RuntimeError('重置中断后仍有运行进程，需要核对现场')
        else:
            r.check_process_ownership(env, meta)
            r.check_live_environment(env, meta, cfg)
            r.drain(env, meta)
            r.http(r.health_url(env) + '/shutdown', 'POST')
            deadline = time.monotonic() + 30
            while r.exists(meta['pids']['daemon']) and time.monotonic() < deadline:
                time.sleep(.2)
            if r.exists(meta['pids']['daemon']):
                raise RuntimeError('Daemon 未退出')
            for service in ('web', 'server'):
                r.owned_stop(meta['pids'][service], service, release/'apps/web' if service == 'web' else release)
        # 不使用 FORCE；出现未识别连接时保留现场并停止。
        if sql(container, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='" + dbname + "'") != '0':
            raise RuntimeError('验收数据库仍有消费者，拒绝重置')
        record.update(stage='resetting', seed=str(path), seed_id=manifest.get('seed_id') or path.name); r.write(record_path, record, True)
        sql(container, 'postgres', f'DROP DATABASE {dbname};\nCREATE DATABASE {dbname} OWNER {role};\nREVOKE CONNECT ON DATABASE {dbname} FROM PUBLIC;\nGRANT CONNECT ON DATABASE {dbname} TO {role};')
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server', env={**cfg['server'], 'MULTICA_MIGRATION_BASELINE':'true'})
        staging = 'verification_seed_import_' + secrets.token_hex(8)
        record['staging_database'] = staging; r.write(record_path, record, True)
        sql(container, 'postgres', f'CREATE DATABASE {staging};')
        try:
            with (path/artifact['dump']).open('rb') as stream:
                subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', staging, '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
            r.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), env, staging, str(pgconfig)], cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})
        finally:
            sql(container, 'postgres', f'DROP DATABASE {staging};')
        # 种子可能来自另一槽位；登录凭据属于槽位安装记录，重置后重新绑定到本槽位密码。
        hashed = password_hash(record['password']).replace("'", "''")
        sql(container, dbname, "UPDATE \"user\" SET password_hash='%s' WHERE account='%s';" % (hashed, record['account'].replace("'", "''")))
        pid = r.start([release/'server/bin/server'], release, cfg['server'], meta['log_paths']['server'])
        try:
            r.ready(meta['backend_url'] + '/health', pid=pid)
            login = i.api(meta['backend_url'], '/auth/login', dict(account=record['account'], password=record['password']))
            me = i.api(meta['backend_url'], '/api/me/onboarding/complete', dict(completion_path='skip_existing'), login['token'])
            user = me['id']
            sql(container, dbname, f"INSERT INTO member(workspace_id,user_id,role) SELECT id,'{user}','owner' FROM workspace WHERE NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=workspace.id AND m.user_id='{user}'); UPDATE agent SET owner_id='{user}';")
            request = urllib.request.Request(meta['backend_url'] + '/api/workspaces', headers={'Authorization': 'Bearer ' + login['token']})
            with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
                workspaces = json.load(response)
            ws = next((w for w in workspaces if w['slug'] == 'ai-studio'), None) or (workspaces[0] if workspaces else i.api(meta['backend_url'], '/api/workspaces', dict(name='任务验收', slug='verification'), login['token']))
            profile_path = Path(meta['daemon_profile_path']); profile = r.read(profile_path)
            profile.update(token=login['token'], workspace_id=ws['id']); r.write(profile_path, profile, True)
            account = i.pwd.getpwnam(cfg['daemon']['MULTICA_SERVICE_USER']); os.chown(profile_path, account.pw_uid, account.pw_gid)
        finally:
            r.owned_stop(pid, 'server', release)
        meta['pids'] = r.launch(env, release, cfg, meta); r.write(r.meta_path(env), meta, True)
        i.verify_installed(env)
    return dict(environment=env, seed_id=manifest.get('seed_id') or path.name, container=container, database=dbname)


def reset(env, seed, pgconfig):
    """单槽位维护：停接单、恢复种子、恢复接单；失败保持停用，不影响其他槽位。"""
    path, manifest, artifact = read_seed(seed)
    verified = bool((manifest.get('restore_verification') or {}).get('passed', True))
    maintain(env, '按种子恢复测试数据')
    try:
        result = restore(env, seed, pgconfig)
    except Exception as error:
        try:
            record_slot_seed(env, manifest.get('seed_id') or path.name, artifact['sha256'], False)
        except Exception:
            pass
        raise RuntimeError('种子恢复失败，槽位保持维护：' + str(error)) from error
    record_slot_seed(env, result['seed_id'], artifact['sha256'], verified)
    resume(env, '种子恢复完成')
    return result


def ensure(env, pgconfig):
    """1. 换任务前核对槽位种子；与当前版本不一致时先恢复，成功后才允许领取新候选。"""
    if not current_path().exists():
        return dict(status='skipped', reason='未配置当前种子，保持原有行为')
    current = r.read(current_path())
    path, manifest, artifact = read_seed(SEEDS / current['seed_id'])
    slot = slot_seed(env)
    if slot and slot.get('seed_id') == current['seed_id'] and slot.get('seed_sha256') == current['dump_sha256'] and slot.get('seed_verified'):
        return dict(status='current', environment=env, seed=current['seed_id'])
    reset(env, path, pgconfig)
    return dict(status='restored', environment=env, seed=current['seed_id'])


def build_seed(seed_id, origin, release, pgconfig):
    """2. 在隔离构建库按目标程序结构重建种子，并记录结构、样本和清理规则身份。"""
    if not re.fullmatch(r'[a-z0-9][a-z0-9._-]{2,60}', seed_id or ''):
        raise RuntimeError('种子编号只允许小写字母、数字、点和横线')
    destination = SEEDS / seed_id
    if destination.exists():
        raise RuntimeError('种子编号已存在；种子发布后不可原地覆盖')
    path, manifest, artifact = read_seed(origin)
    container, port, password = postgres(pgconfig)
    release = Path(release).resolve()
    if not (release/'server/bin/migrate').exists():
        raise RuntimeError('构建种子需要目标候选的迁移程序')
    target = BUILD_PREFIX + re.sub(r'[^a-z0-9_]', '_', seed_id.lower())
    staging = 'verification_seed_import_' + secrets.token_hex(8)
    dsn = 'postgres://postgres:%s@127.0.0.1:%s/%s' % (urllib.parse.quote(password, safe=''), port, target)
    sql(container, 'postgres', f'DROP DATABASE IF EXISTS {target};\nCREATE DATABASE {target};')
    sql(container, 'postgres', f'CREATE DATABASE {staging};')
    try:
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server', env={**os.environ, 'DATABASE_URL': dsn, 'MULTICA_MIGRATION_BASELINE': 'true'})
        with (path/artifact['dump']).open('rb') as stream:
            subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', staging, '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
        copied = subprocess.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), 'build', staging, str(pgconfig), '--target-database', target, '127.0.0.1', port],
                                cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT': str(r.ROOT)}, capture_output=True, text=True)
        if copied.returncode:
            raise RuntimeError('种子导入失败: ' + (copied.stderr or copied.stdout).strip()[:1000])
        copy_report = json.loads(copied.stdout.strip().splitlines()[-1])
        schema = schema_identity(container, target)
        counts = row_counts(container, target)
        checks = data_checks(container, target, dict(manifest, fixture=dict(manifest.get('fixture', {}), row_counts=counts)))
        checks += scrub_checks(container, target)
        failed = [c for c in checks if c['status'] != 'passed']
        if failed:
            raise RuntimeError('种子构建检查失败: ' + ', '.join(c['name'] for c in failed))
        destination.mkdir(parents=True, mode=0o700)
        dump_path = destination/'seed.dump'
        with dump_path.open('xb') as stream:
            dump_path.chmod(0o600)
            subprocess.run(['docker', 'exec', container, 'pg_dump', '-U', 'postgres', '-d', target, '-Fc', '--no-owner', '--no-acl'], stdout=stream, check=True)
        record = dict(seed_id=seed_id, version=2, status='built', source=source_identity(manifest),
                      artifact=dict(dump='seed.dump', sha256=digest(dump_path), bytes=dump_path.stat().st_size),
                      schema=schema, fixture=dict(version=FIXTURE_VERSION, row_counts=counts),
                      scrub=scrub_identity(), build=dict(environment='isolated-build', at=now(), release=str(release), checks=checks, tables=copy_report.get('tables', [])))
        write_manifest(destination, record)
    finally:
        sql(container, 'postgres', f'DROP DATABASE IF EXISTS {staging};\nDROP DATABASE IF EXISTS {target};')
    print(json.dumps(dict(status='built', seed=str(destination), schema=schema, rows=counts), ensure_ascii=False))
    return destination


def verify_seed(env, seed, pgconfig, accept=False):
    """3. 恢复并验收当前种子；失败不替换当前版本，槽位保持维护。"""
    path, manifest, artifact = read_seed(seed)
    seed_id = manifest.get('seed_id') or path.name
    maintain(env, '种子恢复验收')
    try:
        result = restore(env, seed, pgconfig)
        container, database = result['container'], result['database']
        checks = api_checks(env)
        checks += data_checks(container, database, manifest)
        checks += scrub_checks(container, database)
        failed = [c for c in checks if c['status'] != 'passed']
        record = dict(manifest, seed_id=seed_id, version=2, artifact=artifact, source=source_identity(manifest))
        record['restore_verification'] = dict(environment=env, at=now(), passed=not failed, checks=checks)
        if not failed:
            record['status'] = 'verified'
        write_manifest(path, record)
        record_slot_seed(env, seed_id, artifact['sha256'], not failed)
    except Exception as error:
        raise RuntimeError('种子恢复验收失败，槽位保持维护：' + str(error)) from error
    if failed:
        raise RuntimeError('种子恢复验收未通过，槽位保持维护：' + ', '.join(c['name'] for c in failed))
    resume(env, '种子恢复验收完成')
    if accept:
        r.write(current_path(), dict(seed_id=seed_id, dump_sha256=artifact['sha256'], schema=(manifest.get('schema') or {}).get('fingerprint', ''),
                              accepted_at=now(), verified_by=env), True)
    print(json.dumps(dict(status='verified', seed=seed_id, environment=env, current=bool(accept), checks=checks), ensure_ascii=False))


def status():
    current = r.read(current_path()) if current_path().exists() else None
    seeds = []
    for item in sorted(SEEDS.glob('*/seed.json')):
        try:
            manifest = r.read(item)
        except Exception:
            continue
        seeds.append(dict(seed_id=manifest.get('seed_id') or item.parent.name,
                          status=manifest.get('status', 'legacy'),
                          build=manifest.get('build', {}).get('at', manifest.get('collected_at', '')),
                          restore=(manifest.get('restore_verification') or {}).get('environment', ''),
                          verified=(manifest.get('restore_verification') or {}).get('passed')))
    slots = {}
    for env in r.VERIFICATION_ENVS:
        try:
            slots[env] = slot_seed(env)
        except Exception as error:
            slots[env] = dict(error=str(error))
    print(json.dumps(dict(current=current, seeds=seeds, slots=slots), ensure_ascii=False, indent=2))


def export_seed(destination):
    if destination.exists():
        raise RuntimeError('种子目录已存在，拒绝覆盖')
    meta = r.read(r.meta_path('int'))
    cfg = r.read(r.config_path('int'))
    r.check_live_environment('int', meta, cfg)
    target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
    production = urllib.parse.urlparse(r.read(r.config_path('prod'))['server']['DATABASE_URL'])
    if (target.hostname, target.port, target.path) == (production.hostname, production.port, production.path):
        raise RuntimeError('联调连接与生产重叠，拒绝导出')
    matches = []
    containers = r.output(['docker', 'ps', '-q']).split()
    if not containers:
        raise RuntimeError('未发现数据库实例')
    for item in json.loads(r.output(['docker', 'inspect', *containers])):
        for binding in (item['NetworkSettings']['Ports'].get('5432/tcp') or []):
            if target.hostname in ('127.0.0.1', 'localhost') and binding['HostIp'] == '127.0.0.1' and binding['HostPort'] == str(target.port):
                matches.append(item['Name'].lstrip('/'))
    if len(matches) != 1:
        raise RuntimeError('无法唯一定位联调实际使用的 PostgreSQL 实例')
    destination.mkdir(parents=True, mode=0o700)
    dump = destination/'seed.dump'
    # 完整导出保持既有数据关联；验收副本启动前按声明式清单清理执行态和凭据。
    args = ['docker', 'exec', matches[0], 'pg_dump', '-U', urllib.parse.unquote(target.username), '-d', urllib.parse.unquote(target.path[1:]), '-Fc', '--no-owner', '--no-acl']
    with dump.open('xb') as stream:
        dump.chmod(0o600)
        subprocess.run(args, stdout=stream, check=True)
    write_manifest(destination, dict(seed_id=destination.name, version=2, status='exported',
                                     source=dict(environment='int', commit=meta['commit'], database=target.path[1:], collected_at=now()),
                                     artifact=dict(dump='seed.dump', sha256=digest(dump), bytes=dump.stat().st_size),
                                     scrub=scrub_identity()))
    print(json.dumps(dict(status='exported', environment='int', seed=str(destination))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest='action', required=True)
    export = sub.add_parser('export'); export.add_argument('--destination', type=Path, required=True)
    reset_command = sub.add_parser('reset'); reset_command.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True); reset_command.add_argument('--seed', type=Path, required=True); reset_command.add_argument('--postgres-config', type=Path, required=True)
    build = sub.add_parser('build'); build.add_argument('--id', required=True); build.add_argument('--from', dest='origin', type=Path, required=True); build.add_argument('--release', type=Path, required=True); build.add_argument('--postgres-config', type=Path, required=True)
    verify = sub.add_parser('verify'); verify.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True); verify.add_argument('--seed', type=Path, required=True); verify.add_argument('--postgres-config', type=Path, required=True); verify.add_argument('--accept', action='store_true')
    ensure_command = sub.add_parser('ensure'); ensure_command.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True); ensure_command.add_argument('--postgres-config', type=Path, required=True)
    status_command = sub.add_parser('status')
    for item in (verify, reset_command, ensure_command, status_command):
        item.add_argument('--control', choices=('prod', 'int', *r.VERIFICATION_ENVS), default='prod', help='承载交付队列接口的控制端；演练时指向隔离环境')
    args = parser.parse_args()
    CONTROL_ENVIRONMENT = args.control
    if args.action == 'export':
        export_seed(args.destination.resolve())
    elif args.action == 'reset':
        print(json.dumps(reset(args.environment, args.seed.resolve(), args.postgres_config.resolve()), ensure_ascii=False))
    elif args.action == 'ensure':
        print(json.dumps(ensure(args.environment, args.postgres_config.resolve()), ensure_ascii=False))
    elif args.action == 'build':
        build_seed(args.id, args.origin.resolve(), args.release.resolve(), args.postgres_config.resolve())
    elif args.action == 'verify':
        verify_seed(args.environment, args.seed.resolve(), args.postgres_config.resolve(), args.accept)
    else:
        status()
