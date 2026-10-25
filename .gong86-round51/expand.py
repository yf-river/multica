import sys
sys.path.insert(0,'.gong86-current');from round51 import *
i=json.load(open(D/'codebuddy-issue.json'));run=json.load(open(D/'codebuddy-runs.json'))[0]
save('codebuddy-resume',api('POST','issues/'+i['id']+'/rerun',dict(task_id=run['id'])))
agent=json.load(open(D/'agent-effective.json'))
for name,description in [('child','先读取本工单，使用一次内置子代理工具计算17+25，再汇总答案；若工具不可用明确说明。不创建或调用其他平台Agent，不修改文件。'),('cancel','隔离取消测试：执行一次sleep 30，再回答42；等待是用户要求，不修改文件、不创建任务。')]:
 issue=api('POST','issues',dict(title='G86-51 '+name+'样本',description=description,assignee_type='agent',assignee_id=agent['id']));save(name+'-issue',issue);print(name,issue['id'])
