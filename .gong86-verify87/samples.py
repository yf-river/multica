exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
agent=api('GET','agents/'+json.load(open(D/'agent-before.json'))['id']);save('agent-effective',agent)
assert agent['model']=='deepseek-v4.1-flash' and agent['runtime_id']=='ceb1f316-b39c-4b0e-ba86-8986b3c79b46'
for name,aid in [('codebuddy',agent['id']),('codex','3574804a-a55e-4a2a-92ae-85eb94dae8ec')]:
 issue=api('POST','issues',dict(title='G86-87 '+name+'真实轨迹样本',description='隔离验收：仅回答17+25的结果。不修改文件，不创建任务，不调用其他Agent。允许平台要求的读取工单。',assignee_type='agent',assignee_id=aid));save(name+'-issue',issue)
 print(name,issue['id'])
