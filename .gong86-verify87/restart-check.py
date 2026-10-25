exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
r=a.load('release_restart',pathlib.Path('scripts/release-environments.py'))
with r.lock(r.candidate_lock('verify-1')),r.lock('verify-1'):
 meta=r.read(r.meta_path('verify-1'));cfg=r.read(r.config_path('verify-1'));candidate=r.candidate(env='verify-1');assert candidate['id']==json.load(open(D/'scope.json'))['candidate_id'] and candidate['status']=='verifying';r.validate_candidate(candidate);r.check_process_ownership('verify-1',meta);a.require_idle(cfg)
 old=meta['pids']['server'];a.restart_backend(r,'verify-1',meta,cfg);report=dict(before_pid=old,after_pid=meta['pids']['server'],candidate=candidate['id'],commit=meta['commit'],health='ready')
 save('restart',report);print(report)
