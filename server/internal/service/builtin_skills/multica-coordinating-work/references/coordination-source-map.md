# 协调能力边界与真源

- Mika 默认模板：`server/internal/service/builtin_agents/mika/INSTRUCTIONS.md`；`builtin_agents.go` 仅在创建时填充。当前 Agent 数据库指令通过正常 Agent 更新接口维护，修改模板不会自动覆盖现有工作区。
- 技能装载：`server/internal/service/builtin_skills.go` 将本目录随服务版本打包；运行时按技能目录加载正文与支持文件。不要再复制成另一份工作区技能，产生两套真源。
- 任务、评论、子任务阶段和续接：`multica-working-on-issues` 及其 source map；评论与提及路由见 `multica-mentioning`。子任务阶段完成通知与小队队长通知有明确路由，普通独立任务不会因为 Mika 关注就自动订阅。
- 委托回执：`server/cmd/server/delegated_result.go` 消费已校验的完成/取消持久事件，`server/internal/service/delegated_result.go` 复用原失败恢复的评论、投递回执与有界重放。来源依赖真实 `delegated_from_task_id`；回执执行不会反向唤醒 worker，发布暂停交给发布窗口续接。人工独立创建的历史任务不会自动获得委托关系。
- 发布：目标仓库 `docs/operations/environment-releases.md`、持久交付记录、正常 CLI/API 与安装的 publisher。协调任务退出不代表业务完成；需要明确下一事件及接收者。
- 用量：现有 task usage 与 task trace。看板任务可有多个执行；多个执行可续接同一 session，一个执行也不保证只有一个外部 trace。缺失数据不能推断成零。汇总与验收口径见 `docs/operations/usage-metrics.md`。

持续协调优先使用现有子任务阶段屏障、小队通知和真实委托来源；先确认预期事件能到达协调者。不为原本独立的历史任务伪造 parent 或改归属。缺少事件链路时报告该缺口，并由正常调度能力处理；不得承诺“自动持续跟进”却只留一个无人消费的评论。
