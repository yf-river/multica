import sys,time
sys.path.insert(0,'.gong86-current');from round51 import *
i=json.load(open(D/'cancel-issue.json'));end=time.monotonic()+50
while True:
 runs=api('GET','issues/'+i['id']+'/task-runs');run=runs[0]
 if run['status']=='running':
  msgs=api('GET','tasks/'+run['id']+'/messages')
  if any(x.get('type')=='tool_use' and 'sleep' in json.dumps(x) for x in msgs):break
 if run['status'] in ('failed','completed','cancelled'):raise SystemExit('sample ended before cancel')
 if time.monotonic()>end:raise SystemExit('not yet sleep; retry after inspect')
 time.sleep(2)
save('cancel-running',run);save('cancel-running-messages',msgs)
save('cancel-receipt',api('POST','issues/'+i['id']+'/tasks/'+run['id']+'/cancel',{}));print('cancelled running CodeBuddy',run['id'])
