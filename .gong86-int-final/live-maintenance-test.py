import json,urllib.request,urllib.error,uuid
from pathlib import Path
root=Path('/data/ida/multica/.run');m=json.loads((root/'deployments/goal-test-int.json').read_text());p=json.loads(Path(m['daemon_profile_path']).read_text())
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}));owner={};results=[]
def call(path,body=None,method=None,expected=200,owned=False):
 h={'Authorization':'Bearer '+p['token'],'X-Workspace-ID':p['workspace_id'],'Content-Type':'application/json'}
 if owned:h.update({'X-Delivery-Token':owner['token'],'X-Delivery-Generation':str(owner['generation'])})
 req=urllib.request.Request(m['backend_url']+'/api/'+path,data=None if body is None else json.dumps(body).encode(),headers=h,method=method)
 try:
  with opener.open(req,timeout=20) as f:status=f.status;raw=f.read()
 except urllib.error.HTTPError as e:status=e.code;raw=e.read()
 data=json.loads(raw) if raw else None
 if status!=expected:raise RuntimeError(str((path,status,data)))
 return data

issue=None;lease=None;delivery=None;worker=None
try:
 assert m['commit']=='269d38dbcee65e04440dd56af34412fce799798d'
 issue=call('issues',dict(title='G86 int-only controller acceptance fixture',description='Synthetic controller test only; not G86 business acceptance',priority='not_important_not_urgent'),expected=201)['id']
 owner.update(call('issues/'+issue+'/delivery-owner',dict(action='claim',execution_id='integration-control-api-proof')))
 c=call('issues/'+issue+'/comments',dict(content='Synthetic controller test: verification then int only, no production authorization.'),expected=201)
 plan=call('issues/'+issue+'/delivery-plan',dict(comment_id=c['id'],revision=c.get('revision',1)),method='PUT',owned=True)
 call('issues/'+issue+'/delivery-plan/confirm',dict(plan_id=plan['plan']['id'],revision=1))
 response=call('issues/'+issue+'/deliveries',dict(operation_id=str(uuid.uuid4()),source_commit=m['commit'],source_branch='feature/maintenance/gong86-int-only',base_commit='f0514bb259c0f7b79740d1757d194cdb7a40eefd',evidence=[dict(name='local tests',status='passed',evidence='integration-control/tests.log')],required_checks=[dict(name='synthetic verification',stage='verification'),dict(name='synthetic integration',stage='integration')]),owned=True)
 d=response['deliveries'][0];delivery=d['id']
 def current():return call('issues/'+issue+'/deliveries')['deliveries'][0]
 def advance(action,expected=200,**extra):
  d=current();return call('delivery-queue/advance',dict(id=delivery,revision=d['revision'],worker_token=worker or '',action=action,source_commit=m['commit'],publication_generation=lease['generation'] if lease else 0,**extra),expected=expected)
 worker=advance('claim')['worker_token']
 advance('prepared',candidate_id='synthetic-verify',candidate_url=m['frontend_url'])
 def accept(candidate,name,expected=200):
  return call('issues/'+issue+'/deliveries/'+delivery+'/action',dict(action='accept',revision=current()['revision'],candidate_id=candidate,evidence=[dict(name=name,status='passed',evidence='synthetic controller interface fixture')]),owned=True,expected=expected)
 accept('synthetic-verify','synthetic verification');advance('accepted')
 queue=call('delivery-queue?current='+delivery);assert next(x for x in queue if x['id']==delivery)['integration_pending'] is True
 assert call('issues/'+issue)['status']!='in_review'
 worker=advance('claim-integration')['worker_token'];advance('claim-integration',expected=409)
 lease=call('delivery-queue/publication',dict(action='claim',delivery_id=delivery,worker_token=worker))
 actual=json.loads(Path('/data/ida/multica/.run/int-candidate.json').read_text()) if False else None
 candidate='maintenance-269d38dbcee65e04440dd56af34412fce799798d'
 advance('prepared',candidate_id=candidate,candidate_url=m['frontend_url'])
 advance('fail',error='synthetic deployed int failure')
 call('delivery-queue/publication',dict(lease,action='release'));lease=None
 call('issues/'+issue+'/deliveries/'+delivery+'/action',dict(action='retest',revision=current()['revision'],candidate_id='wrong',evidence=[dict(name='recovery checked',status='passed',evidence='scoped fixture')]),owned=True,expected=409)
 call('issues/'+issue+'/deliveries/'+delivery+'/action',dict(action='retest',revision=current()['revision'],candidate_id=candidate,evidence=[dict(name='recovery checked',status='passed',evidence='scoped fixture')]),owned=True)
 restored=current();assert restored['phase']=='awaiting_review' and restored['approved_by'] is None and restored['plan_snapshot']['integration_failures'][0]['error']=='synthetic deployed int failure'
 worker=advance('claim-integration')['worker_token']
 lease=call('delivery-queue/publication',dict(action='claim',delivery_id=delivery,worker_token=worker))
 advance('prepared',candidate_id=candidate,candidate_url=m['frontend_url'])

 accept(candidate,'synthetic verification',expected=409)
 advance('integrated',expected=409,candidate_id=candidate)
 accept(candidate,'synthetic integration')
 advance('integrated',expected=409,candidate_id='stale-candidate')
 advance('integrated',candidate_id=candidate)
 d=current();assert d['phase']=='awaiting_review' and d['approved_by'] is None
 assert call('issues/'+issue)['status']=='in_review'
 status=call('issues/'+issue+'/deliveries');assert len(status['acceptances'])==2
 advance('publishing',expected=409,candidate_id=candidate)
 results=['deployed control candidate identity checked','verification does not enter review','formal queue exposes integration_pending','claim replay rejected','int lease claimed without production approval','verify evidence rejected for integration','missing or wrong int candidate rejected','distinct evidence histories preserved','int-only completion enters review without approval','unauthorized publishing rejected','deployed int failure restored through formal retest; wrong candidate rejected']
 output=dict(environment='int',commit=m['commit'],release_id=m['release_id'],fixture_issue_id=issue,checks=results,boundary='synthetic controller API lifecycle; not G86 business integration acceptance')
 Path('/data/ida/artifacts/quality-token-supervision-20261025/int-recovery-20261025/live-int-api.json').write_text(json.dumps(output,indent=2));print(json.dumps(output))
finally:
 if lease:call('delivery-queue/publication',dict(lease,action='release'))
 if issue:call('issues/'+issue,method='DELETE',expected=204)
