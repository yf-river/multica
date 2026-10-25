import importlib.util,os,subprocess,json,hashlib
from pathlib import Path
commit='5b57c4623'
root=Path('/data/ida/multica');run=root/'.run';evidence=Path('.gong86-int-fix').resolve()
get=lambda path:subprocess.check_output(['git','show',commit+':scripts/'+path])
manifest=json.loads(get('delivery-operations.json'));names=['delivery-operations.json',*manifest['files']]
assert len(names)==len(set(names))
stage=run/'publisher'/('ops-input-'+commit);stage.mkdir(exist_ok=True)
for name in names:
 p=stage/name;p.parent.mkdir(parents=True,exist_ok=True);data=get(name)
 if p.exists():assert p.read_bytes()==data
 else:p.write_bytes(data)
assert all((stage/name).read_bytes()==get(name) for name in names)
os.environ['MULTICA_RELEASE_ROOT']=str(root)
spec=importlib.util.spec_from_file_location('release',stage/'release-environments.py');r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
receipts=[]
for env in ('int','prod'):
 with r.lock('delivery-worker-'+env),r.lock('publisher'):
  units=subprocess.check_output(['systemctl','list-units','--state=active,activating,deactivating','--no-legend','--plain','multica-verify-*'],text=True)
  assert not units.strip(),'active verification worker'
  old=subprocess.check_output(['systemctl','show','multica-delivery-'+env+'.service','-p','ExecStart','-p','ActiveState','-p','MainPID'],text=True)
  assert 'MainPID=0' in old and 'ActiveState=inactive' in old,old
  subprocess.run(['python3',str(stage/'issue-delivery-worker.py'),'--env',env,'--install'],check=True)
  digest=hashlib.sha256(b''.join((stage/name).read_bytes() for name in names)).hexdigest()[:16]
  target=run/'publisher'/('delivery-'+env+'-'+digest)
  assert all((target/name).read_bytes()==get(name) for name in names)
  new=subprocess.check_output(['systemctl','show','multica-delivery-'+env+'.service','-p','ExecStart','-p','ActiveState','-p','MainPID'],text=True)
  assert str(target/'issue-delivery-worker.py') in new
  receipts.append(dict(environment=env,source_commit=commit,files=len(names),fingerprint=digest,old_consumer=old,new_consumer=new,files_sha256={name:hashlib.sha256(get(name)).hexdigest() for name in names}))
 if env=='int':
  audit=subprocess.run(['python3',str(target/'test-agent-models.py'),'--environment','int'],capture_output=True,text=True,env={**os.environ,'MULTICA_RELEASE_ROOT':str(root)},timeout=40)
  assert audit.returncode==0,'int installed inventory consumer failed: '+r.redact_failure(audit.stderr)[-1200:]
  inventory=json.loads(audit.stdout.strip().splitlines()[-1])
  (evidence/'installed-int-audit.json').write_text(json.dumps(dict(environment=inventory['environment'],mode=inventory['mode'],agents=inventory['agents'],busy=inventory['busy'],daemon_id=inventory['daemon_id']),indent=2))
(evidence/'ops-install.json').write_text(json.dumps(receipts,indent=2))
print(json.dumps([dict(environment=x['environment'],fingerprint=x['fingerprint'],files=x['files']) for x in receipts]))
