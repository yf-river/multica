## 实施计划（GONG-68 原范围内）

1. `issue_delivery` 授权状态分类：`ready`（当前候选已复核）、`unbound`（有有效发布授权但缺当前候选复核）、`missing`（无有效授权/已撤销）。
2. int 验收完成后分流：`ready` 直接推进发布；`unbound` 落库待复核意图并定向通知负责人；`missing` 只记录等待原因，不自动批准。
3. 待复核意图随事务落库，按“交付 + 候选 + 源码提交 + 评论版本 + 授权记录”去重；通知号预分配，重复轮询、重复回调、服务重启都不重复派发；负责人已有活动运行时保留意图等其结束；通知失败由队列重试；显式接续受次数上限约束。
4. 复核绑定后清除待复核意图；候选变化、重新联调、授权撤回时旧意图失效。
5. 交付记录新增 `authorization_state`，区分等待用户授权、待负责人核对、已通知未接续。
6. 同步通知文案、内置任务技能 `multica-working-on-issues`、运维真源 `docs/operations/environment-releases.md` 和 ida-development 的候选复核入口。

## 验证

- Go handler 集成测试（真实 PostgreSQL 状态机）：已授权未复核自动续接、已绑定直接推进、无授权等待、授权撤回、负责人活动运行去重、重复轮询不重复唤醒。
- 交付脚本测试：`issue-delivery-worker.test.py`、`delivery-coordination.test.py`。
- int 部署后按正式交付接口完成验收，只到测试环境，不申请生产发布。

本计划只登记执行范围授权；生产发布不在本次授权内。
