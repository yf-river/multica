import subprocess,os,secrets,json,pathlib,time
D=pathlib.Path('.gong86-round51');name='gong86-round51-fixture-db';password=secrets.token_hex(20)
env={**os.environ,'POSTGRES_PASSWORD':password}
result=subprocess.run(['docker','run','-d','--name',name,'--label','purpose=gong86-round51-fixed-fixtures','-e','POSTGRES_PASSWORD','-e','POSTGRES_USER=multica','-e','POSTGRES_DB=gong86_round51_fixture','-p','127.0.0.1::5432','postgres:17-alpine'],env=env,check=True,capture_output=True,text=True)
info=json.loads(subprocess.check_output(['docker','inspect',name]));port=info[0]['NetworkSettings']['Ports']['5432/tcp'][0]['HostPort'];dsn=f'postgres://multica:{password}@127.0.0.1:{port}/gong86_round51_fixture?sslmode=disable'
handle=dict(container=name,id=result.stdout.strip(),port=port,database='gong86_round51_fixture',purpose='new isolated fixture database');(D/'fixture-db-handle.json').write_text(json.dumps(handle))
env={**os.environ,'DATABASE_URL':dsn,'MULTICA_TEST_DATABASE_URL':dsn}
try:
 for _ in range(20):
  if subprocess.run(['docker','exec',name,'pg_isready','-U','multica','-d','gong86_round51_fixture'],capture_output=True).returncode==0:break
  time.sleep(.3)
 with open(D/'fixture-target-check.log','w') as f:subprocess.run(['python3','scripts/check-test-database.py'],env=env,stdout=f,stderr=subprocess.STDOUT,check=True)
 with open(D/'fixture-migrations.log','w') as f:subprocess.run(['go','run','./cmd/migrate','up'],cwd='server',env=env,stdout=f,stderr=subprocess.STDOUT,check=True)
 with open(D/'fixture-supervision-tests.log','w') as f:subprocess.run(['go','test','./internal/supervision','-count=1','-v'],cwd='server',env=env,stdout=f,stderr=subprocess.STDOUT,check=True)
 print('isolated candidate supervision suite passed')
finally:
 subprocess.run(['docker','rm','-f',name],check=True,capture_output=True)
 (D/'fixture-db-cleanup.json').write_text(json.dumps(dict(container=name,removed=True)))
