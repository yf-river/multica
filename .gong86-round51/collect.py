import sys
sys.path.insert(0,'.gong86-current');from round51 import *
for n in ['codebuddy','codex','child','cancel','waste']:
 if not (D/(n+'-issue.json')).exists():continue
 i=json.load(open(D/(n+'-issue.json')));runs=api('GET','issues/'+i['id']+'/task-runs');save(n+'-runs',runs)
 print(n,[(x['id'],x['status'],(x.get('result') or {}).get('session_id')) for x in runs])
 for x in runs:
  save(n+'-messages-'+x['id'],api('GET','tasks/'+x['id']+'/messages'))
 s=api('GET','supervision/subjects/'+i['id']);save(n+'-subject',s)
 print('materials',[(x['status'],x['id']) for x in s.get('trace_materials',[])],'batches',[(x['status'],x.get('error')) for x in s.get('batches',[])])
