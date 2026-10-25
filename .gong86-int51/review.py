exec(open('.gong86-int51/common.py').read())
st,d,c,m,p,cfg=guard();before=api('GET','review/settings');save('review-before',before)
fields=api('GET','supervision/')['config'];settings=api('PUT','review/settings',dict(enabled=False,runtime_id=fields['runtime_id'],model=fields['model'],thinking_level=fields.get('thinking_level','')));save('review-configured',settings)
source=pathlib.Path(c['source'])/'skills/delivery-review';content=(source/'SKILL.md').read_text();existing=next((x for x in api('GET','skills') if x['name']=='delivery-review'),None)
if existing:
 skill=api('GET','skills/'+existing['id']);assert skill['content']==content
else:
 skill=api('POST','skills',dict(name='delivery-review',description='Pinned review acceptance',content=content,files=[dict(path=str(x.relative_to(source)),content=x.read_text()) for x in sorted(source.rglob('*')) if x.is_file() and x.name!='SKILL.md']))
api('POST','agents/'+settings['agent_id']+'/skills/add',dict(skill_ids=[skill['id']]))
save('review-enabled',api('PUT','review/settings',dict(enabled=True)))
issue=api('POST','issues',dict(title='G86 int review regression: arithmetic',description='本任务用于真实审查回归。要求仅回答17+25=42，不修改代码、不创建任务。开发结果将在评论提供，按原delivery-review职责检查实际结果。',priority='not_important_not_urgent'));save('review-issue',issue)
body=D/'review-result.md';body.write_text('验收样本结果：17+25=42。未修改代码；计算已人工复核。仅验收环境样本，不包含生产发布。')
# Post a scoped evidence comment without triggering the business developer.
save('review-evidence-comment',api('POST','issues/'+issue['id']+'/comments',dict(content=body.read_text(),suppress_agent_triggers=True)))
save('review-started',api('PUT','issues/'+issue['id'],dict(status='in_review')))
print('real reviewer requested',issue['id'])
