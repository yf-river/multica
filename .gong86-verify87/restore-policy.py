exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
agent=json.load(open(D/'agent-before.json'));before=json.load(open(D/'control-before.json'));row=next(x for x in control('GET','test-agent-control') if x['name']=='verify-1');assert before['desired']['policy']==row['desired']['policy']
prior=before['desired'].get('overrides',{});current=row['desired'].get('overrides',{});assert {k:v for k,v in current.items() if k!=agent['id']}==prior
policy=prior.get(agent['id'],before['desired']['policy'])
control('POST','test-agent-control',dict(action='configure',name='verify-1',agent_id=agent['id'],revision=row['revision'],policy=policy));row=next(x for x in control('GET','test-agent-control') if x['name']=='verify-1')
scope=json.load(open(D/'scope.json'));scope.update(operation_id=str(uuid.uuid4()),revision=row['revision']);save('restore-scope',scope)
subprocess.run(['python3','/data/ida/multica/.run/publisher/delivery-int-36fe86d0111339c2/test-agent-models.py','--environment','verify-1','--apply','--scope-file',str((D/'restore-scope.json').resolve())],env={**os.environ,'MULTICA_RELEASE_ROOT':'/data/ida/multica'},stdout=open(D/'target-restore.json','w'),check=True)
restored=api('GET','agents/'+agent['id']);save('agent-restored',restored)
assert restored['runtime_id']==agent['runtime_id'] and restored['model']==agent['model']
audit=json.loads(subprocess.check_output(['python3','/data/ida/multica/.run/publisher/delivery-int-36fe86d0111339c2/test-agent-models.py','--environment','verify-1'],env={**os.environ,'MULTICA_RELEASE_ROOT':'/data/ida/multica'}));save('audit-after',audit)
old=json.load(open(D/'audit-before.json'));fields=['runtime_id','model','thinking_level','service_tier'];new={x['id']:x for x in audit['after']}
for x in old['before']:
 if x['id']!=agent['id']:assert all(x.get(k)==new[x['id']].get(k) for k in fields)
save('policy-restore-proof',dict(target=agent['id'],restored=True,other_agents_unchanged=True,environment_policy_unchanged=True,retained_archived_agent_override=policy))
print('target policy restored; other Agent model settings and environment policy unchanged')
