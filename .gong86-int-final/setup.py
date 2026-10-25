exec(open('.gong86-int-final/common.py').read())
import re
st,d,c,m,p,cfg=guard()
policy=load('policy','scripts/test-agent-policy.py');runtimes=api('GET','runtimes');me=api('GET','me');owned=[x for x in runtimes if x['status']=='online' and x.get('owner_id')==me['id'] and not x.get('profile_id')];cb=next(x for x in owned if x['provider']=='codebuddy');fields=policy.agent_fields(runtimes,cb['daemon_id'],environ={'MULTICA_RELEASE_ROOT':str(ROOT)})
credential=D/'credential.json'
text=pathlib.Path('/data/cbs/worktree/prod/gongzuo-8d670ff37c61/gong-86-245b542b50e8/codex-home/skills/ida-development/references/codebuddy-token.md').read_text();key=re.search(r'```text\n([a-f0-9]{32})\n```',text)[1]
credential.write_text(json.dumps(dict(token=key,workspace_id=p['workspace_id'])));credential.chmod(0o600)
with r.lock('publisher'),r.lock('int'):
 guard();a.require_idle(cfg)
 private=ROOT/'.run/supervision-acceptance/int';private.mkdir(parents=True,exist_ok=True,mode=0o700);backup=private/'original-service.json';assert not backup.exists()
 target=pathlib.Path(cfg['server']['HOME'])/'supervision-acceptance-credential.json';assert not target.exists()
 r.write(backup,cfg,True);r.write(target,json.loads(credential.read_text()),True)
 uid=pathlib.Path('/proc/'+str(m['pids']['server'])).stat().st_uid;os.chown(target,uid,-1)
 updated=json.loads(json.dumps(cfg));updated['server'].update(MULTICA_AGENTLENS_CREDENTIAL_FILE=str(target),MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE=datetime.datetime.now(datetime.timezone.utc).isoformat())
 a.restart_backend(r,'int',m,updated)
before=api('GET','supervision/');config=before.get('config',before);save('settings-before',config)
result=api('PUT','supervision/',dict(**{k:v for k,v in fields.items() if k!='service_tier'},enabled=False,investigation_enabled=True,interval_seconds=config.get('interval_seconds',300)));saved=result.get('config',result)
source,content=a.candidate_skill(c);skills=api('GET','skills');existing=next((x for x in skills if x['name']=='delivery-supervision'),None)
if existing:
 skill=api('GET','skills/'+existing['id']);assert skill['content']==content
else:
 skill=api('POST','skills',dict(name='delivery-supervision',description='Pinned G86 int supervision acceptance',content=content,files=[dict(path=str(x.relative_to(source)),content=x.read_text()) for x in sorted(source.rglob('*')) if x.is_file() and x.name!='SKILL.md']))
api('POST','agents/'+saved['agent_id']+'/skills/add',dict(skill_ids=[skill['id']]))
# The user explicitly requires CodeBuddy samples; create a dedicated Flash agent without changing existing agents.
agent=api('POST','agents',dict(name='G86 int CodeBuddy acceptance',runtime_id=cb['id'],model='deepseek-v4.1-flash',thinking_level='',service_tier='',instructions='Scoped integration test: obey the issue. Do not create issues or delegate.'))
save('agent',agent);save('settings-configured',api('PUT','supervision/',dict(enabled=True,investigation_enabled=True,interval_seconds=config.get('interval_seconds',300))))
print('int supervision configured; dedicated CodeBuddy sample agent',agent['id'])
