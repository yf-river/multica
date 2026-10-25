#!/usr/bin/env python3
"""Prepare supervision test settings only inside an owned, deployed verification candidate."""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import pwd
import stat
import subprocess
from pathlib import Path
import urllib.error
import urllib.request


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def api(meta, profile, method, route, body=None):
    request = urllib.request.Request(
        meta['backend_url'] + '/api/' + route, method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={'Authorization': 'Bearer ' + profile['token'],
                 'X-Workspace-ID': profile['workspace_id'], 'Content-Type': 'application/json'})
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=30) as response:
            raw = response.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        # 1. Report the exact failing operation without leaking response bodies or credentials.
        raise RuntimeError(f'{method} /api/{route}: HTTP {error.code}') from None


def require_idle(cfg):
    # 2. A backend configuration change must not interrupt an active test model execution.
    result = subprocess.run(['psql', '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-c',
        "SELECT count(*) FROM agent_task_queue WHERE status IN ('queued','dispatched','running','waiting_local_directory')"],
        env={**os.environ, 'PGDATABASE': cfg['server']['DATABASE_URL']}, capture_output=True, text=True, timeout=15)
    if result.returncode or result.stdout.strip() != '0':
        raise RuntimeError('Cannot apply service configuration while test tasks are active or inventory is unavailable')


def configure_service(r, environment, meta, cfg, workspace, credential_file):
    # 3. Inject short-lived credentials outside the seed and restart only the verified backend PID.
    source = credential_file.lstat()
    if not stat.S_ISREG(source.st_mode) or stat.S_IMODE(source.st_mode) != 0o600:
        raise RuntimeError('Credential source must be a regular 0600 file')
    credentials = json.loads(credential_file.read_text())
    if credentials.get('workspace_id') != workspace or not credentials.get('token'):
        raise RuntimeError('Credential source must explicitly authorize the test workspace')
    require_idle(cfg)
    private = r.RUN / 'supervision-acceptance' / environment
    private.mkdir(parents=True, exist_ok=True, mode=0o700)
    backup = private / 'original-service.json'
    if backup.exists():
        raise RuntimeError('Previous temporary service configuration requires cleanup before another application')
    owner = pwd.getpwnam(cfg['server']['MULTICA_SERVICE_USER'])
    target = Path(cfg['server']['HOME']) / 'supervision-acceptance-credential.json'
    if target.exists() or target.is_symlink():
        raise RuntimeError('Temporary credential target already exists')
    r.write(backup, cfg, True)
    r.write(target, credentials, True)
    os.chown(target, owner.pw_uid, owner.pw_gid)
    updated = json.loads(json.dumps(cfg))
    updated['server'].update(MULTICA_AGENTLENS_CREDENTIAL_FILE=str(target),
        MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE=datetime.datetime.now(datetime.timezone.utc).isoformat())
    try:
        restart_backend(r, environment, meta, updated)
    except Exception:
        restart_backend(r, environment, meta, cfg)
        target.unlink()
        backup.unlink()
        raise RuntimeError('Temporary configuration failed; original backend configuration restored') from None
    actual = r.process_env(meta['pids']['server'])
    for key in ('MULTICA_AGENTLENS_CREDENTIAL_FILE', 'MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE'):
        if actual.get(key) != updated['server'][key]:
            raise RuntimeError('Temporary service configuration did not reach the backend')
    return backup


def restart_backend(r, environment, meta, cfg):
    # 4. Record the exact replacement PID before readiness, allowing safe recovery after a failed start.
    release = Path(meta['release_dir'])
    r.write(r.config_path(environment), cfg, True)
    r.owned_stop(meta['pids']['server'], 'server', release)
    meta['pids']['server'] = r.start([release / 'server/bin/server'], release, cfg['server'], meta['log_paths']['server'])
    r.write(r.meta_path(environment), meta, True)
    r.ready(meta['backend_url'] + '/healthz', pid=meta['pids']['server'])


