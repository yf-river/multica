exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import time
end=time.monotonic()+50
while True:
 active=False
 for n in ['child','cancel']:
  i=json.load(open(D/(n+'-issue.json')));runs=api('GET','issues/'+i['id']+'/task-runs');save(n+'-runs',runs);active |= any(x['status'] not in ['completed','failed','cancelled'] for x in runs)
 if not active or time.monotonic()>end:break
 time.sleep(3)
print({n:[(r['id'],r['status']) for r in json.load(open(D/(n+'-runs.json')))] for n in ['child','cancel']})
if not active:
 for n in ['child','cancel']:
  i=json.load(open(D/(n+'-issue.json')));save(n+'-check',api('POST','supervision/check',dict(issue_id=i['id'])))
 ag=json.load(open(D/'agent-pre-waste.json'));api('PUT','agents/'+ag['id'],dict(instructions=ag['instructions']));print('agent instructions restored')
