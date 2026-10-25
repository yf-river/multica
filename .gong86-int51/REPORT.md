GONG-86 固定 int 验收未通过

交付：01a1723e-ac21-78a7-a120-5842f6732045；源码：51cb16cab2dd68547ecc3d93ef9fc5bd707364b1；候选：51cb16cab2dd68547ecc3d93ef9fc5bd707364b1-1792919060746019202；入口：http://9.134.129.162:13682。本轮没有修改源码、合并或发布生产，没有更换或重置环境数据。

已实际执行：
1. 固定 int 五项监督测试：并发派发和长历史、连续分页、全部/单项停止、人类新决定保护通过；TestIntPersistenceAndResume失败。原日志fixed-int-five.log完整保留，无跳过。
2. 真实Codex样本：首次回答42、同session续接回答42，另一样本进入sleep工具调用后取消。样本Agent虽命名CodeBuddy，但创建时被测试策略强制成Codex；所有本轮材料和结论均按实际runtime=codex，不能计为CodeBuddy通过。
3. 两个真实监督批次completed；首次与续接均clear，取消效率uncertain，正确性clear。两者分别保留正文覆盖与请求级分配缺口；监督没有将必要读取或明确要求的等待当浪费。材料3份，按连续offset完整读取并校验各自generation/sha256一致，外部HTTP均0。模型自身用量在subject.supervisor_usage单列。
4. 浏览器真实认证进入监督列表及详情，刷新后保留任务及结论，1440px与390px截图，pageErrors=0。browser.json、detail.png、mobile.png、detail-text.txt、refresh-text.txt。
5. 真实审查员回归completed，算术结果经审查通过且没有blocking/quality意见；测试任务仍in_review。review-comments.json和review-messages记录持久化结果。

未通过与缺口：
- TestIntPersistenceAndResume在源码65行创建status=offline夹具，却在311/318行断言旧“不支持provider”错误；当前实际返回“监督 Agent 暂不可用：agent runtime is offline”，在312行失败，后续断言未执行。该结果支持测试契约陈旧的判断，不证明真实持续更新场景已通过，也不能据此直接判产品调度缺陷。本轮固定提交不可修改，未用修改断言/删除断言的方式宣称通过。
- 当前int默认测试策略为Codex。新建Agent提交CodeBuddy参数仍由策略覆盖。scripts/test-agent-models.mjs定向入口读取交付时仅接受phase=testing（约111行），而当前准确int交付phase=preparing_release。未绕过该围栏、未直接修改数据库或其他Agent策略；因此本轮CodeBuddy与AgentLens真实补测未完成。该项为源码核对得到的入口限制，未虚报已执行配置成功。
- 本轮没有重新完成原计划全部故障矩阵、混合runtime及效率浪费对照，历史verify51报告仅作背景，不代替本轮full通过。
- 两个监督批次的completed不表示正文全部已被模型读完。取消批次明确只读0—8000/54632；本验收脚本完整读取3份正文不冒充模型阅读覆盖。

清理：所有本轮样本执行及监督批次终态；恢复监督和审查原设置，归档本轮新建Agent；恢复后端原服务配置并删除临时0600凭据和备份。cleanup.json可核对。未删除样本/历史材料。原AGENTS.md改动保留。

结论：不可登记完整验收通过，不应进入生产。通过accept提交实际状态并保留其回执；若接口只接受passed，将明确记录拒绝并通过任务附件保留失败证据，不伪报accept成功。后续需要在新的固定候选中修复测试前置与int单Agent配置入口后，重做未通过范围。

accept实际回执：Request conflict: 交付 01a1723e-ac21-78a7-a120-5842f6732045：缺少 integration 业务验收证据：固定int监督页面刷新持久化与审查回归

交付仍为preparing_release，revision=8；未登记完整验收成功。
