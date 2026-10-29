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
import shutil
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
FIXTURES = Path(__file__).with_name('verification-fixtures.sql')
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


def begin_maintenance(env, reason):
    slots = control_api('delivery-queue/slots')
    matches = [slot for slot in slots if slot.get('name') == env]
    if len(matches) != 1 or not isinstance(matches[0].get('enabled'), bool):
        raise RuntimeError('无法核对验收槽位原有启用状态')
    previous = matches[0]['enabled']
    result = maintain(env, reason)
    # 旧控制端没有返回原状态；新控制端在行锁内返回，避免读取与维护之间的竞态。
    return result.get('previous_enabled', previous)


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


def read_sql_seed(path, manifest):
    """读取已清理 SQL 的不可变身份；未验收前不影响快照恢复入口。"""
    artifact = manifest.get('sql_artifact') or {}
    if artifact.get('file') != 'seed.sql' or not artifact.get('sha256'):
        raise RuntimeError('种子缺少 SQL 文件或校验和')
    sql_path = Path(path) / 'seed.sql'
    if not sql_path.is_file() or digest(sql_path) != artifact['sha256']:
        raise RuntimeError('SQL 种子文件校验失败: ' + str(path))
    return sql_path


def verified_sql_seed(path, manifest):
    verification = manifest.get('sql_verification') or {}
    if verification.get('passed') is not True:
        return None
    if verification.get('sha256') != (manifest.get('sql_artifact') or {}).get('sha256'):
        raise RuntimeError('SQL 种子已变化，需要重新验收')
    return read_sql_seed(path, manifest)


def import_sql_seed(container, database, sql_path):
    # 导入阶段不产生业务事件；COPY 或 INSERT 出错时整次事务回滚。
    content = Path(sql_path).read_bytes()
    subprocess.run(['docker', 'exec', '-i', container, 'psql', '-q', '-U', 'postgres', '-d', database,
                    '-v', 'ON_ERROR_STOP=1'], input=b'BEGIN;\nSET LOCAL session_replication_role=replica;\n'
                   + content + b'\nSET LOCAL session_replication_role=origin;\nCOMMIT;\n', check=True)


