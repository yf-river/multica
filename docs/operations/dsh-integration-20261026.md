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
- 角色权限：平台技能过滤已上线；监督/审查的只读策略代码已提交（`18132b1f8` + bridge `b8dc4c9`），但受托管审查运行时只支持 Codex/CodeBuddy 与测试默认策略限制，尚未发布生产；按角色模型默认值未做。
- 知识库删除仅 CLI/服务端，Web 未提供删除入口；来源 URL 导入、管理员配置未做。
- 统计页尚未提供独立 Context 标签和跨任务上下文聚合。
- bridge 仓库提交 `df5fdfa` 只在本地 profile 生效，远端推送因 GitHub 权限被拒；生产运行依赖本机 profile。

## 未发布后续

- `18132b1f8` / bridge `b8dc4c9`：角色只读沙箱（approval never + sandbox/mode=read-only）。协议级验证通过；GONG-92 因 verify 环境不能把托管审查角色绑定 DSH、普通样本缺 `deepseek-official` 凭据而未完成验收，生产仍为 `46d4f6ee6`。
- bridge 本地提交已到 `b8dc4c9`；远端仓库推送仍受 GitHub 权限限制。

## 运行权限：默认完全访问 + 按 Agent 可配（2026-10-26）

`GONG-55` 暴露了两件事：转录把 DSH 的 `runtime` 遥测帧当成了"错误"（前端 schema 也没有该类型，导致重新打开运行时历史为空），以及 DSH 任务继承了 DSH 自己的 `workspace-write` 默认——bwrap 把 `/tmp` 换成空 tmpfs、其余只读，daemon 建在 `/tmp/multica-task-*` 的 TMPDIR 与 `~/.cache/go-build` 因此在沙箱里不可用，`go vet/test` 直接失败。

- 运行权限成为 Agent 字段 `sandbox_mode`（`read-only` / `workspace-write` / `danger-full-access`），默认 `danger-full-access`，与 `security-model` 承诺的"Multica 不替用户做文件系统沙箱"一致。
- daemon 按 claim 载荷把它翻译成 DSH 的 `DSH_PERMISSION_MODE`；`read-only` 同时把 approval 姿态设为 `never`（否则 agent 可以用一次审批把写入要回来）。按角色（监督/审查）的审批姿态被按 Agent 的档位取代，`18132b1f8` 的角色判定不再参与。
- 非完全访问档位下 daemon 把任务的 `TMPDIR` 重定向到任务工作目录内的 `.multica-tmp`，否则沙箱内没有任何可写临时目录；`GOCACHE` / `GOMODCACHE` 仍在工作区外，构建型任务应保留完全访问。
- Codex / Claude 的沙箱策略继续由守护进程固定（Linux Codex `danger-full-access`、Claude `bypassPermissions`），本字段对它们不生效；设置页在该情况下保持控件可见但禁用并说明原因。
