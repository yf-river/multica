exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import time
end=time.monotonic()+50
while True:
 summaries=[];active=False
 for n in ['codebuddy','codex','waste','child','cancel']:
  i=json.load(open(D/(n+'-issue.json')));s=api('GET','supervision/subjects/'+i['id']);save(n+'-subject',s)
  summaries.append((n,[(b['id'],b['status']) for b in s['batches']]))
  active |= any(b['status'] in ['preparing','running'] for b in s['batches'])
 if not active or time.monotonic()>end:print(summaries);break
 time.sleep(5)
