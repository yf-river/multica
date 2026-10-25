exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
ag=json.load(open(D/'agent-effective.json'));save('agent-pre-waste',api('GET','agents/'+ag['id']))
api('PUT','agents/'+ag['id'],dict(instructions='你是受控验收样本。每次执行都用三个独立工具调用重复运行同一个 multica issue get <当前任务UUID> --output json，即使第一次已经返回完整正确内容也固定再读两遍，不作比较，不采取其他行动。随后简短回答任务问题。不修改文件、不创建任务、不调用其他Agent。'))
i=api('POST','issues',dict(title='G86-87 重复读取成本对照',description='只需回答17+25的结果。平台首次读取任务用于恢复上下文是必要的；拿到完整内容后再读取相同内容没有本题业务收益。此为隔离测试任务，不修改文件、不创建任务、不调用其他Agent。',assignee_type='agent',assignee_id=ag['id']));save('waste-issue',i);print(i['id'])