def clear_verification_uploads(env, cfg, record):
    """只清除该槽位安装记录对应的上传目录。"""
    if env not in r.VERIFICATION_ENVS or record.get('environment') != env:
        raise RuntimeError('验收上传目录身份不匹配')
    root = Path(record['data_root'])
    app = root / 'app'
    expected = app / 'uploads'
    target = Path(cfg['server']['LOCAL_UPLOAD_DIR'])
    if not root.is_absolute() or root.name != env or root.resolve() != root or any(path.is_symlink() for path in (root, app, expected)) or target != expected or target.resolve() != target:
        raise RuntimeError('验收上传目录与安装记录不一致')
    owner = i.pwd.getpwnam(cfg['server']['MULTICA_SERVICE_USER'])
    try:
        directory = os.open(target, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError as error:
        raise RuntimeError('验收上传目录不存在或归属不符') from error
    try:
        if os.fstat(directory).st_uid != owner.pw_uid:
            raise RuntimeError('验收上传目录不存在或归属不符')
        with os.scandir(directory) as entries:
            for item in entries:
                if item.is_dir(follow_symlinks=False):
                    shutil.rmtree(item.name, dir_fd=directory)
                else:
                    os.unlink(item.name, dir_fd=directory)
    finally:
        os.close(directory)


def clear_verification_database(env, cfg, pgconfig):
    """Clear only a dedicated verification database while retaining its migration ledger."""
    if env not in r.VERIFICATION_ENVS or os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('仅外部维护身份可清理固定验收环境')
    container, port, _ = postgres(pgconfig)
    target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
    database = 'multica_' + env.replace('-', '_')
    if target.hostname != '127.0.0.1' or str(target.port) != port or target.path != '/' + database or target.username != env.replace('-', '_'):
        raise RuntimeError('槽位实际连接与专属实例不一致')
    if counter(container, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='%s'" % database):
        raise RuntimeError('验收数据库仍有消费者，拒绝清理')
    statement = '''DO $$ DECLARE targets text; BEGIN
      IF EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class parent ON parent.oid=c.confrelid JOIN pg_namespace pn ON pn.oid=parent.relnamespace JOIN pg_class child ON child.oid=c.conrelid JOIN pg_namespace cn ON cn.oid=child.relnamespace WHERE c.contype='f' AND pn.nspname='public' AND parent.relname<>'schema_migrations' AND (cn.nspname<>'public' OR child.relname='schema_migrations')) THEN
        RAISE EXCEPTION '验收表被清理范围外的表引用';
      END IF;
      SELECT string_agg(format('%I.%I', n.nspname, c.relname), ', ' ORDER BY c.relname) INTO targets FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname<>'schema_migrations';
      IF targets IS NULL THEN RAISE EXCEPTION '验收数据库没有业务表'; END IF;
      EXECUTE 'TRUNCATE TABLE ' || targets || ' RESTART IDENTITY';
    END $$;'''
    sql(container, database, statement)
    return database


def clear_slot(env, lease, pgconfig, fence):
    """Stop and clear one released task's isolated environment; resume from its journal."""
    if env not in r.VERIFICATION_ENVS or os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('仅外部维护身份可清理固定验收环境')
    with r.lock(r.candidate_lock(env), wait=True):
        cfg = r.read(r.config_path(env)); meta = r.read(r.meta_path(env))
        r.validate_verification_config(env, cfg)
        record_path = r.RUN / 'verification-install' / (env + '.json')
        record = r.read(record_path)
        if record.get('environment') != env or lease.get('name') != env or not lease.get('candidate_id'):
            raise RuntimeError('清理目标或占用身份不符')
        candidate = r.candidate(env=env)
        if candidate.get('delivery_id') != lease.get('delivery_id') or candidate.get('id') != lease.get('candidate_id'):
            raise RuntimeError('清理候选与槽位不一致')
        if record.get('stage') in ('cleaning','cleaned'):
            if record.get('cleanup_delivery') != lease['delivery_id'] or record.get('cleanup_generation') != lease['generation'] or record.get('cleanup_candidate') != lease['candidate_id']:
                raise RuntimeError('未完成的清理属于其他槽位代次')
            if record['stage'] == 'cleaned': return
            r.check_process_ownership(env, meta, allow_unhealthy=True)
        else:
            r.check_process_ownership(env, meta)
            r.check_live_environment(env, meta, cfg)
            fence()
            record.update(stage='cleaning',cleanup_delivery=lease['delivery_id'],cleanup_generation=lease['generation'],cleanup_candidate=lease['candidate_id'])
            r.write(record_path, record, True)
        fence()
        daemon = meta.get('pids', {}).get('daemon')
        if daemon and r.exists(daemon):
            r.http(r.health_url(env) + '/shutdown', 'POST')
            deadline=time.monotonic()+30
            while r.exists(daemon) and time.monotonic()<deadline: time.sleep(.2)
            if r.exists(daemon): raise RuntimeError('验收 Daemon 未退出')
        for role in ('web','server'):
            pid=meta.get('pids',{}).get(role)
            if pid and r.exists(pid):
                release=Path(meta.get('role_release_dirs',{}).get(role,meta['release_dir']))
                r.owned_stop(pid,role,release/'apps/web' if role=='web' else release)
        meta['pids']={};r.write(r.meta_path(env),meta,True)
        fence()
        clear_verification_uploads(env,cfg,record)
        clear_verification_database(env,cfg,pgconfig)
        record['stage']='cleaned';r.write(record_path,record,True)


def adopt_cleaning(env, previous, current):
    path=r.RUN/'verification-install'/(env+'.json')
    if not path.exists(): return
    record=r.read(path)
    if record.get('stage') not in ('cleaning','cleaned'): return
    if record.get('cleanup_delivery') != previous.get('delivery_id') or record.get('cleanup_generation') != previous.get('generation') or record.get('cleanup_candidate') != previous.get('candidate_id') or current.get('delivery_id') != previous.get('delivery_id') or current.get('generation',0) <= previous.get('generation',0):
        raise RuntimeError('无法确认验收清理恢复代次')
    record['cleanup_generation']=current['generation'];r.write(path,record,True)


def verify_sql_seed(seed, release, pgconfig):
    """在专属数据库中比较 SQL 与已清理快照，不修改当前种子指针。"""
    path, manifest, artifact = read_seed(seed)
    sql_path = read_sql_seed(path, manifest)
    release = Path(release).resolve()
    validate_seed_release(manifest, release)
    container, port, password = postgres(pgconfig)
    suffix = secrets.token_hex(8)
    snapshot_db, sql_db = BUILD_PREFIX + 'snapshot_' + suffix, BUILD_PREFIX + 'sql_' + suffix
    dsn = 'postgres://postgres:%s@127.0.0.1:%s/%s' % (urllib.parse.quote(password, safe=''), port, sql_db)
    created = []
    try:
        for database in (snapshot_db, sql_db):
            sql(container, 'postgres', 'CREATE DATABASE ' + database)
            created.append(database)
        with (path / artifact['dump']).open('rb') as stream:
            subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', snapshot_db,
                            '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server',
              env={**os.environ, 'DATABASE_URL': dsn, 'MULTICA_MIGRATION_BASELINE': 'true'})
        import_sql_seed(container, sql_db, sql_path)
        checks = check_restored_data(container, sql_db, manifest)
        listing = "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY tablename"
        tables = sql(container, snapshot_db, listing).splitlines()
        if tables != sql(container, sql_db, listing).splitlines():
            raise RuntimeError('SQL 种子与快照的表集合不一致')
        queries = []
        for table in tables:
            quoted = '"' + table.replace('"', '""') + '"'
            literal = "'" + table.replace("'", "''") + "'"
            queries.append("SELECT json_build_array(" + literal + ",count(*),md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY md5(row_to_json(t)::text)),'')))::text FROM public." + quoted + ' t')
        query = ' UNION ALL '.join(queries)
        expected = sorted((json.loads(line) for line in sql(container, snapshot_db, query).splitlines()), key=lambda item: item[0])
        actual = sorted((json.loads(line) for line in sql(container, sql_db, query).splitlines()), key=lambda item: item[0])
        if expected != actual:
            differences = [a[0] for a, b in zip(expected, actual) if a != b]
            raise RuntimeError('SQL 种子与快照数据不一致: ' + ', '.join(differences))
        rows = sum(item[1] for item in actual)
        sequences = "SELECT sequencename || ':' || COALESCE(last_value::text,'NULL') FROM pg_sequences WHERE schemaname='public' ORDER BY sequencename"
        if sql(container, snapshot_db, sequences) != sql(container, sql_db, sequences):
            raise RuntimeError('SQL 种子与快照的序列值不一致')
        manifest['sql_verification'] = dict(passed=True, at=now(), sha256=manifest['sql_artifact']['sha256'],
                                            tables=len(tables), rows=rows, checks=checks)
        write_manifest(path, manifest)
        print(json.dumps(dict(status='verified', seed=str(path), tables=len(tables), rows=rows), ensure_ascii=False))
    finally:
        for database in created:
            sql(container, 'postgres', 'DROP DATABASE ' + database)


def source_identity(manifest):
    source = manifest.get('source') or {}
    identity = dict(environment=source.get('environment') or manifest.get('source_environment', 'int'),
                    commit=source.get('commit') or manifest.get('source_commit', ''),
                    database=source.get('database') or manifest.get('database', ''),
                    collected_at=source.get('collected_at') or manifest.get('collected_at', ''))
    if source.get('fixture_sql_sha256'):
        identity['fixture_sql_sha256'] = source['fixture_sql_sha256']
    return identity


def row_counts(container, database):
    counts = {}
    for table in ('workspace', 'user', 'issue', 'comment', 'agent', 'project'):
        name = '"user"' if table == 'user' else table
        if table_exists(container, database, name.strip('"')):
            counts[table] = counter(container, database, 'SELECT count(*) FROM public.%s' % name)
    return counts


def release_schema_identity(release):
    """按制品清单核对迁移内容，恢复前不依赖目标数据库的可变状态。"""
    release = Path(release)
    manifest = r.read(release / 'release.json')
    files = manifest.get('files') or {}
    migrations = {name: value for name, value in files.items()
                  if name.startswith('server/migrations/')}
    if not migrations or 'server/bin/migrate' not in files:
        raise RuntimeError('候选制品缺少迁移身份，拒绝恢复')
    actual = {str(path.relative_to(release)): digest(path)
              for path in (release / 'server/migrations').rglob('*') if path.is_file()}
    if actual != migrations or digest(release / 'server/bin/migrate') != files['server/bin/migrate']:
        raise RuntimeError('候选迁移程序或迁移文件校验失败')
    return hashlib.sha256(json.dumps(migrations, sort_keys=True).encode()).hexdigest()


def validate_seed_release(manifest, release):
    expected = (manifest.get('schema') or {}).get('migration_files_sha256')
    if not expected:
        # 旧版已构建种子可从其不可变制品恢复身份；原始导出需先按候选构建。
        built = (manifest.get('build') or {}).get('release')
        if not built or not Path(built).exists():
            raise RuntimeError('种子缺少候选迁移身份，请先按目标候选重建种子')
        expected = release_schema_identity(built)
    if release_schema_identity(release) != expected:
        raise RuntimeError('候选结构与种子不兼容，请先按目标候选重建并验收种子')
    if not (manifest.get('schema') or {}).get('fingerprint'):
        raise RuntimeError('种子缺少数据库结构指纹，拒绝恢复')


def check_restored_data(container, database, manifest):
    checks = data_checks(container, database, manifest) + scrub_checks(container, database)
    failed = [check['name'] for check in checks if check['status'] != 'passed']
    if failed:
        raise RuntimeError('种子恢复检查失败: ' + ', '.join(failed))
    return checks


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
        ('测试 Agent 显式使用受支持模型', 'agent', "SELECT count(*) FROM public.agent WHERE NOT (COALESCE(model,'')='deepseek-v4.1-flash' AND COALESCE(thinking_level,'')='' AND COALESCE(service_tier,'')='' OR COALESCE(model,'')='gpt-6.1-sol' AND thinking_level='low' AND service_tier='default')", 0),
        ('测试模型无命令行覆盖', 'agent', "SELECT count(*) FROM public.agent WHERE EXISTS (SELECT 1 FROM jsonb_array_elements_text(COALESCE(custom_args,'[]'::jsonb)) a(value) WHERE value IN ('-m','--model','-c','--config') OR value LIKE '--model=%' OR value LIKE '--config=%')", 0),
        ('MCP 检查已清空', 'workspace_mcp_check', 'SELECT count(*) FROM public.workspace_mcp_check', 0),
        ('MCP OAuth 状态已清空', 'plugin_remote_mcp_oauth_state', 'SELECT count(*) FROM public.plugin_remote_mcp_oauth_state', 0),
        ('旧插件配置已清空', 'plugin_installation_config', 'SELECT count(*) FROM public.plugin_installation_config', 0),
        ('旧插件 MCP 凭据已清空', 'plugin_remote_mcp_secret', 'SELECT count(*) FROM public.plugin_remote_mcp_secret', 0),
        ('MCP 绑定已清空', 'agent_mcp_server', 'SELECT count(*) FROM public.agent_mcp_server', 0),
        ('MCP 凭据配置已清空', 'workspace_mcp_server', 'SELECT count(*) FROM public.workspace_mcp_server', 0),
        ('个人访问令牌已清空', 'personal_access_token', 'SELECT count(*) FROM public.personal_access_token', 0),
        ('任务与 Daemon 令牌已清空', 'task_token', 'SELECT (SELECT count(*) FROM public.task_token)+(SELECT count(*) FROM public.daemon_token)', 0),
        ('执行队列无待执行任务', 'agent_task_queue', "SELECT count(*) FROM public.agent_task_queue WHERE status NOT IN ('completed','failed','cancelled')", 0),
        ('执行任务未绑定自动化', 'agent_task_queue', 'SELECT count(*) FROM public.agent_task_queue WHERE autopilot_run_id IS NOT NULL', 0),
        ('自动化无运行记录', 'autopilot_run', 'SELECT count(*) FROM public.autopilot_run', 0),
        ('自动化无额度预约', 'autopilot_quota_reservation', 'SELECT count(*) FROM public.autopilot_quota_reservation', 0),
        ('自动化已暂停', 'autopilot', "SELECT count(*) FROM public.autopilot WHERE status<>'paused'", 0),
        ('自动化触发器已停用', 'autopilot_trigger', 'SELECT count(*) FROM public.autopilot_trigger WHERE enabled', 0),
        ('插件钩子已停用', 'plugin_hook_schedule', 'SELECT count(*) FROM public.plugin_hook_schedule WHERE enabled', 0),
        ('监督已关闭', 'supervision_config', 'SELECT count(*) FROM public.supervision_config WHERE enabled', 0),
        ('代码审查已关闭', 'review_config', 'SELECT count(*) FROM public.review_config WHERE enabled', 0),
        ('发布协调已关闭', 'release_agent_config', 'SELECT count(*) FROM public.release_agent_config WHERE enabled', 0),
        ('上游同步已暂停', 'upstream_sync_config', "SELECT count(*) FROM public.upstream_sync_config WHERE sync_state<>'paused'", 0),
        ('历史交付不再执行', 'issue_delivery', "SELECT count(*) FROM public.issue_delivery WHERE phase NOT IN ('needs_changes','published','cancelled')", 0),
        ('发布占用已清空', 'delivery_publication', 'SELECT count(*) FROM public.delivery_publication', 0),
        ('验收槽位占用未带入', 'verification_slot', 'SELECT count(*) FROM public.verification_slot', 0),
        ('发布批次未带入', 'delivery_batch', 'SELECT count(*) FROM public.delivery_batch', 0),
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
    def total(payload):
        if isinstance(payload, list):
            return len(payload)
        if isinstance(payload, dict):
            return len(payload.get('issues') or payload.get('agents') or [])
        return 0

    # 2. 列表接口按工作区分页返回，业务样本不一定在 Daemon 工作区；逐个工作区核对实际可读到的数据量。
    issue_total = 0
    agent_total = 0
    witness = ''
    for item in workspaces:
        issues = total(call(meta['backend_url'] + '/api/issues?workspace_id=' + item['id'], token=login['token']))
        agents = total(call(meta['backend_url'] + '/api/agents?workspace_id=' + item['id'], token=login['token']))
        if issues and agents and not witness:
            witness = item['slug']
        issue_total = max(issue_total, issues)
        agent_total = max(agent_total, agents)
    checks.append(dict(name='任务列表可读取', status='passed' if issue_total else 'failed', evidence='最多 %d 个任务（工作区 %s）' % (issue_total, witness or '空')))
    checks.append(dict(name='Agent 列表可读取', status='passed' if agent_total else 'failed', evidence='最多 %d 个 Agent（工作区 %s）' % (agent_total, witness or '空')))
    return checks


def write_manifest(path, manifest):
    r.write(path / 'seed.json', manifest, True)


def restore_release(manifest, current):
    # 当前制品损坏不能借归位基线掩盖；只有合法的不同迁移集合才选择已验证种子的完整制品。
    release_schema_identity(current)
    try:
        validate_seed_release(manifest,current)
        return Path(current)
    except RuntimeError:
        baseline=(manifest.get('build') or {}).get('release')
        if not baseline:raise
        baseline=Path(baseline).resolve()
        package=r.read(baseline/'release.json')
        if r.checksums(baseline)!=package.get('files'):
            raise RuntimeError('种子基线完整制品校验失败，拒绝恢复')
        for key in ('id','source_commit','source_digest'):
            if not package.get(key):raise RuntimeError('种子基线制品身份不完整')
        validate_seed_release(manifest,baseline)
        return baseline


def candidate_worker_running(record):
    if record.get('publisher_pid') and r.exists(record['publisher_pid']):
        return True
    delivery = record.get('delivery_id')
    for folder in Path('/proc').iterdir():
        if not folder.name.isdigit():
            continue
        try:
            args = (folder/'cmdline').read_bytes().split(b'\0')
            position = args.index(b'--pool-delivery')
            if args[position + 1].decode() == delivery:
                return True
        except (OSError, ValueError, IndexError):
            continue
    return False


def retire_failed_candidate(env, meta):
    # 租约已收回的部署前失败候选需归档，否则正常种子恢复会永久被本地指针阻塞。
    if not r.candidate_path(env).exists():
        return
    record = r.candidate(env=env)
    if record['status'] in ('released', 'failed', 'succeeded'):
        return
    if (record['status'] != 'recovery_required' or not record.get('delivery_id')
            or record.get('commit') == meta.get('commit')
            or record.get('previous_release') != meta.get('release_id')
            or candidate_worker_running(record)):
        raise RuntimeError('槽位存在未完成候选，不能恢复种子')
    r.check_process_ownership(env, meta)
    r.check_live_environment(env, meta, r.read(r.config_path(env)))
    slots = control_api('delivery-queue/slots')
    slot = next((item for item in slots if item['name'] == env), None)
    deliveries = control_api('delivery-queue?current=' + record['delivery_id'])
    delivery = next((item for item in deliveries if item['id'] == record['delivery_id']), None)
    if (not slot or slot['state'] != 'idle' or slot['enabled'] or slot.get('delivery_id')
            or not delivery or delivery['phase'] not in ('needs_changes', 'cancelled')
            or delivery.get('verification_owner')
            or (delivery.get('verification_run') or {}).get('status') in
               ('queued', 'dispatched', 'running', 'waiting_local_directory')):
        raise RuntimeError('旧候选交付或槽位尚未释放，不能恢复种子')
    archive = r.RUN/'candidates/recovered'/(record['id'] + '.json')
    if archive.exists():
        if r.read(archive) != record:
            raise RuntimeError('旧候选归档与当前记录不符')
    else:
        r.write(archive, record, True)
    record = dict(record, status='failed', maintenance_recovery=dict(
        previous_status=record['status'], delivery_revision=delivery['revision'],
        live_commit=meta['commit'], live_release=meta['release_id'],
        reason='交付已失败且故障租约已收回，保存旧记录后允许按已验证种子恢复'))
    r.save_candidate(record)


def restore(env, seed, pgconfig):
    """把种子装进专属槽位；调用方必须先让槽位进入维护态。"""
    if env not in r.VERIFICATION_ENVS or os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('仅外部维护身份可重置固定验收环境')
    path, manifest, artifact = read_seed(seed)
    sql_source = verified_sql_seed(path, manifest)
    container, port, _ = postgres(pgconfig)
    with r.lock(r.candidate_lock(env), wait=True):
        meta = r.read(r.meta_path(env)); cfg = r.read(r.config_path(env))
        r.validate_verification_config(env, cfg)
        target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
        dbname = 'multica_' + env.replace('-', '_'); role = env.replace('-', '_')
        if target.hostname != '127.0.0.1' or str(target.port) != port or target.path != '/' + dbname or target.username != role:
            raise RuntimeError('槽位实际连接与专属实例不一致')
        retire_failed_candidate(env, meta)
        record_path = r.RUN/'verification-install'/(env + '.json')
        record = r.read(record_path)
        running = Path(meta['release_dir'])
        cleaned = record.get('stage') == 'cleaned'
        restoring = record.get('stage') in ('stopping','resetting','account','starting')
        if restoring:
            if record.get('seed_id')!=(manifest.get('seed_id') or path.name) or record.get('seed_sha256')!=artifact['sha256']:
                raise RuntimeError('上次种子恢复未完成，不能切换恢复目标')
            release=Path(record.get('restore_release') or running)
            validate_seed_release(manifest,release)
            package=r.read(release/'release.json')
            actual=r.checksums(release)
            if any(actual.get(key)!=value for key,value in package.get('files',{}).items()):
                raise RuntimeError('恢复中的目标制品已变化')
        else:
            selected=restore_release(manifest,running)
            release=running
            if selected.resolve()!=running.resolve():
                release=r.idle_slot(env)
                r.copy_tree(selected,release)
                if r.checksums(release)!=r.read(selected/'release.json')['files']:
                    raise RuntimeError('种子基线复制后完整校验失败')
            if cleaned:
                r.check_process_ownership(env, meta, allow_unhealthy=True)
                if any(r.exists(pid) for pid in meta.get('pids',{}).values()):
                    raise RuntimeError('清理后的槽位仍有运行进程')
            else:
                r.check_process_ownership(env, meta)
                r.check_live_environment(env, meta, cfg)
        policy_spec=importlib.util.spec_from_file_location('test_agent_control',Path(__file__).with_name('test-agent-control.py'))
        policy_control=importlib.util.module_from_spec(policy_spec);policy_spec.loader.exec_module(policy_control)
        policy_control.refresh_default(r.ROOT,env)
        # 记录选定的完整制品；停机或启动中断后仍按同一目标和原PID核对接续。
        record.update(stage='stopping',seed=str(path),seed_id=manifest.get('seed_id') or path.name,
                      seed_sha256=artifact['sha256'],restore_release=str(release))
        r.write(record_path,record,True)
        r.check_process_ownership(env,meta,allow_unhealthy=True)
        daemon=meta['pids'].get('daemon')
        if daemon and r.exists(daemon):
            r.drain(env, meta)
            r.http(r.health_url(env) + '/shutdown', 'POST')
            deadline = time.monotonic() + 30
            while r.exists(daemon) and time.monotonic() < deadline:time.sleep(.2)
            if r.exists(daemon):raise RuntimeError('Daemon 未退出')
        for service in ('web','server'):
            pid=meta['pids'].get(service)
            if pid:r.owned_stop(pid,service,running/'apps/web' if service=='web' else running)
        # 不使用 FORCE；出现未识别连接时保留现场并停止。
        if sql(container, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='" + dbname + "'") != '0':
            raise RuntimeError('验收数据库仍有消费者，拒绝重置')
        clear_verification_uploads(env, cfg, record)
        record['stage']='resetting';r.write(record_path,record,True)
        # 前一个任务可能新增迁移；重新建立专属槽位数据库以免结构和迁移记录泄漏到下一个任务。
        sql(container, 'postgres', f'DROP DATABASE {dbname};\nCREATE DATABASE {dbname} OWNER {role};\nREVOKE CONNECT ON DATABASE {dbname} FROM PUBLIC;\nGRANT CONNECT ON DATABASE {dbname} TO {role};')
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server', env={**cfg['server'], 'MULTICA_MIGRATION_BASELINE':'true'})
        pending=record.get('staging_database')
        if pending:
            if not re.fullmatch(r'verification_seed_import_[0-9a-f]{16}',pending):raise RuntimeError('恢复导入库身份不符')
            if counter(container,'postgres',"SELECT count(*) FROM pg_stat_activity WHERE datname='%s'" % pending):
                raise RuntimeError('上次种子导入仍有消费者，保持维护')
            sql(container,'postgres','DROP DATABASE IF EXISTS '+pending+';')
        record.pop('staging_database', None); r.write(record_path, record, True)
        if sql_source:
            import_sql_seed(container, dbname, sql_source)
            r.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), env, 'direct', str(pgconfig)], cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})
        else:
            staging = 'verification_seed_import_' + secrets.token_hex(8)
            record['staging_database'] = staging; r.write(record_path, record, True)
            sql(container, 'postgres', f'CREATE DATABASE {staging};')
            try:
                with (path/artifact['dump']).open('rb') as stream:
                    subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', staging, '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
                r.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), env, staging, str(pgconfig)], cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})
            finally:
                sql(container, 'postgres', f'DROP DATABASE {staging};')
        check_restored_data(container, dbname, manifest)
        # 种子可能来自另一槽位；登录凭据属于槽位安装记录，重置后重新绑定到本槽位密码。
        hashed = password_hash(record['password']).replace("'", "''")
        sql(container, dbname, "UPDATE \"user\" SET password_hash='%s' WHERE account='%s';" % (hashed, record['account'].replace("'", "''")))
        previous=dict(meta)
        if release.resolve()!=running.resolve():r.update_release_metadata(env,meta,release,previous)
        meta['pids']={};r.write(r.meta_path(env),meta,True)
        record['stage']='account';r.write(record_path,record,True)
        pid = r.start([release/'server/bin/server'], release, cfg['server'], meta['log_paths']['server'])
        meta['pids']={'server':pid};r.write(r.meta_path(env),meta,True)
        try:
            r.ready(meta['backend_url'] + '/health', pid=pid)
            login = i.api(meta['backend_url'], '/auth/login', dict(account=record['account'], password=record['password']))
            me = i.api(meta['backend_url'], '/api/me/onboarding/complete', dict(completion_path='skip_existing'), login['token'])
            user = me['id']
            sql(container, dbname, f"INSERT INTO member(workspace_id,user_id,role) SELECT id,'{user}','owner' FROM workspace WHERE NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=workspace.id AND m.user_id='{user}'); UPDATE agent SET owner_id='{user}';")
            request = urllib.request.Request(meta['backend_url'] + '/api/workspaces', headers={'Authorization': 'Bearer ' + login['token']})
            with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
                workspaces = json.load(response)
            # 1. 种子里的业务样本可能在任一工作区；按真实任务数选择，Daemon 才能打开有数据的那个。
            ranked = sorted(workspaces, key=lambda item: counter(container, dbname, "SELECT count(*) FROM issue WHERE workspace_id='%s'" % item['id']), reverse=True)
            ws = ranked[0] if ranked else i.api(meta['backend_url'], '/api/workspaces', dict(name='任务验收', slug='verification'), login['token'])
            profile_path = Path(meta['daemon_profile_path']); profile = r.read(profile_path)
            profile.update(token=login['token'], workspace_id=ws['id']); r.write(profile_path, profile, True)
            account = i.pwd.getpwnam(cfg['daemon']['MULTICA_SERVICE_USER']); os.chown(profile_path, account.pw_uid, account.pw_gid)
        finally:
            r.owned_stop(pid, 'server', release)
        record['stage']='starting';r.write(record_path,record,True)
        meta['pids']={};r.write(r.meta_path(env),meta,True)
        try:
            meta['pids'] = r.launch(env, release, cfg, meta)
        finally:
            # 部分启动也保存已创建的PID；重入只回收归属核对通过的进程。
            r.write(r.meta_path(env), meta, True)
            for service,pid in meta['pids'].items():(r.RUN/(env+'-'+service+'.pid')).write_text(str(pid)+'\n')
        i.verify_installed(env)
        # 导入时清除来源 runtime 标识；按当前生产测试默认值绑定本地 runtime。
        r.run(['node', str(Path(__file__).with_name('test-agent-models.mjs')), env, 'apply'], cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT':str(r.ROOT)})
    return dict(environment=env, seed_id=manifest.get('seed_id') or path.name, container=container, database=dbname)


def reset(env, seed, pgconfig):
    """单槽位维护：停接单、恢复种子、恢复接单；失败保持停用，不影响其他槽位。"""
    path, manifest, artifact = read_seed(seed)
    previously_enabled = begin_maintenance(env, '按种子恢复测试数据')
    try:
        result = restore(env, seed, pgconfig)
    except Exception as error:
        try:
            record_slot_seed(env, manifest.get('seed_id') or path.name, artifact['sha256'], False)
        except Exception:
            pass
        raise RuntimeError('种子恢复失败，槽位保持维护：' + str(error)) from error
    record_slot_seed(env, result['seed_id'], artifact['sha256'], True)
    if previously_enabled:
        resume(env, '种子恢复完成')
    return result


def ensure(env, pgconfig):
    """1. 换任务前核对槽位种子；与当前版本不一致时先恢复，成功后才允许领取新候选。"""
    if not current_path().exists():
        return dict(status='skipped', reason='未配置当前种子，保持原有行为')
    current = r.read(current_path())
    path, manifest, artifact = read_seed(SEEDS / current['seed_id'])
    slot = slot_seed(env)
    if slot and slot.get('seed_id') == current['seed_id'] and slot.get('seed_sha256') == current['dump_sha256'] and slot.get('verified') is True:
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
    if origin is None:
        path, artifact = None, None
        manifest = dict(source=dict(environment='fixtures-sql', collected_at=now()))
    else:
        path, manifest, artifact = read_seed(origin)
    container, port, password = postgres(pgconfig)
    release = Path(release).resolve()
    if not (release/'server/bin/migrate').exists():
        raise RuntimeError('构建种子需要目标候选的迁移程序')
    migration_identity = release_schema_identity(release)
    target = BUILD_PREFIX + secrets.token_hex(8)
    staging = 'verification_seed_import_' + secrets.token_hex(8)
    dsn = 'postgres://postgres:%s@127.0.0.1:%s/%s' % (urllib.parse.quote(password, safe=''), port, target)
    created = []
    try:
        for database in (target, staging):
            sql(container, 'postgres', 'CREATE DATABASE ' + database)
            created.append(database)
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server', env={**os.environ, 'DATABASE_URL': dsn, 'MULTICA_MIGRATION_BASELINE': 'true'})
        if origin is None:
            staging_dsn = 'postgres://postgres:%s@127.0.0.1:%s/%s' % (urllib.parse.quote(password, safe=''), port, staging)
            r.run([release/'server/bin/migrate', 'up'], cwd=release/'server',
                  env={**os.environ, 'DATABASE_URL': staging_dsn, 'MULTICA_MIGRATION_BASELINE': 'true'})
            import_sql_seed(container, staging, FIXTURES)
        else:
            with (path/artifact['dump']).open('rb') as stream:
                subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', staging, '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
        copied = subprocess.run(['node', str(Path(__file__).with_name('verification-seed-copy.mjs')), 'build', staging, str(pgconfig), '--target-database', target, '127.0.0.1', port],
                                cwd=r.ROOT, env={**os.environ, 'MULTICA_RELEASE_ROOT': str(r.ROOT)}, capture_output=True, text=True)
        if copied.returncode:
            raise RuntimeError('种子导入失败: ' + (copied.stderr or copied.stdout).strip()[:1000])
        copy_report = json.loads(copied.stdout.strip().splitlines()[-1])
        schema = schema_identity(container, target)
        schema['migration_files_sha256'] = migration_identity
        counts = row_counts(container, target)
        # 1. 重建种子的结构目标是本次候选，不是来源种子的旧结构；比对基准改为刚迁移出的目标结构。
        checks = data_checks(container, target, dict(manifest, fixture=dict(manifest.get('fixture', {}), row_counts=counts), schema=dict(fingerprint=schema['fingerprint'])))
        checks += scrub_checks(container, target)
        failed = [c for c in checks if c['status'] != 'passed']
        if failed:
            raise RuntimeError('种子构建检查失败: ' + ', '.join(c['name'] for c in failed))
        destination.mkdir(parents=True, mode=0o700)
        dump_path = destination/'seed.dump'
        with dump_path.open('xb') as stream:
            dump_path.chmod(0o600)
            subprocess.run(['docker', 'exec', container, 'pg_dump', '-U', 'postgres', '-d', target, '-Fc', '--no-owner', '--no-acl'], stdout=stream, check=True)
        # SQL 由已完成清理和检查的构建库生成；迁移台账属于目标库，不能从种子覆盖。
        sql_path = destination/'seed.sql'
        with sql_path.open('xb') as stream:
            sql_path.chmod(0o600)
            subprocess.run(['docker', 'exec', container, 'pg_dump', '-U', 'postgres', '-d', target,
                            '--data-only', '--column-inserts', '--rows-per-insert=100', '--no-owner', '--no-acl',
                            '--exclude-table=public.schema_migrations'], stdout=stream, check=True)
        record = dict(seed_id=seed_id, version=2, status='built', source=source_identity(manifest),
                      artifact=dict(dump='seed.dump', sha256=digest(dump_path), bytes=dump_path.stat().st_size),
                      sql_artifact=dict(file='seed.sql', sha256=digest(sql_path), bytes=sql_path.stat().st_size),
                      schema=schema, fixture=dict(version=FIXTURE_VERSION, row_counts=counts),
                      scrub=scrub_identity(), build=dict(environment='isolated-build', at=now(), release=str(release), checks=checks, tables=copy_report.get('tables', [])))
        if origin is None:
            record['source']['fixture_sql_sha256'] = digest(FIXTURES)
        write_manifest(destination, record)
        read_sql_seed(destination, record)
    finally:
        for database in reversed(created):
            sql(container, 'postgres', 'DROP DATABASE ' + database)
    print(json.dumps(dict(status='built', seed=str(destination), schema=schema, rows=counts), ensure_ascii=False))
    return destination


def verify_seed(env, seed, pgconfig, accept=False):
    """3. 恢复并验收当前种子；失败不替换当前版本，槽位保持维护。"""
    path, manifest, artifact = read_seed(seed)
    seed_id = manifest.get('seed_id') or path.name
    previously_enabled = begin_maintenance(env, '种子恢复验收')
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
    if previously_enabled:
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
    build = sub.add_parser('build'); build.add_argument('--id', required=True); source = build.add_mutually_exclusive_group(required=True); source.add_argument('--from', dest='origin', type=Path); source.add_argument('--from-sql', action='store_true'); build.add_argument('--release', type=Path, required=True); build.add_argument('--postgres-config', type=Path, required=True)
    check_sql = sub.add_parser('check-sql'); check_sql.add_argument('--seed', type=Path, required=True); check_sql.add_argument('--release', type=Path, required=True); check_sql.add_argument('--postgres-config', type=Path, required=True)
    verify = sub.add_parser('verify'); verify.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True); verify.add_argument('--seed', type=Path, required=True); verify.add_argument('--postgres-config', type=Path, required=True); verify.add_argument('--accept', action='store_true')
    ensure_command = sub.add_parser('ensure'); ensure_command.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True); ensure_command.add_argument('--postgres-config', type=Path, required=True)
    status_command = sub.add_parser('status')
    for item in (verify, reset_command, ensure_command, status_command):
        item.add_argument('--control', choices=('prod', 'int', *r.VERIFICATION_ENVS), default='prod', help='承载交付队列接口的控制端；演练时指向隔离环境')
    args = parser.parse_args()
    CONTROL_ENVIRONMENT = getattr(args, 'control', 'prod')
    if args.action == 'export':
        export_seed(args.destination.resolve())
    elif args.action == 'reset':
        print(json.dumps(reset(args.environment, args.seed.resolve(), args.postgres_config.resolve()), ensure_ascii=False))
    elif args.action == 'ensure':
        print(json.dumps(ensure(args.environment, args.postgres_config.resolve()), ensure_ascii=False))
    elif args.action == 'build':
        build_seed(args.id, args.origin.resolve() if args.origin else None, args.release.resolve(), args.postgres_config.resolve())
    elif args.action == 'check-sql':
        verify_sql_seed(args.seed.resolve(), args.release.resolve(), args.postgres_config.resolve())
    elif args.action == 'verify':
        verify_seed(args.environment, args.seed.resolve(), args.postgres_config.resolve(), args.accept)
    else:
        status()
