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


## GONG-93：bridge 收编与自动供给（开发中）

源码从外部 bridge `b8dc4c9` 收进 `packages/dsh-runtime`，包名为
`@multica-ai/dsh-runtime`，版本 `0.3.0`，兼容范围 `>=0.2.1-rc.3 <0.3.0`。
DSH 精确测试版本为 `0.2.1-rc.3`；依赖锁用于复现构建，不锁定用户安装的 DSH。

- 制品为 `dsh-runtime-<version>.tgz`；发布清单记录兼容信息、字节数和 SHA-256。
  Go 与 bridge 共用 execute 和事件黄金帧。正式构建从固定提交生成 bridge，
  运行包携带 tarball，不引用外部开发目录。
- 自动供给使用独立版本 profile、持久化候选状态和原子活动记录。
  安装后经过 probe、真实普通任务/context、原生 plan 切换和同 session 续跑，再激活。
  staging 安装使用 DSH plugin/pnpm 与 `--offline`，宿主 DSH 提供其自身的依赖。
- daemon 认领/准备屏障和跨进程共享执行锁共同阻止运行时切换。
  已验证但忙碌的候选等待空闲，续接不重复安装或消耗模型调用。
  安装或激活失败保留/恢复旧活动记录，同一失败候选持久隔离。
- 活动记录保留 N-1 bridge/profile。可识别的 Node/npm 安装保存独立 Node 与 DSH
  包快照（包含依赖及逐文件校验清单），启动器只引用快照文件；活动与上一代记录
  固定精确 DSH 版本、原安装来源和快照路径。快照缓存跨本机环境复用，不复制用户
  profile、home 或凭据。未知或 Electron 安装没有已验证的独立宿主快照时，健康信息
  明确返回 `dsh_snapshot_available=false`；不能声称该安装支持跨 DSH 版本回退。
- 发布器从运行包派生本环境 artifact/state 路径，`MULTICA_DSH_MANAGED_PROFILE=0`
  停用受控供给并恢复原 `multica` 入口；旧 profile 和外部 bridge 目录尚未删除。
  健康接口返回供给状态及活动版本/哈希，不返回路径和凭据。

本地验证：bridge typecheck、25 项单元测试和构建通过；Go DSH/供给定向测试通过；
发布器原有45项回归和新增制品验证通过。真实 tarball 独立 profile 的普通任务、
context、session 续跑和不调用模型的 `/plan` 通过；供给器真实 staging→smoke→激活通过，
新增空 pnpm 缓存与 `--offline` 安装检查通过。真实检查使用本机 DSH 账号来源，
不构成固定 int 业务验收或生产发布；所有样本都在独立临时目录和专属 profile 中执行。
DSH 自身的模型列表诊断可能混入 stdout，Go adapter 继续按既有协议跳过非 JSON 行。

当前阶段没有生产发布授权，生产仍以现场健康版本为准。后续 DSH 问答与计划卡片
复用 `feature/codex-codebuddy-integration` 的公共交互与轨迹组件，
不额外建立平行审批表或通用界面；DSH 的挂起续跑行为单独扩展。


### 能力探测与宿主恢复补充

- probe/ready 根据实际服务方法报告 commands、userQuestions、sessionProjections、
  approval、sandboxPolicy 和 compaction。optional 服务缺失不使启动崩溃。
  人工计划评审尚未接入，因此 `plan_review=false`，不会把底层服务存在冒充 UI 能力。
- 必需只读策略验证实际沙箱结果，缺失能力或设置失败直接拒绝；普通执行和斜杠命令
  都经过同一前置检查。显式 Agent 能力设置和真实写入拒绝验收属于后续 B2/C。
- 本机旧 DSH 目录的 launcher 仍引用当前安装目录，不能直接作为独立回退入口。
  新宿主快照包含 62958 个文件身份，独立启动返回 `0.2.1-rc.3`，基于该快照的真实
  普通任务、context、plan 开关、session 恢复及激活通过。首次复制和 smoke 共163.90秒；
  后续候选复用经过校验的宿主快照。
- 单元测试验证原安装与 Node 文件被移除后，快照启动器仍可运行；外部依赖链接被拒绝。
  Linux 定向 race 检查通过，Windows 锁与快照代码已交叉编译；Windows 真实执行仍待 CI。
- bridge 当前28项单元测试通过。上述检查不代替固定 int 及生产业务验收。


### 首次 verify-3 验收与交付修复

`e95c9dfd7` 的 verify-3 程序身份正确，但在役旧版外部发布器没有复制新增 bridge
制品，daemon 供给状态为空，因此本轮功能验收未通过。没有手工补写运行目录，
没有把程序健康当成功能通过，也没有修改生产发布器。

修复由 daemon 二进制内置受信 tarball 与 SHA-256 清单，启动时提取到本环境私有
CLI profile 目录；独立 daemon 安装和旧发布器都能供给 bridge。生成制品从
`packages/dsh-runtime` 构建，CI 重新生成并校验字节；用户可用既有开关停用。

verify-3 当前没有 DSH 认证，真实 verify/int 样本还需明确测试账号来源。
本机现有 DSH 登录同时用于日常运行，未经该测试范围授权不复制到验收环境。
