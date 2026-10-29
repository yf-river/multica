"""Run a verify frontend from its retained task checkout."""
import base64
import os
import pwd
import re
import signal
import subprocess
import time
from pathlib import Path


def marker(r, env):
    if env not in r.VERIFICATION_ENVS:
        raise RuntimeError('热更新仅用于 verify 环境')
    return r.RUN / 'verification-dev' / (env + '.json')


def mounted(path):
    return subprocess.run(['mountpoint', '-q', str(path)]).returncode == 0


def adopt(r, env, previous, current):
    journal = marker(r, env)
    if not journal.exists(): return
    state = r.read(journal)
    if state.get('environment') != env or state.get('delivery_id') != previous.get('delivery_id') or state.get('generation') != previous.get('generation') or current.get('delivery_id') != previous.get('delivery_id') or current.get('generation', 0) <= previous.get('generation', 0):
        raise r.RecoveryRequired('无法确认热更新恢复代次')
    state['generation'] = current['generation']
    r.write(journal, state, True)


def update_next_env(path, original):
    current = path.read_bytes()
    generated = original.replace(b'./.next/types/', b'./.next/dev/types/')
    if current == original:
        return
    if current != generated:
        raise RuntimeError('Next 类型文件包含其他修改，保留现场')
    path.write_bytes(original)


def restore_acl(raw, source):
    records = []
    roots = [(source / 'apps/web/.next').resolve(), (source / 'apps/web/.source').resolve(), (source / 'apps/web/next-env.d.ts').resolve()]
    for block in raw.split(b'\n\n'):
        if not block.strip(): continue
        first = block.splitlines()[0]
        if not first.startswith(b'# file: '): raise RuntimeError('ACL 恢复记录格式错误')
        encoded = first[len(b'# file: '):]
        encoded = re.sub(rb'\\([0-7]{3})', lambda m: bytes([int(m.group(1), 8)]), encoded)
        path = Path(os.fsdecode(encoded))
        if not path.exists() and not path.is_symlink(): continue
        if path.is_symlink() or not any(path.resolve() == root or path.resolve().is_relative_to(root) for root in roots):
            raise RuntimeError('ACL 恢复路径身份变化')
        records.append(block)
    if records:
        subprocess.run(['setfacl', '--restore=-'], input=b'\n\n'.join(records)+b'\n', check=True)


def start(r, env, source, lease):
    journal = marker(r, env)
    if journal.exists():
        raise r.RecoveryRequired('验收热更新已有现场，需要先恢复')
    source = Path(source).resolve(strict=True)
    cfg = r.read(r.config_path(env)); meta = r.read(r.meta_path(env))
    r.validate_verification_config(env, cfg)
    r.check_process_ownership(env, meta)
    r.check_live_environment(env, meta, cfg)
    if lease.get('name') != env or not lease.get('delivery_id') or not lease.get('generation'):
        raise RuntimeError('缺少当前验收槽位身份')
    # Only ignored build inputs may be visible to the test server.
    ignored = r.output(['git', 'ls-files', '--others', '--ignored', '--exclude-standard', '--directory'], cwd=source).splitlines()
    def build_output(name):
        parts = Path(name).parts
        return ('node_modules' in parts or '.turbo' in parts or '__pycache__' in parts
                or name.startswith(('apps/web/.next/', 'apps/web/.source/', 'apps/docs/.source/', 'server/bin/'))
                or name.endswith('.tsbuildinfo'))
    if any(not build_output(name) for name in ignored):
        raise RuntimeError('任务目录有未识别的忽略文件，不能向测试进程开放')
    web = source / 'apps/web'
    config = web / 'next.config.ts'
    if not config.is_file() or 'agentRules: false' not in config.read_text():
        raise RuntimeError('任务前端未关闭 Next 自动生成 Agent 指令')
    uid = pwd.getpwnam(cfg['web']['MULTICA_SERVICE_USER']).pw_uid
    paths = [web / '.next', web / '.source', web / 'next-env.d.ts']
    for path in paths[:2]:
        if path.is_symlink(): raise RuntimeError('前端生成目录不能是符号链接')
        path.mkdir(exist_ok=True)
    if paths[2].is_symlink() or not paths[2].is_file(): raise RuntimeError('前端类型文件身份不符')
    target = Path(cfg['web']['HOME']).parent / 'hot-source'
    target.mkdir(mode=0o755, exist_ok=True)
    if target.is_symlink() or target.stat().st_uid != 0 or mounted(target):
        raise RuntimeError('验收挂载目录身份不符或已占用')
    acl = subprocess.check_output(['getfacl', '-R', '-P', '-p', *map(str, paths)])
    original = paths[2].read_bytes()
    state = dict(environment=env, source=str(source), target=str(target), delivery_id=lease['delivery_id'],
                 generation=lease['generation'], release_dir=meta['release_dir'], original=base64.b64encode(original).decode(),
                 acl=base64.b64encode(acl).decode(), state='preparing')
    r.write(journal, state, True)
    try:
        subprocess.run(['setfacl', '-R', '-P', '-m', f'u:{uid}:rwX', *map(str, paths[:2])], check=True)
        for directory in paths[:2]:
            for item in [directory, *(p for p in directory.rglob('*') if p.is_dir() and not p.is_symlink())]:
                subprocess.run(['setfacl', '-m', f'd:u:{uid}:rwx', str(item)], check=True)
        subprocess.run(['setfacl', '-m', f'u:{uid}:rw', str(paths[2])], check=True)
        subprocess.run(['mount', '--bind', str(source), str(target)], check=True)
        for relative in ('apps/web/.next', 'apps/web/.source', 'apps/web/next-env.d.ts'):
            subprocess.run(['mount', '--bind', str(source / relative), str(target / relative)], check=True)
        subprocess.run(['mount', '-o', 'remount,bind,ro', str(target)], check=True)
        state['state'] = 'mounted'; r.write(journal, state, True)
        oldpid = meta['pids']['web']
        r.owned_stop(oldpid, 'web', Path(meta['release_dir']) / 'apps/web')
        meta['pids'].pop('web', None); r.write(r.meta_path(env), meta, True)
        args = ['node', target / 'apps/web/node_modules/next/dist/bin/next', 'dev', '--webpack', '--port', meta['frontend_port'], '--hostname', '0.0.0.0']
        pid = r.start(args, target / 'apps/web', {**cfg['web'], 'NEXT_TELEMETRY_DISABLED': '1', 'MULTICA_RELEASE_MANIFEST': str(Path(meta['release_dir']) / 'release.json')}, meta['log_paths']['web'])
        meta['pids']['web'] = pid
        meta.setdefault('role_release_dirs', {})['web'] = str(target)
        meta['frontend_mode'] = 'dev'
        state.update(state='starting', pid=pid); r.write(journal, state, True); r.write(r.meta_path(env), meta, True)
        r.ready(meta['frontend_url'] + '/login', pid=pid)
        if r.http(meta['frontend_url'] + '/api/config').get('environment_id') != env:
            raise RuntimeError('热更新前端连接了错误环境')
        update_next_env(paths[2], original)
        state['state'] = 'running'; r.write(journal, state, True)
    except Exception as error:
        try: stop(r, env, lease)
        except Exception as recovery: raise r.RecoveryRequired('热更新启动失败且恢复未完成：' + str(recovery)) from error
        raise


