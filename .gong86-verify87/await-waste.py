exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import time
issue=json.load(open(D/'waste-issue.json'));end=time.monotonic()+45
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs');save('waste-runs',runs)
 if runs and all(x['status'] in ['completed','failed','cancelled'] for x in runs):break
 if time.monotonic()>=end:raise SystemExit('sample still active; collect foreground before cleanup')
 time.sleep(3)
for r in runs:save('waste-messages',api('GET','tasks/'+r['id']+'/messages'))
print([(x['id'],x['status']) for x in runs]);print(api('POST','supervision/check',dict(issue_id=issue['id'])))
ag=json.load(open(D/'agent-pre-waste.json'));api('PUT','agents/'+ag['id'],dict(instructions=ag['instructions']));print('temporary instructions restored')
