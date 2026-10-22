#!/usr/bin/env python3
"""固定验收环境复用联调种子；源环境只读，重置仅限独立验收实例。"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
import secrets
import base64
import hashlib
import urllib.parse
import urllib.request

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('verification-environments.py'))
i = importlib.util.module_from_spec(spec)
spec.loader.exec_module(i)
r = i.r


def postgres(config):
    record = r.read(config)
    inspected = json.loads(r.output(['docker', 'inspect', record['container']]))[0]
    if inspected['Id'] != record['container_id'] or inspected['Config'].get('Labels', {}).get('multica.purpose') != 'verification-pool':
        raise RuntimeError('验收数据库实例身份已变化')
    ports = inspected['NetworkSettings']['Ports']['5432/tcp']
    if len(ports) != 1 or ports[0]['HostIp'] != '127.0.0.1':
        raise RuntimeError('验收数据库端口身份不匹配')
    return record['container'], ports[0]['HostPort']


def sql(container, database, statement):
    return subprocess.run(['docker', 'exec', '-i', container, 'psql', '-U', 'postgres', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'], input=statement, text=True, capture_output=True, check=True).stdout.strip()


def password_hash(password):
    salt = secrets.token_bytes(16)
    digest = hashlib.pbkdf2_hmac('sha256', password.encode(), salt, 210000, 32)
    return 'pbkdf2_sha256$210000$%s$%s' % (
        base64.b64encode(salt).decode().rstrip('='),
        base64.b64encode(digest).decode().rstrip('='),
    )


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
    # 完整导出保持既有数据关联；验收副本启动前停用执行和外部触发。
    excluded = []
    args = ['docker', 'exec', matches[0], 'pg_dump', '-U', urllib.parse.unquote(target.username), '-d', urllib.parse.unquote(target.path[1:]), '-Fc', '--no-owner', '--no-acl']
    args += ['--exclude-table-data=public.'+name for name in excluded]
    with dump.open('xb') as stream:
        dump.chmod(0o600)
        subprocess.run(args, stdout=stream, check=True)
    r.write(destination/'seed.json', dict(source_environment='int', source_commit=meta['commit'], collected_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), database=target.path[1:], dump_sha256=hashlib.sha256(dump.read_bytes()).hexdigest(), excluded_table_data=excluded), True)
    print(json.dumps(dict(status='exported', environment='int', seed=str(destination))))


def reset(env, seed, pgconfig):
    if env not in r.VERIFICATION_ENVS or os.environ.get('MULTICA_TASK_ID'):
        raise RuntimeError('仅外部维护身份可重置固定验收环境')
    manifest = r.read(seed/'seed.json')
    if not str(manifest['source_environment']).startswith('int') or hashlib.sha256((seed/'seed.dump').read_bytes()).hexdigest() != manifest['dump_sha256']:
        raise RuntimeError('联调种子校验失败')
    container, port = postgres(pgconfig)
    with r.lock(r.candidate_lock(env), wait=True):
        meta = r.read(r.meta_path(env)); cfg = r.read(r.config_path(env))
        r.validate_verification_config(env, cfg)
        target = urllib.parse.urlparse(cfg['server']['DATABASE_URL'])
        dbname = 'multica_'+env.replace('-', '_'); role = env.replace('-', '_')
        if target.hostname != '127.0.0.1' or str(target.port) != port or target.path != '/'+dbname or target.username != role:
            raise RuntimeError('槽位实际连接与专属实例不一致')
        if r.candidate_path(env).exists() and r.candidate(env=env)['status'] not in ('released', 'failed', 'succeeded'):
            raise RuntimeError('槽位存在未完成候选')
        pool = r.RUN/'verification-pool.json'
        if pool.exists() and r.read(pool).get('enabled'):
            raise RuntimeError('先停用环境池并等待占用释放，再执行种子重置')
        record_path = r.RUN/'verification-install'/f'{env}.json'
        record = r.read(record_path)
        release = Path(meta['release_dir'])
        if record.get('stage') == 'resetting':
            if any(r.exists(pid) for pid in meta['pids'].values()):
                raise RuntimeError('重置中断后仍有运行进程，需要核对现场')
        else:
            r.check_process_ownership(env, meta)
            r.check_live_environment(env, meta, cfg)
            r.drain(env, meta)
            r.http(r.health_url(env)+'/shutdown', 'POST')
            deadline = time.monotonic()+30
            while r.exists(meta['pids']['daemon']) and time.monotonic() < deadline:
                time.sleep(.2)
            if r.exists(meta['pids']['daemon']):
                raise RuntimeError('Daemon 未退出')
            for service in ('web', 'server'):
                r.owned_stop(meta['pids'][service], service, release/'apps/web' if service == 'web' else release)
        # 不使用 FORCE；出现未识别连接时保留现场并停止。
        if sql(container, 'postgres', "SELECT count(*) FROM pg_stat_activity WHERE datname='"+dbname+"'") != '0':
            raise RuntimeError('验收数据库仍有消费者，拒绝重置')
        record.update(stage='resetting', seed=str(seed));r.write(record_path, record, True)
        sql(container, 'postgres', f'DROP DATABASE {dbname};\nCREATE DATABASE {dbname} OWNER {role};\nREVOKE CONNECT ON DATABASE {dbname} FROM PUBLIC;\nGRANT CONNECT ON DATABASE {dbname} TO {role};')
        r.run([release/'server/bin/migrate', 'up'], cwd=release/'server', env={**cfg['server'], 'MULTICA_MIGRATION_BASELINE':'true'})
        staging = 'verification_seed_import_'+secrets.token_hex(8)
        record['staging_database']=staging;r.write(record_path, record, True)
        sql(container, 'postgres', f'CREATE DATABASE {staging};')
        try:
            with (seed/'seed.dump').open('rb') as stream:
                subprocess.run(['docker', 'exec', '-i', container, 'pg_restore', '-U', 'postgres', '-d', staging, '--no-owner', '--no-acl', '--exit-on-error'], stdin=stream, check=True)
            r.run(['node', Path(__file__).with_name('verification-seed-copy.mjs'), env, staging, pgconfig], cwd=r.ROOT, env={**os.environ,'MULTICA_RELEASE_ROOT':str(r.ROOT)})
        finally:
            sql(container, 'postgres', f'DROP DATABASE {staging};')
        sql(container, dbname, "UPDATE agent_runtime SET status='offline'; UPDATE agent SET status='offline', custom_env='{}',runtime_config='{}',mcp_config=NULL; UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE status NOT IN ('completed','failed','cancelled'); UPDATE autopilot SET status='paused'; UPDATE autopilot_trigger SET enabled=false; UPDATE plugin_hook_schedule SET enabled=false; UPDATE life_cognition_job SET status='cancelled' WHERE status IN ('queued','running'); UPDATE supervision_config SET enabled=false; UPDATE issue_delivery SET phase='needs_changes',error='验收种子中的历史交付，不执行'; DELETE FROM domain_event_outbox;")
        sql(container, dbname, "DO $$ BEGIN IF to_regclass('public.upstream_sync_config') IS NOT NULL THEN UPDATE upstream_sync_config SET sync_state='paused'; END IF; END $$;")
        # 种子可能来自另一槽位；登录凭据属于槽位安装记录，重置后重新绑定到本槽位密码。
        hashed = password_hash(record['password']).replace("'", "''")
        sql(container, dbname, "UPDATE \"user\" SET password_hash='%s' WHERE account='%s';" % (hashed, record['account'].replace("'", "''")))
        pid = r.start([release/'server/bin/server'], release, cfg['server'], meta['log_paths']['server'])
        try:
            r.ready(meta['backend_url']+'/health', pid=pid)
            login = i.api(meta['backend_url'], '/auth/login', dict(account=record['account'], password=record['password']))
            me = i.api(meta['backend_url'], '/api/me/onboarding/complete', dict(completion_path='skip_existing'), login['token'])
            user = me['id']
            sql(container, dbname, f"INSERT INTO member(workspace_id,user_id,role) SELECT id,'{user}','owner' FROM workspace WHERE NOT EXISTS(SELECT 1 FROM member m WHERE m.workspace_id=workspace.id AND m.user_id='{user}'); UPDATE agent SET owner_id='{user}';")
            # 数据种子提供历史样本；验收使用本槽位账号和本槽位 Daemon。
            request = urllib.request.Request(meta['backend_url']+'/api/workspaces', headers={'Authorization':'Bearer '+login['token']})
            with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
                workspaces = json.load(response)
            ws = next((w for w in workspaces if w['slug']=='ai-studio'), None) or (workspaces[0] if workspaces else i.api(meta['backend_url'], '/api/workspaces', dict(name='任务验收', slug='verification'), login['token']))
            profile_path = Path(meta['daemon_profile_path']);profile = r.read(profile_path)
            profile.update(token=login['token'], workspace_id=ws['id']);r.write(profile_path, profile, True)
            account = i.pwd.getpwnam(cfg['daemon']['MULTICA_SERVICE_USER']);os.chown(profile_path, account.pw_uid, account.pw_gid)
        finally:
            r.owned_stop(pid, 'server', release)
        meta['pids'] = r.launch(env, release, cfg, meta);r.write(r.meta_path(env), meta, True)
        i.verify_installed(env)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest='action', required=True)
    export = sub.add_parser('export');export.add_argument('--destination', type=Path, required=True)
    restore = sub.add_parser('reset');restore.add_argument('--environment', choices=r.VERIFICATION_ENVS, required=True);restore.add_argument('--seed', type=Path, required=True);restore.add_argument('--postgres-config', type=Path, required=True)
    args = parser.parse_args()
    if args.action == 'export':export_seed(args.destination.resolve())
    else:reset(args.environment, args.seed.resolve(), args.postgres_config)
