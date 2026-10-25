exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import time
issue=json.load(open(D/'cancel-issue.json'))
end=time.monotonic()+40
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs');run=runs[0]
 if run['status']=='running':break
 if time.monotonic()>end:raise SystemExit('cancel sample not running yet')
 time.sleep(2)
save('cancel-running',run);save('cancel-receipt',api('POST','issues/'+issue['id']+'/tasks/'+run['id']+'/cancel',{}))
end=time.monotonic()+12
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs')
 if runs[0]['status']=='cancelled':break
 if time.monotonic()>end:raise SystemExit('cancellation not settled')
 time.sleep(1)
save('cancelled-runs',runs);save('retry-receipt',api('POST','issues/'+issue['id']+'/rerun',dict(task_id=run['id'])));print('started run cancelled and retry accepted',run['id'])
