import subprocess,pathlib,json,os
ROOT=pathlib.Path('/data/ida/multica');SOURCE=pathlib.Path('/data/ida/worktrees/gong86-integration-only');commit=subprocess.check_output(['git','-C',SOURCE,'rev-parse','HEAD'],text=True).strip();dest=ROOT/'.run/publisher'/('gong86-recovery-input-'+commit);dest.mkdir(exist_ok=True)
def blob(n):return subprocess.check_output(['git','-C',SOURCE,'show',commit+':scripts/'+n])
for n in json.loads(blob('delivery-operations.json'))['files']+['delivery-operations.json']:
 p=dest/n;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(blob(n));assert p.read_bytes()==blob(n)
subprocess.run(['git','-C',ROOT,'fetch','origin','fix/gong86-integration-only'],check=True)
source=ROOT/'.run/delivery-sources'/('maintenance-'+commit)
if not source.exists():subprocess.run(['git','-C',ROOT,'worktree','add','--detach',source,commit],check=True)
P=pathlib.Path('/data/ida/artifacts/quality-token-supervision-20261025/int-recovery-20261025')
with (P/'maintenance-int.log').open('w') as f:
 result=subprocess.run(['python3',str(dest/'release-environments.py'),'maintenance-int','--source',str(source),'--evidence',str(P/'authorization.json')],cwd=ROOT,env={**os.environ,'MULTICA_RELEASE_ROOT':str(ROOT)},stdout=f,stderr=subprocess.STDOUT)
print('maintenance-int exit',result.returncode,'log',str(P/'maintenance-int.log'));raise SystemExit(result.returncode)
