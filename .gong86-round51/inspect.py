import sys
sys.path.insert(0,'.gong86-current');from round51 import *
for n in ['codebuddy','codex']:
 i=json.load(open(D/(n+'-issue.json')));runs=api('GET','issues/'+i['id']+'/task-runs');save(n+'-runs',runs)
 for t in runs:
  msgs=api('GET','tasks/'+t['id']+'/messages');save(n+'-messages',msgs)
  print(n,t['id'],t['status'],t.get('session_id'),[(x.get('type'),str(x.get('content',''))[-350:]) for x in msgs[-3:]])