def validate_identity(environment, delivery, candidate, metadata, owner, execution, lease, seed, installation):
    # 5. Released candidates and another execution's lease must fail before any test write.
    now = datetime.datetime.now(datetime.timezone.utc)
    expires = datetime.datetime.fromisoformat(owner['expires_at'].replace('Z', '+00:00'))
    if environment != 'verify-1':
        raise RuntimeError('This recipe is limited to verify-1; final int requires its own integration ownership check')
    if not execution or owner.get('execution_id') != execution or expires <= now:
        raise RuntimeError('Current execution does not hold a live delivery owner lease')
    if delivery.get('phase') != 'testing' or candidate.get('status') != 'verifying':
        raise RuntimeError('Candidate is not in active verification')
    if (candidate.get('id') != delivery.get('candidate_id')
            or candidate.get('delivery_id') != delivery.get('id')
            or candidate.get('environment') != environment
            or candidate.get('release_id', candidate['id']) != metadata.get('release_id')
            or candidate.get('commit') != metadata.get('commit')
            or delivery.get('source_commit') != candidate.get('source_commit', candidate.get('commit'))):
        raise RuntimeError('Delivery/candidate/release mapping differs')
    if (lease.get('state') != 'verifying' or lease.get('name') != environment
            or lease.get('delivery_id') != delivery['id']
            or lease.get('candidate_id') != candidate['id']
            or lease.get('source_commit') != delivery['source_commit']):
        raise RuntimeError('Verification lease does not cover the candidate')
    if not seed.get('seed_verified') or seed.get('seed_id') != installation.get('seed_id'):
        raise RuntimeError('Verified seed identity differs')
    if not seed.get('seed_sha256') or seed.get('seed_sha256') != installation.get('seed_sha256'):
        raise RuntimeError('Verified seed checksum differs')


