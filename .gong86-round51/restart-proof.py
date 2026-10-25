import sys,datetime
sys.path.insert(0,'.gong86-current');from round51 import *
r=a.load('release_restart',pathlib.Path('scripts/release-environments.py'))
with r.lock(r.candidate_lock('verify-1')),r.lock('verify-1'):
 meta=r.read(r.meta_path('verify-1'));cfg=r.read(r.config_path('verify-1'));candidate=r.candidate(env='verify-1');scope=json.load(open(D/'scope.json'))
 status=control('GET','issues/01a17118-80ef-717e-b9d0-9059a78db295/deliveries');owner=status['owner']
 assert owner['execution_id']==os.environ['MULTICA_TASK_ID'] and datetime.datetime.fromisoformat(owner['expires_at'])>datetime.datetime.now(datetime.timezone.utc)
 assert candidate['id']==scope['candidate_id'] and candidate['status']=='verifying';r.validate_candidate(candidate);r.check_process_ownership('verify-1',meta);a.require_idle(cfg)
 old=meta['pids']['server'];a.restart_backend(r,'verify-1',meta,cfg)
 save('restart',dict(before_pid=old,after_pid=meta['pids']['server'],candidate=candidate['id'],commit=meta['commit'],health='ready'))
proof={}
for name,before in json.load(open(D/'immutable-before-restart.json')).items():
 tid=name.split('-pages-')[1][:-5];page=api('GET','supervision/evidence/trace_usage/'+tid+'?offset=0');assert page['sha256']==before['sha256'] and page['generation']==before['generation'] and page['length']==before['length']
 proof[tid]=dict(sha256=page['sha256'],generation=page['generation'],unchanged=True)
save('immutable-after-restart',proof);print('idle backend restart ready; immutable historical material retained',len(proof))
