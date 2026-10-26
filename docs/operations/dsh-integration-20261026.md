# DSH 深度集成：运行数据、计划/目标、知识库与监督（2026-10-26）

## 发布

- 固定候选：`46d4f6ee6504b631edbb8bd910099ac9d27ff064`
- 任务分支：`feature/gong-90-dsh-integration-v4`
- 生产健康：`/health` 返回上述 commit（2026-10-26 13:23 CST）
- 交付：GONG-90，`published`；生产真实任务 GONG-91 完成并通过审查

## 交付内容

### 运行数据与 Context

- 执行树挂载 `task_usage`，运行详情的 token/缓存/成本不再为 0。
- 运行复盘的任务级列表移除；运行详情迁到 issue 执行日志（时间线、事件、JSON 导出）。
- DSH bridge 上报 `context` / `compaction` 帧：provider/model、duration、input/output/cache、context_window、pressure、projected、breakdown。
- 新增 `GET /api/issues/{id}/tasks/{taskId}/context`，issue 执行日志提供“运行上下文”弹窗。
- 统计页增加模型/运行时拆分、缓存命中与失败率 KPI、运行效率摘要；i18n 插值修正。

### `/plan` 与 `/goal`

- 聊天命令路由到 DSH 命令注册表，不产生模型请求。
- `/plan <message>` 拆成 plan mode + follow-up 回合；plan review 自动选择 approve（默认计划后自动执行），计划正文作为消息输出，最终 result 使用最终 assistant 文本。
- 裸 `/plan` 通过聊天历史重放保持模式；`/plan off` 关闭。
- `/goal` 首轮通过 command+prompt 进入 session；命令后没有真实模型回合时，服务端把目标重放进第一条消息。

### 知识库

- 工作区级 OpenViking 代理：状态、浏览、搜索、读取、上传、删除。
- 默认命名空间 `viking://resources/workspaces/<workspace_id>`，强制身份头与 URI 前缀。
- Web 知识库菜单；CLI `multica knowledge status|list|search|read|add|rm`；内置 `multica-knowledge` 技能。
- 首次上传自动创建父命名空间。

### 角色、监督与质量

- `quality_supervision` / `issue_review` 不再收到 issue/delivery 平台技能，旧载荷与 skill-assets 两条路径都过滤。
- 监督材料汇总 `runtime_observation`（requests、window、peak、projected、compactions、model duration、token、breakdown），默认提示词补充字段语义与缺口规则。
- 质量页展示运行观测；监督详情修复 `report` text 到 jsonb 的读取错误。

## 真实验收

- verify-2：DSH `/plan`、自动批准、执行、context 帧、知识库上传/读取/搜索/隔离、统计/监督插值、监督详情 200。
- 组合 int：真实 DSH chat 与 issue 任务 completed；运行上下文接口返回完整 context；新工作区首次上传 200；监督材料非空：
  - `requests=5, window=1048576, peak=9751, duration=1282ms, system/tools/messages=695/4899/3353`
- 生产：GONG-91 真实 DSH 任务 completed，输出含 `PROD_DSH_OK=4757`；`task_usage` input 12929/output 686；context 帧含 pressure/window/breakdown/duration；审查任务对结果给出 pass。

## 验证命令

```text
pnpm typecheck（6 tasks）                      PASS
pnpm --filter @multica/views exec vitest run dashboard  82 PASS
pnpm --filter @multica/views exec vitest run run-reviews 47 PASS
go build ./... / go vet ./...                  PASS
bridge pnpm check（22 tests + build）           PASS
```

## 已知边界

- 计划评审当前是自动批准，没有人工计划卡片/审批 UI；`require-plan-approval` 未实现。
- `/goal` 的多轮驱动依赖 DSH goal-round-driver；首轮与重放已验证，未做多轮上限/阻塞的完整矩阵。
- 角色权限只做了技能过滤；监督/审查的运行时只读策略、按角色模型默认值尚未落地。
- 知识库删除仅 CLI/服务端，Web 未提供删除入口；来源 URL 导入、管理员配置未做。
- 统计页尚未提供独立 Context 标签和跨任务上下文聚合。
- bridge 仓库提交 `df5fdfa` 只在本地 profile 生效，远端推送因 GitHub 权限被拒；生产运行依赖本机 profile。
