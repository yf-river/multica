import sys
sys.path.insert(0,'.gong86-current');from round51 import *
for n in ['codebuddy','codex','child','cancel','waste']:
 if not (D/(n+'-issue.json')).exists():continue
 i=json.load(open(D/(n+'-issue.json')));runs=api('GET','issues/'+i['id']+'/task-runs')
 if runs and all(x['status'] in ('completed','failed','cancelled') for x in runs):
  if not (D/(n+'-check.json')).exists():save(n+'-check',api('POST','supervision/check',dict(issue_id=i['id'],full=True)));print(n,'check submitted')
