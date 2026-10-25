exec(open('.gong86-int51/common.py').read())
import time,hashlib
end=time.monotonic()+55
while True:
 active=[]
 for n in ['resume','cancel']:
  i=json.load(open(D/(n+'-issue.json')));s=api('GET','supervision/subjects/'+i['id']);save(n+'-subject',s)
  for b in s.get('batches',[]):
   if b['status'] in ['preparing','running']:active.append(b['id'])
 if not active or time.monotonic()>end:break
 time.sleep(3)
for n in ['resume','cancel']:
 i=json.load(open(D/(n+'-issue.json')))
 runs=api('GET','issues/'+i['id']+'/task-runs');save(n+'-runs',runs)
 for run in runs:save(n+'-messages-'+run['id'],api('GET','tasks/'+run['id']+'/messages'))
 s=json.load(open(D/(n+'-subject.json')))
 for t in s.get('trace_materials',[]):
  if t['status'] in ['pending','processing']:continue
  offset=0;pages=[]
  while True:
   page=api('GET','supervision/evidence/trace_usage/'+t['id']+'?offset='+str(offset));assert page['offset']==offset and not page['deleted'];pages.append(page)
   if page['end']>=page['length']:break
   assert page['end']>offset;offset=page['end']
  assert len({(x['sha256'],x['generation']) for x in pages})==1
  save(n+'-pages-'+t['id'],pages);save(n+'-report-'+t['id'],json.loads(''.join(x['text'] for x in pages)))
 for b in s.get('batches',[]):
  if b.get('task_id'):
   task=api('GET','supervision/batches/'+b['id']+'/task');save('supervisor-'+b['task_id'],task);save('supervisor-messages-'+b['task_id'],api('GET','tasks/'+b['task_id']+'/messages'))
print('still active',active)
