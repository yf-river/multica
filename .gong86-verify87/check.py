exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
for n in ['codebuddy','codex']:
 i=json.load(open(D/(n+'-issue.json')));r=api('GET','issues/'+i['id']+'/task-runs');save(n+'-runs',r)
 print(n,[(x['id'],x['status']) for x in r])
 for x in r:save(n+'-messages',api('GET','tasks/'+x['id']+'/messages'))
 if all(x['status']=='completed' for x in r):
  try:save(n+'-check',api('POST','supervision/check',dict(issue_id=i['id'],full=True)))
  except Exception as e:print(n,str(e))
