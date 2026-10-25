exec(open('.gong86-int51/common.py').read())
import hashlib
st,d,c,m,p,cfg=guard()
# Restore only this run's test settings after all sample/model executions finish.
a.require_idle(cfg)
for route,file in [('supervision/','settings-before.json'),('review/settings','review-before.json')]:
 before=json.load(open(D/file));before=before.get('config',before)
 fields={k:before[k] for k in ['enabled','investigation_enabled','interval_seconds','runtime_id','model','thinking_level','instructions'] if k in before and before[k] is not None}
 if route=='review/settings':fields.pop('interval_seconds',None);fields.pop('investigation_enabled',None)
 save('restored-'+file.removesuffix('.json'),api('PUT',route,fields))
agent=json.load(open(D/'agent.json'));save('archived-agent',api('POST','agents/'+agent['id']+'/archive',{}))
with r.lock('publisher'),r.lock('int'):
 guard();a.require_idle(cfg)
 backup=ROOT/'.run/supervision-acceptance/int/original-service.json';original=r.read(backup);original['server'].pop('MULTICA_SERVICE_USER',None)
 target=pathlib.Path(cfg['server']['MULTICA_AGENTLENS_CREDENTIAL_FILE'])
 assert target.name=='supervision-acceptance-credential.json'
 a.restart_backend(r,'int',m,original);target.unlink();backup.unlink()
save('cleanup',dict(settings_restored=True,agent_archived=True,credential_removed=True,original_service_restored=True))
print('restored settings and service; archived test agent; removed private credential')
