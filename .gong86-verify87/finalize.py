exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import re,hashlib,zipfile
agent=json.load(open(D/'agent-before.json'));save('archived-agent',api('POST','agents/'+agent['id']+'/archive',{}))
config=api('GET','supervision/')['config'];assert config['enabled'] is False and config['investigation_enabled'] is False;save('restored-supervision-settings',config)
credential=json.load(open(D/'credential.json'))['token'];meta=json.load(open('/data/ida/multica/.run/deployments/goal-test-verify-1.json'));cfg=json.load(open('/data/ida/multica/.run/env/verify-1-services.json'));assert 'MULTICA_AGENTLENS_CREDENTIAL_FILE' not in cfg['server'] and not (pathlib.Path(cfg['server']['HOME'])/'supervision-acceptance-credential.json').exists()
secrets_to_check=[credential,p['token'],cp['token']]
scanned=0
for f in D.iterdir():
 if f.suffix not in ['.json','.log','.txt','.md','.py','.cjs'] or f.name=='credential.json':continue
 text=f.read_text(errors='replace');assert not any(v and v in text for v in secrets_to_check),f.name;scanned+=1
(D/'credential.json').unlink();save('redaction-and-cleanup',dict(files_scanned=scanned,credential_matches=0,source_credential_removed=True,service_credential_removed=True,service_restored=True,test_agent_archived=True,managed_supervisor_retained_disabled=True))
summary={}
for n in ['codebuddy','codex','waste','child','cancel']:
 s=json.load(open(D/(n+'-subject.json')));assert all(b['status']=='completed' for b in s['batches']);summary[n]=dict(issue_id=s['subject']['issue_id'],coverage=s['coverage'],subject=s['subject'],supervisor_usage=s['supervisor_usage'],trace_materials=s['trace_materials'])
save('acceptance-summary',summary)
logs={}
for name in ['fixture-supervision-tests','fixed-material-pure-tests','execution-evidence-tests']:
 text=(D/(name+'.log')).read_text();logs[name]=dict(passed=len(re.findall(r'^--- PASS:',text,re.M)),skipped=re.findall(r'^--- SKIP: (\S+)',text,re.M));assert re.search(r'^PASS$',text,re.M)
save('test-counts',logs);print(json.dumps(logs,ensure_ascii=False));print('all owned runtime samples and supervision batches terminal; credentials removed; test Agent archived')
