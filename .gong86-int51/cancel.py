exec(open('.gong86-int51/common.py').read())
import time
issue=json.load(open(D/'cancel-issue.json'));end=time.monotonic()+50
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs');run=runs[0];messages=api('GET','tasks/'+run['id']+'/messages')
 if run['status']=='running' and any(x.get('type')=='tool_use' and 'sleep' in json.dumps(x) for x in messages):break
 if time.monotonic()>end:save('cancel-precheck',dict(runs=runs,messages=messages));raise SystemExit('No observed running sleep yet; no cancellation issued')
 time.sleep(2)
save('cancel-running',run);save('cancel-started-messages',messages);save('cancel-receipt',api('POST','issues/'+issue['id']+'/tasks/'+run['id']+'/cancel',{}));print('cancelled observed CodeBuddy running sleep',run['id'])
