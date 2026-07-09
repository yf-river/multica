# Goal: TAPD 到评论式 SOP 闭环验收

## 当前目标

从 TAPD 需求材料创建 issue，按真实用户评论方式推进 SOP：需求澄清、方案设计、任务拆分、开发、测试、MR、人肉 CodeReview、增量需求处理、运行复盘。最终需要通过公开 UI/API 证明流程、数据、附件、trace、usage、MR 关联、复盘导出都闭环。

固定模拟 TAPD 来源：

- https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154

## 强约束

- 只运行一条模拟任务；失败任务必须标记 blocked 或删除，不得作为最终证据。
- 真实通过评论推进，不能直接改数据库制造状态。
- 子任务必须能创建、触发、产生 trace/usage 和 handoff 闭包证据。
- 跨项目 child issue 在本 E2E 中不承担完整产品开发，技术验收由父任务 service sandbox 统一完成。
- 父任务必须有 quick-entries 需求级 service sandbox：
  - `GET /v1/usercenter/quick-entries`
  - `POST /v1/usercenter/quick-entries`
  - `DELETE /v1/usercenter/quick-entries/{id}`
  - 缺少 `X-Request-ID` 返回明确 4xx
  - 越权/归属字段注入被拒绝或忽略
- historical sandbox 继续作为历史真实链路回归，不得替代当前需求级验收。
- 验证通过后才创建 Gongfeng MR，并把 MR 关联回 issue。
- CodeReview 是 05 之后的人工阶段，依赖人工评论或工蜂链接，不自动伪造 review 通过。

## 本轮失败结论

2026-06-30 的 GOA-584 模拟失败，已归档为 blocked。

失败原因不是 TLS/network，而是流程边界：

- gateway child issue 被完整展开到 01-05，并进入真实 04-implement 落改，超过 E2E 等待窗口。
- 02-design 一度把 historical 样本 `/v1/user-center/link-user-space-org` 污染到当前 quick-entries 合同，后续被 pm 打回修正。
- 子任务应验证跨项目 handoff 闭包，不应在这条 E2E 中承担完整产品开发。

已做修正：

- 历史 customer-comment 专项验收脚本已下线，后续只保留通用部署、smoke 和 UI 验收入口。
- child issue handoff 评论明确禁止展开完整 01-05、禁止真实落改，只输出交付闭包摘要。
- child 产生一轮有效 task message/trace/usage 后，E2E 会取消后续扩散任务并由父任务统一关闭。
- customer-comment E2E 等待窗口提升到 2 小时，降低误判。

## 下一轮验收

1. 部署最新代码到联调环境。
2. 确认无 active task。
3. 新建一条干净模拟任务。
4. 验证 child issue：
   - gateway 和 ida-deployment 均创建为 backlog。
   - 调整状态后能触发运行。
   - 只输出 handoff closure，不展开完整阶段链。
   - trace、message、usage 存在。
5. 验证父任务：
   - child done 后能唤醒父任务。
   - quick-entries service sandbox 和 historical sandbox 都通过并以附件/comment 呈现。
   - 05-verify 能把父任务置为 done。
   - MR 在 05 之后创建并关联。
   - CodeReview/增量需求/简单任务直通能继续闭环。

## 愿景

这个系统最终应该像一名严格但高效的工程 PM：从 TAPD 需求自动建立上下文，识别涉及项目，创建合适的跨项目交付物，按评论与人工确认推进，能在每个阶段沉淀可下载/可预览的 markdown 产物，能把 MR 和人工 CodeReview 纳入同一条 issue 生命周期，能在运行复盘中清楚解释耗时、token、节点和原始事件。

完成后必须自动自评是否达到这个愿景。满分 10 分，低于 9.5 分不能宣称完成，必须列出原因并正向修复。
