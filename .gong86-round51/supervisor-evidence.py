import sys,hashlib
sys.path.insert(0,'.gong86-current');from round51 import *
out=[];content=pathlib.Path('skills/delivery-supervision/SKILL.md').read_text()
for n in ['codebuddy','codex','child','cancel','waste']:
 s=api('GET','supervision/subjects/'+json.load(open(D/(n+'-issue.json')))['id']);save(n+'-subject',s)
 for b in s['batches']:
  t=api('GET','supervision/batches/'+b['id']+'/task');save('supervisor-'+b['task_id'],t)
  msgs=api('GET','tasks/'+b['task_id']+'/messages');save('supervisor-messages-'+b['task_id'],msgs)
  f=pathlib.Path(t.get('work_dir',''))/'AGENTS.md';loaded=f.is_file() and content in f.read_text()
  out.append(dict(sample=n,status=b['status'],task=b['task_id'],task_status=t['status'],skill_loaded=loaded,skill_sha256=hashlib.sha256(content.encode()).hexdigest(),tools=[x.get('tool') for x in msgs if x.get('type')=='tool_use']))
save('loaded-skills-and-tools',out)
print(json.dumps([{k:v for k,v in x.items() if k not in ('tools','skill_sha256')} for x in out]))
