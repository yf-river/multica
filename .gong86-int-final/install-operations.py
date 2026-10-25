import json,subprocess,pathlib,hashlib,os,importlib.util
root=pathlib.Path('/data/ida/multica');commit=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip();dest=root/'.run/publisher'/('gong86-ops-input-'+commit);dest.mkdir(exist_ok=True)
def blob(n):return subprocess.check_output(['git','show',commit+':scripts/'+n])
manifest=blob('delivery-operations.json');names=json.loads(manifest)['files']+['delivery-operations.json']
for name in names:
 p=dest/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(blob(name));assert p.read_bytes()==blob(name)
os.environ['MULTICA_RELEASE_ROOT']=str(root)
s=importlib.util.spec_from_file_location('r',dest/'release-environments.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)
for env in ['int','prod']:
 with r.lock('delivery-worker-'+env),r.lock('publisher'):
  state=subprocess.check_output(['systemctl','show','multica-delivery-'+env+'.service','-p','ActiveState','--value'],text=True).strip();assert state=='inactive',state
  subprocess.run(['python3',str(dest/'issue-delivery-worker.py'),'--env',env,'--install'],check=True)
print(json.dumps(dict(commit=commit,input=str(dest),files={n:hashlib.sha256(blob(n)).hexdigest() for n in names})))
