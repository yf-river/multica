# Agent 调用授权

本文是「谁能触发某次 Agent 运行」的权威说明：调用门禁的口径、现有的人类授权来源，以及任务级委托授权继承（GONG-83）的绑定、证据、生命周期与拒绝原因。归因（originator / accountable）与授权是两件事，改动其中一件前先读本文。

## 调用门禁

唯一的私有 Agent 调用门禁是 `server/internal/handler/agent_access.go` 的 `canInvokeAgent` → `invokeAgentDecision`：

- `permission_mode = private` 只有「有效人类 == agent owner」可以调用；没有工作区 admin 绕过，也没有 agent → agent 旁路。
- `permission_mode = public_to` 由 `agent_invocation_target` 白名单决定：`workspace` 接收任何成员与工作区内部主体，`member` 只接收指定用户，`team` 在 V1 不生效。
- 托管审查 Agent 与上游同步执行体在门禁最前面就被拒，只接受服务端的专门派发。
- A2A（Agent 之间）按调用链顶端的人类 originator 判定，绝不看直接的 agent 主体。

门禁只回答「能否为这个 Agent 入队一次运行」，输入是一个人类主体。这个人类从哪来由授权来源决定；写入 `originator_user_id` / `accountable_user_id` 的是归因，两者互不替代。

## 授权来源

按优先级依次尝试，前一条解析出人类时不再往下走：

1. 真实 originator：`invokeOriginatorFromRequest` 读 `X-Task-ID` 指向任务的 `originator_user_id`；评论路径从 `comment.source_task_id` 解析（`autopilotDelegationAuthorityFromComment`、`taskDelegationAuthorityFromComment` 使用同一份服务端可信血统）。
2. autopilot 委派授权（MUL-4857 / MUL-6691）：`autopilotTaskAssignAuthority` 与 `autopilotDelegationAuthority`，只在 `originator_source ∈ {trigger_owner, rule_owner}`、运行仍 `running`、且工作可验证属于该 autopilot 运行时出借 autopilot 创建者或任务可追责人类。`owner_fallback`、`delegation` 刻意排除。
3. 任务级委托授权继承（GONG-83）：见下节。只在上面两条都解析不出人类时才追问。

新顶层任务（`scopeNewTopLevelIssue`）与明确关闭委派的入口（`scopeNoDelegation`）只使用前两条。squad 领袖的指派校验同样只用前两条，因为真正的入队还会由 `enqueueSquadLeaderTask` 用 `OriginatorForIssueTask` 再判一次，两处解析不同会让指派成功却什么都不跑。

## 任务级委托授权继承

**要解决的问题。** 审查结论或系统事件会把协调者唤醒成一条没有人类发起人的运行（`originator_user_id = NULL`、`originator_source = delegation`）。用户前不久刚在同一条看板任务上授权过一模一样的委托，但这条唤醒运行自己链顶没有人，门禁只能失败关闭，于是系统反馈之后再也续不上私有开发 Agent。

**证据。** 只读查询 `FindTaskDelegationAuthorization`（`server/pkg/db/queries/agent.sql`）自连接 `issue` 与 `agent_task_queue`，回答「这条任务范围内是否真的发生过：人类 U 授权了协调者的某次运行 → 协调者把工作交给了目标 Agent」。匹配条件：

- `t.agent_id` = 目标 Agent，`d.agent_id` = 当前运行 Agent（委托者）；
- `t.originator_user_id IS NOT NULL` 且等于 `d.originator_user_id`——两侧同一位人类，链上没有人类的历史任务不算证据；
- `t.originator_source IN ('delegation', 'comment_source')`——只有复制父运行人类的两种标签能描述一次委托；`direct_human`、`trigger_owner`、`rule_owner`、`owner_fallback`、`unattributed` 都不算；
- `delegated_from_task_id` 指向 `d`，这条列就是「谁发起的这次委托」；
- 证据任务落在 scope 任务本身或它的直接子任务上，且证据 issue 属于请求的工作区（跨租户永远匹配不到）。

**绑定。** 一次授权绑定 workspace + scope 任务（及其直接子任务）+ 委托者 + 目标 Agent。四者任一不同都不成立，所以一次授权推不出其他任务、其他 Agent 或新顶层任务的权限。

**判定顺序**（`taskDelegationAuthority`，任何一步不满足即失败关闭）：

