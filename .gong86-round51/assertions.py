import sys,hashlib
sys.path.insert(0,'.gong86-current');from round51 import *
proof={};runs={}
for n in ['codebuddy','codex','child','cancel','waste']:
 rs=api('GET','issues/'+json.load(open(D/(n+'-issue.json')))['id']+'/task-runs');save(n+'-runs',rs);runs[n]=rs
 assert all(t['status'] in ('completed','cancelled') for t in rs)
 proof[n]=[dict(id=t['id'],status=t['status'],runtime_id=t['runtime_id'],session_id=(t.get('result') or {}).get('session_id'),usage=t.get('usage'),traces=t.get('traces')) for t in rs]
cb=sorted([t for t in runs['codebuddy'] if t['runtime_id']=='ceb1f316-b39c-4b0e-ba86-8986b3c79b46'],key=lambda x:x['created_at'])
assert len(cb)==2 and cb[0]['result']['session_id']==cb[1]['result']['session_id']
assert any(t['runtime_id']=='4f8f9494-b626-4542-951d-8c0217e18a10' for t in runs['codebuddy'])
assert [t['status'] for t in runs['cancel']]==['completed','cancelled']
assert all(t['runtime_id']=='ceb1f316-b39c-4b0e-ba86-8986b3c79b46' for t in runs['cancel'])
proof['same_session_resume']=True;proof['cancelled_during_sleep']=True
save('runtime-and-session-proof',proof)
logs=(D/'fixture-supervision-tests.log').read_text();import re
save('test-counts',dict(passed=len(re.findall(r'^--- PASS:',logs,re.M)),skipped=re.findall(r'^--- SKIP: (\S+)',logs,re.M)));assert re.search(r'^PASS$',logs,re.M)
print('runtime/session/cancel assertions passed; fixture test counts saved')
