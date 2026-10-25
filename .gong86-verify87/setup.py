import sys,json,pathlib,subprocess,os,uuid
sys.path.insert(0,'.gong86-current');from common import api,a,m,p
D=pathlib.Path('.gong86-verify87')
def save(n,d): (D/(n+'.json')).write_text(json.dumps(d,ensure_ascii=False,indent=2))
c=json.load(open('/data/ida/multica/.run/deployments/goal-test-prod.json'));cp=json.load(open(c['daemon_profile_path']))
def control(method,route,body=None):return a.api(c,cp,method,route,body)
agent=json.load(open(D/'agent-before.json'))
save('agent-before',agent)
audit=json.loads(subprocess.check_output(['python3','/data/ida/multica/.run/publisher/delivery-int-36fe86d0111339c2/test-agent-models.py','--environment','verify-1'],env={**os.environ,'MULTICA_RELEASE_ROOT':'/data/ida/multica'}));save('audit-before',audit)
row=next(x for x in control('GET','test-agent-control') if x['name']=='verify-1');save('control-before',row)
observed=row['observed'];observed['agents']=[{k:x.get(k) for k in ['id','name','runtime_id','model','thinking_level','service_tier','status','archived_at']} for x in audit['after']]
control('POST','test-agent-control',dict(action='report',name='verify-1',observed=observed))
control('POST','test-agent-control',dict(action='configure',name='verify-1',agent_id=agent['id'],revision=row['revision'],policy=dict(provider='codebuddy',model='deepseek-v4.1-flash',thinking_level='',service_tier='')))
row=next(x for x in control('GET','test-agent-control') if x['name']=='verify-1');save('control-configured',row)
status=control('GET','issues/01a17118-80ef-717e-b9d0-9059a78db295/deliveries');candidate=json.load(open('/data/ida/multica/.run/candidates/verify-1/current.json'));seed=json.load(open('/data/ida/multica/.run/verification-install/verify-1.json'))
scope=dict(environment='verify-1',delivery_id=candidate['delivery_id'],candidate_id=candidate['id'],source_commit=m['commit'],execution_id=os.environ['MULTICA_TASK_ID'],owner_generation=status['owner']['generation'],generation=candidate['generation'],workspace_id=p['workspace_id'],agent_id=agent['id'],revision=row['revision'],seed_id=seed['seed_id'],seed_sha256=seed['seed_sha256'],operation_id=str(uuid.uuid4()))
save('scope',scope)
print(json.dumps(dict(agent_id=agent['id'],revision=row['revision'],generation=scope['generation'])))
