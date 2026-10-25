exec(open('.gong86-int-final/common.py').read())
guard();agent=json.load(open(D/'agent.json'))
for name,description in [('resume','隔离验收：记住校验词G86-session-7403。仅回答17+25的结果。不修改文件，不创建任务，不调用其他Agent。'),('cancel','隔离验收取消：必须先执行一次sleep 90，再仅回答42。等待是取消测试必需行为。不修改文件、不创建任务、不调用其他Agent。')]:
 issue=api('POST','issues',dict(title='G86 int CodeBuddy '+name+' sample',description=description,assignee_type='agent',assignee_id=agent['id']));save(name+'-issue',issue);print(name,issue['id'])
