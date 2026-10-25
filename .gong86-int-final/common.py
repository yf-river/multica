import json,pathlib,importlib.util,os,datetime
D=pathlib.Path('.gong86-int-final');ROOT=pathlib.Path('/data/ida/multica');os.environ['MULTICA_RELEASE_ROOT']=str(ROOT)
def load(name,path):
 s=importlib.util.spec_from_file_location(name,path);a=importlib.util.module_from_spec(s);s.loader.exec_module(a);return a
a=load('a','scripts/prepare-supervision-acceptance.py');r=load('r','scripts/release-environments.py')
def save(n,x): (D/(n+'.json')).write_text(json.dumps(x,ensure_ascii=False,indent=2))
def env(n):
 m=json.loads((ROOT/'.run/deployments'/('goal-test-'+n+'.json')).read_text());return m,json.load(open(m['daemon_profile_path']))
def api(method,path,body=None):
 m,p=env('int');return a.api(m,p,method,path,body)
def control(method,path,body=None):
 m,p=env('prod');return a.api(m,p,method,path,body)
ISSUE='01a17118-80ef-717e-b9d0-9059a78db295';DELIVERY='01a171e2-116d-79e6-bf67-5cb4d087625b'
def guard():
 st=control('GET','issues/'+ISSUE+'/deliveries');d=next(x for x in st['deliveries'] if x['id']==DELIVERY);c=r.candidate();m,p=env('int');lease=st['publication'];now=datetime.datetime.now(datetime.timezone.utc)
 assert d['phase']=='preparing_release' and d['plan_snapshot']['integration_only'] is True and d['approved_by'] is None
 assert c['status']=='verifying' and c['delivery_id']==DELIVERY and c['id']==d['candidate_id'] and c['commit']==m['commit'] and c['release_id']==m['release_id']
 assert lease['state']=='active' and lease['delivery_id']==DELIVERY and lease['generation']==c['publication_lease']['generation'] and datetime.datetime.fromisoformat(lease['expires_at'])>now
 assert st['owner']['execution_id']==os.environ['MULTICA_TASK_ID'] and datetime.datetime.fromisoformat(st['owner']['expires_at'])>now
 cfg=r.read(r.config_path('int'));r.check_process_ownership('int',m);assert r.process_env(m['pids']['server'])['DATABASE_URL']==cfg['server']['DATABASE_URL'];assert api('GET','config')['environment_id']=='int'
 save('identity',dict(commit=c['commit'],candidate=c['id'],source_commit=d['source_commit'],publication=lease,owner=st['owner'],environment='int'))
 return st,d,c,m,p,cfg