def stop(r, env, lease):
    journal = marker(r, env)
    if not journal.exists(): return
    state = r.read(journal)
    if state.get('environment') != env or state.get('delivery_id') != lease.get('delivery_id') or state.get('generation') != lease.get('generation'):
        raise r.RecoveryRequired('热更新记录与当前槽位不符')
    cfg = r.read(r.config_path(env)); meta = r.read(r.meta_path(env))
    source = Path(state['source']); target = Path(state['target'])
    if target != Path(cfg['web']['HOME']).parent / 'hot-source' or not source.is_dir():
        raise r.RecoveryRequired('热更新挂载身份不符')
    pid = state.get('pid')
    if pid and r.exists(pid):
        if Path(f'/proc/{pid}/cwd').resolve() != target / 'apps/web' or os.getpgid(pid) != pid:
            raise r.RecoveryRequired('热更新进程身份不符')
        os.killpg(pid, signal.SIGTERM)
        deadline = time.monotonic() + 30
        while r.exists(pid) and time.monotonic() < deadline: time.sleep(.2)
        if r.exists(pid): raise r.RecoveryRequired('热更新进程未退出')
    for relative in ('apps/web/next-env.d.ts', 'apps/web/.source', 'apps/web/.next', ''):
        dest = target / relative
        if mounted(dest):
            if not os.path.samefile(source / relative, dest): raise r.RecoveryRequired('热更新挂载来源已变化')
            deadline = time.monotonic() + 30
            while True:
                result = subprocess.run(['umount', str(dest)], capture_output=True, text=True)
                if result.returncode == 0: break
                if time.monotonic() >= deadline:
                    raise r.RecoveryRequired('热更新挂载仍在使用：' + (result.stderr or result.stdout).strip())
                time.sleep(.2)
    original = base64.b64decode(state['original'])
    update_next_env(source / 'apps/web/next-env.d.ts', original)
    restore_acl(base64.b64decode(state['acl']), source)
    if not meta.get('pids', {}).get('web') or not r.exists(meta['pids']['web']):
        release = Path(state['release_dir'])
        if meta['release_dir'] != str(release): raise r.RecoveryRequired('槽位运行版本已变化')
        command = ['node', release / 'apps/web/server.js'] if (release / 'apps/web/server.js').exists() else ['node', release / 'apps/web/node_modules/next/dist/bin/next', 'start', '-p', str(meta['frontend_port']), '-H', '0.0.0.0']
        pid = r.start(command, release / 'apps/web', {**cfg['web'], 'MULTICA_RELEASE_MANIFEST': str(release / 'release.json')}, meta['log_paths']['web'])
        meta.setdefault('pids', {})['web'] = pid
        meta.get('role_release_dirs', {}).pop('web', None)
        meta['frontend_mode'] = 'standalone'; r.write(r.meta_path(env), meta, True)
        r.ready(meta['frontend_url'] + '/login', pid=pid)
    journal.unlink()
