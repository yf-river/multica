exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import hashlib
out=[]
content=pathlib.Path('skills/delivery-supervision/SKILL.md').read_text()
for n in ['codebuddy','codex','waste','child','cancel']:
 s=api('GET','supervision/subjects/'+json.load(open(D/(n+'-issue.json')))['id']);save(n+'-subject',s)
 for b in s['batches']:
  task=api('GET','supervision/batches/'+b['id']+'/task');save('supervisor-'+b['task_id'],task)
  messages=api('GET','tasks/'+b['task_id']+'/messages');save('supervisor-messages-'+b['task_id'],messages)
  work=pathlib.Path(task.get('work_dir',''))/'AGENTS.md';loaded=work.is_file() and content in work.read_text()
  tools=[x.get('tool') for x in messages if x.get('type')=='tool_use']
  out.append(dict(batch=b['id'],status=b['status'],task=b['task_id'],skill_loaded=loaded,skill_sha256=hashlib.sha256(content.encode()).hexdigest(),tools=tools))
save('loaded-skills-and-tools',out);print(json.dumps(out,ensure_ascii=False))
