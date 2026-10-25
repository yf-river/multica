exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
import time
issue=api('POST','issues',dict(title='G86-87 取消后重试实际验收',description='隔离测试：先执行一次sleep 15，再仅回答42。等待用于验收取消行为。不修改文件、不创建任务、不调用其他Agent。',assignee_type='agent',assignee_id='3574804a-a55e-4a2a-92ae-85eb94dae8ec'));save('cancel-issue',issue)
end=time.monotonic()+40
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs');run=runs[0]
 if run['status']=='running':break
 if time.monotonic()>end:raise SystemExit('cancel sample not running yet')
 time.sleep(2)
save('cancel-running',run);save('cancel-receipt',api('POST','issues/'+issue['id']+'/tasks/'+run['id']+'/cancel',{}))
end=time.monotonic()+12
while True:
 runs=api('GET','issues/'+issue['id']+'/task-runs')
 if runs[0]['status']=='cancelled':break
 if time.monotonic()>end:raise SystemExit('cancellation not settled')
 time.sleep(1)
save('cancelled-runs',runs);save('retry-receipt',api('POST','issues/'+issue['id']+'/rerun',dict(task_id=run['id'])));print('started run cancelled and retry accepted',run['id'])