1. actor 是 agent、`run.agent_id == actor`。**新请求**还要求 `run.status == running`——结束的运行不能为一条新请求放权；**重放**（`taskDelegationAuthorityForReplay`）不要求，理由见下面的「重放」段落。
2. `run.issue_id` 有效，且 scope 任务是该运行所在任务本身或它的直接子任务——兄弟任务、父任务和更远血缘都不在范围内。
3. scope 任务属于请求的工作区；`issuestatus.Effective` 不是 `done` / `cancelled`（授权随任务终止失效）。
4. 上面那条查询取到人类 U。
5. U 仍是该工作区成员。
6. `canInvokeAgent(target, member, U)` 仍为真——私有 Agent 换了 owner 或从白名单移除都算撤回。

**重放。** 目标 Agent 正忙时，创建评论那一刻受理的是「推迟」：调用方拿到成功形态 `deferred`，承诺由目标完成时的调和接手。重放是服务端对这条**内容未变的已存评论**再处理，不是一条新请求；评论的 `source_task_id` 只由作者运行在写入时打下，而协调者通常写完这一轮就结束，重放又必然发生在目标 Agent 完成 / 取消之后。所以重放形态只核身份（`run.agent_id == author`）与血缘，不要求该运行此刻仍在 `running`——否则上面那个 `deferred` 会静默落空。范围、任务活性、证据、授权者当下资格一条不减，已结束的运行仍然不能为任何**新请求**放权。

**归因不变。** 继承来的 U 只作为 `canInvokeAgent` 的输入人类，绝不写进 `originator_user_id` / `accountable_user_id`。被唤醒的运行仍是系统来源，它派发的新任务保持原归因语义（`source` 不变、originator 仍为空）。这也是为什么不能改用 accountable 或 autopilot 创建者兜底：那等于给任何无法归因的运行开放 owner 的私有 Agent。

**接入点**（同一判定，避免预览与实际不一致）：

- issue 创建 / 指派：`validateAssigneePair`（`scopeChildOf` / `scopeExistingIssue`）。
- 评论预览：`PreviewCommentTriggers`。
- 评论真实派发：`CreateComment` → `triggerTasksForComment`。
- 评论编辑重发：`UpdateComment`（`source_task_id` 由本次编辑重打，所以按新请求判定）。
- 重放（不要求作者运行仍在跑）：`reconcileCommentsOnCompletion`（目标完成时重放被推迟的提及）、`retriggerCancelledTaskSurvivors`（取消批次的幸存者重放），都从 `comment.source_task_id` 解析，走 `commentTaskDelegationReplayResolver`。
- quick action 派发：`RunQuickAction`。
- issue 触发预览：`issueTriggerPreviewProbe`（与写路径选同一条 issue 作 scope，更新时是写前的那条）。

**拒绝原因码。** 只写结构化日志（`task delegation authority denied`），HTTP 文案保持既有通用措辞，不泄露目标 Agent 的权限模式：`not_agent_actor`、`no_run`、`run_agent_mismatch`、`run_not_running`（仅新请求）、`run_unbound`、`scope_unbound`、`scope_mismatch`、`scope_workspace_mismatch`、`issue_inactive`、`no_authorization_evidence`、`authorization_lookup_failed`、`authorizer_not_member`、`authorizer_lacks_permission`、`target_unbound`。

**生命周期。** 授权随任务终止失效：scope 任务完成后不再续接。要恢复，必须重新有一条真实人类发起的委托，或用户对新任务重新授权。

## 变更边界

- 新增调用路径必须说明它的授权来源；不能只给门禁换一个人，而不做证据与绑定校验。
- 不放宽 `private`：继承只改变「用谁的名义过闸」，`permission_mode`、owner 与白名单语义不变。
- 托管审查员不可被普通调用、autopilot 例外、真实人类 originator 路径是既有边界，改动时必须有回归用例。
- 拒绝原因进日志，不进响应；错误文案不点名目标的权限模式。

## 相关实现与测试

- `server/internal/handler/agent_access.go`（门禁与三种授权来源）
- `server/internal/handler/comment.go`（`commentTriggerComputeOptions.effectiveInvokerFor`）
- `server/internal/handler/task_delegation_authority_test.go`（原故障回归、拒绝矩阵、重放时序回归）
