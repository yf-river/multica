import importlib.util,json,pathlib,hashlib
s=importlib.util.spec_from_file_location('a','scripts/prepare-supervision-acceptance.py');a=importlib.util.module_from_spec(s);s.loader.exec_module(a)
m=json.load(open('/data/ida/multica/.run/deployments/goal-test-verify-1.json'));p=json.load(open(m['daemon_profile_path']))
def api(method,path,body=None):return a.api(m,p,method,path,body)
source=pathlib.Path('/data/ida/multica/.run/delivery-sources/01a171b7-60b1-788d-99f3-1d47e1c7f7ea/skills/delivery-supervision/SKILL.md');content=source.read_text();skills=api('GET','skills');existing=next((x for x in skills if x['name']=='delivery-supervision'),None)
if existing:
 skill=api('GET','skills/'+existing['id']);assert skill['content']==content
else:skill=api('POST','skills',dict(name='delivery-supervision',description='Pinned candidate supervision acceptance',content=content))
cfg=api('GET','supervision/')['config'];api('POST','agents/'+cfg['agent_id']+'/skills/add',{'skill_ids':[skill['id']]})
cfg=api('PUT','supervision/',dict(enabled=True,investigation_enabled=True,interval_seconds=cfg['interval_seconds']))['config']
print(json.dumps({'config':cfg,'skill_id':skill['id'],'sha256':hashlib.sha256(content.encode()).hexdigest()}))
