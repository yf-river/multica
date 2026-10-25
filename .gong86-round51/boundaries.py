import sys
sys.path.insert(0,'.gong86-current');from round51 import *
from urllib import request,error
s=json.load(open(D/'codebuddy-subject.json'));csub=json.load(open(D/'codex-subject.json'));tid=s['trace_materials'][0]['id'];batch=csub['batches'][0]['id']
cases=[('negative-offset','supervision/evidence/trace_usage/'+tid+'?offset=-1',None),('invalid-id','supervision/evidence/trace_usage/not-a-uuid',None),('other-task-batch','supervision/evidence/trace_usage/'+tid+'?batch='+batch,None)]
out={}
for name,route,_ in cases:
 req=request.Request(m['backend_url']+'/api/'+route,headers={'Authorization':'Bearer '+p['token'],'X-Workspace-ID':p['workspace_id']})
 try:
  with request.build_opener(request.ProxyHandler({})).open(req,timeout=15) as res:out[name]=dict(status=res.status,body=json.load(res))
 except error.HTTPError as e:out[name]=dict(status=e.code,body=e.read().decode())
save('boundaries',out);print({k: {'status':v['status'],'deleted':v.get('body',{}).get('deleted') if isinstance(v.get('body'),dict) else None} for k,v in out.items()})