def prepare(args):
    r = load('supervision_release', Path(__file__).with_name('release-environments.py'))
    if args.environment != 'verify-1':
        raise RuntimeError('Only verify-1 is supported by this recipe')
    control = r.read(r.meta_path('prod'))
    control_profile = r.read(control['daemon_profile_path'])
    meta = r.read(r.meta_path(args.environment))
    profile = r.read(meta['daemon_profile_path'])
    candidate = r.candidate(args.candidate, args.environment)

    def guard():
        status = api(control, control_profile, 'GET', 'issues/' + args.issue + '/deliveries')
        delivery = next(x for x in status['deliveries'] if x['id'] == args.delivery)
        lease = api(control, control_profile, 'GET', 'delivery-queue/slots?delivery_id=' + args.delivery)
        seed = next(x for x in api(control, control_profile, 'GET', 'delivery-queue/slots') if x['name'] == args.environment)
        validate_identity(args.environment, delivery, candidate, r.read(r.meta_path(args.environment)),
                          status['owner'], os.environ.get('MULTICA_TASK_ID'), lease, seed,
                          r.read(r.RUN / 'verification-install' / (args.environment + '.json')))
        r.validate_candidate(candidate)
        cfg = r.read(r.config_path(args.environment))
        r.validate_verification_config(args.environment, cfg)
        r.check_process_ownership(args.environment, meta)
        actual = r.process_env(meta['pids']['server'])
        if actual.get('DATABASE_URL') != cfg['server']['DATABASE_URL']:
            raise RuntimeError('Live backend database differs from controlled service configuration')
        if api(meta, profile, 'GET', 'config').get('environment_id') != args.environment:
            raise RuntimeError('Live API environment identity differs')
        return cfg

    with r.lock(r.candidate_lock(args.environment)), r.lock(args.environment):
        cfg = guard()
        if args.cleanup:
            # 6. Cleanup stays inside the current lease and restores service configuration before removing credentials.
            require_idle(cfg)
            backup = r.RUN / 'supervision-acceptance' / args.environment / 'original-service.json'
            original = r.read(backup)
            credential = Path(cfg['server']['HOME']) / 'supervision-acceptance-credential.json'
            if cfg['server'].get('MULTICA_AGENTLENS_CREDENTIAL_FILE') != str(credential):
                raise RuntimeError('Temporary credential ownership differs; refuse cleanup')
            current = api(meta, profile, 'GET', 'supervision/')['config']
            api(meta, profile, 'PUT', 'supervision/', dict(enabled=False, investigation_enabled=False,
                interval_seconds=current['interval_seconds']))
            restart_backend(r, args.environment, meta, original)
            settings_path = backup.with_name('original-settings.json')
            if settings_path.exists():
                settings = r.read(settings_path)
                restored = {k: settings[k] for k in ('enabled', 'investigation_enabled', 'interval_seconds', 'instructions') if k in settings}
                restored.update({k: settings[k] for k in ('runtime_id', 'model', 'thinking_level') if settings.get(k) is not None})
                api(meta, profile, 'PUT', 'supervision/', restored)
                settings_path.unlink()
            credential.unlink()
            backup.unlink()
            print(json.dumps(dict(environment=args.environment, candidate=args.candidate, mode='cleaned')))
            return
        me = api(meta, profile, 'GET', 'me')
        runtimes = api(meta, profile, 'GET', 'runtimes')
        policy = load('supervision_policy', Path(__file__).with_name('test-agent-policy.py'))
        runtime = next(x for x in runtimes if x['id'] == args.runtime)
        fields = policy.agent_fields(runtimes, runtime['daemon_id'], environ={'MULTICA_RELEASE_ROOT': str(r.ROOT)})
        if fields['runtime_id'] != args.runtime or runtime.get('owner_id') != me['id']:
            raise RuntimeError('Selected runtime is not the test account owned policy runtime')
        report = dict(environment=args.environment, candidate=args.candidate, delivery=args.delivery,
                      commit=meta['commit'], runtime_id=args.runtime, mode='preflight')
        if not args.apply:
            print(json.dumps(report)); return
        if not args.credential_file:
            raise RuntimeError('--apply requires a scoped 0600 --credential-file')
        guard()
        backup = configure_service(r, args.environment, meta, cfg, profile['workspace_id'], args.credential_file)

        # 7. Use the official managed-agent setting and preserve custom instructions and intervals.
        before = api(meta, profile, 'GET', 'supervision/')
        config = before.get('config', before)
        r.write(backup.with_name('original-settings.json'), config, True)
        guard()
        result = api(meta, profile, 'PUT', 'supervision/', dict(
            **{k: v for k, v in fields.items() if k != 'service_tier'}, enabled=False,
            investigation_enabled=True, interval_seconds=config.get('interval_seconds', 300)))
        saved = result.get('config', result)
        if saved.get('investigation_enabled') is not True or not saved.get('agent_id'):
            raise RuntimeError('Managed supervision settings were not persisted')

        # 8. Import the pinned candidate skill additively; never overwrite an existing custom skill.
        source = Path(candidate['source']) / 'server/internal/service/builtin_skills/delivery-supervision'
        content = (source / 'SKILL.md').read_text()
        digest = hashlib.sha256(content.encode()).hexdigest()
        name = 'delivery-supervision'
        skills = api(meta, profile, 'GET', 'skills')
        existing = next((x for x in skills if x['name'] == name), None)
        if existing:
            skill = api(meta, profile, 'GET', 'skills/' + existing['id'])
            if skill.get('content') != content:
                raise RuntimeError('Existing test skill content differs from candidate')
        else:
            guard()
            skill = api(meta, profile, 'POST', 'skills', dict(name=name, description='Pinned supervision acceptance skill',
                content=content, files=[dict(path=str(p.relative_to(source)), content=p.read_text())
                    for p in sorted(source.rglob('*')) if p.is_file() and p.name != 'SKILL.md']))
        guard()
        api(meta, profile, 'POST', 'agents/' + saved['agent_id'] + '/skills/add', {'skill_ids': [skill['id']]})
        guard()
        enabled = api(meta, profile, 'PUT', 'supervision/', dict(enabled=True, investigation_enabled=True,
            interval_seconds=config.get('interval_seconds', 300)))['config']
        if (enabled.get('enabled') is not True or enabled.get('runtime_id') != fields['runtime_id']
                or enabled.get('model') != fields['model']):
            raise RuntimeError('Effective managed-agent settings differ from test policy')
        report.update(mode='settings_prepared', agent_id=saved['agent_id'], skill_id=skill['id'], skill_sha256=digest,
                      service_backup=str(backup), remaining=['real model execution and loaded skill fingerprint',
                                 'CodeBuddy/Codex business matrix', 'final int integration'])
        target = r.RUN / 'delivery-evidence' / (args.candidate + '-supervision-setup.json')
        r.write(target, report, True)
        print(json.dumps(report))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('environment', 'issue', 'delivery', 'candidate', 'runtime'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--credential-file', type=Path)
    parser.add_argument('--cleanup', action='store_true')
    try:
        prepare(parser.parse_args())
    except Exception as error:
        raise SystemExit(str(error))
