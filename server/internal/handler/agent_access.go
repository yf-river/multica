// 本文件是 agent 访问与调用权限模型（MUL-3963）的核心判定区：谁能「看」某个
// agent（列表/详情导航、聊天记录读取、取消任务授权）与谁能「触发」一次运行
// （canInvokeAgent）是两个不同问题。调用门禁是安全关键点——admin 不该仅因为是
// admin 就能调用他人的 private agent（从而借用其所有者的 Composio/OAuth 连接）；
// 授权语义以调用链顶端的人类发起者（originator）为准，杜绝 agent 绕开 owner
// 白名单成链。文件还含 autopilot 派生的委派授权（MUL-4857 / MUL-6691）与
// 聚合端点用的可访问/受限 agent 集合。
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Agent 调用权限模型（MUL-3963）。
//
// 两个不同的问题，过去在 canAccessPrivateAgent 里被混为一谈：
//
//   - "该 actor 能否在 UI 里 SEE / 打开这个 agent"  -> canAccessPrivateAgent
//   - "该 actor 能否为这个 agent TRIGGER 一次运行"  -> canInvokeAgent
//
// invoke 门禁才是安全关键：工作区 admin 不能因为自己是 admin 就能调用某人的
// private agent（从而用那个所有者的 Composio/OAuth 连接）。Admin 保留管理与库存
// 可见性，但不保留「运行」权。
//
// permission_mode 驱动 invoke：
//   - private   -> 只有 agent 所有者可调用；无 admin 绕过、无 A2A 绕过。
//   - public_to -> 由 agent_invocation_target 白名单决定：
//     * workspace target -> 任何工作区成员（以及工作区内 agent/system 主体）
//     都可调用。
//     * member target    -> 只有指定用户可调用。
//     * team target      -> 预留，V1 中不起作用。
//
// A2A 以调用链顶端的人类 originator 判定，绝不看直接 agent actor：用户 U 触发
// agent A、A 再 @ 到 agent B 时，只有 U（originator）在 B 的白名单里，B 才可被
// 调用。这防止 agent 之间形成绕过 owner 白名单的通道。

// canInvokeAgent 判断能否为 `agent` 以给定 actor 的名义入队一次运行。判定依据
// 「有效调用用户」：
//   - member actor -> 成员本人（actorID）
//   - agent actor  -> 调用链顶端的人类 originator（originatorUserID）
//   - system actor -> 解析到 originator 时用它，否则无用户
//
// originatorUserID 在无法归因到人类时为空串。对 private agent 这意味着「拒绝」
// （除非 actor 就是所有者）。对 public_to agent，workspace target 仍接收工作区
// 内部的 agent/system 主体，但 member/team target 在没有匹配人类时失败关闭。
func (h *Handler) canInvokeAgent(ctx context.Context, agent db.Agent, actorType, actorID, originatorUserID, workspaceID string) bool {
	allowed := h.invokeAgentDecision(ctx, agent, actorType, actorID, originatorUserID, workspaceID)
	if !allowed && actorType != "member" && originatorUserID == "" {
		// MUL-6490：线格式原因保持刻意通用的 invocation_not_allowed
		// （dispatch/reason.go——不能暴露目标是否存在），这会让工作区本可修复的
		// 那类拒绝看起来与普通权限错误无异。在此点名根因——只有运维会读：这条链
		// 到达本门禁时顶端没有人类，因此 member 作用域的白名单匹配不到任何人。
		// 没有这行，被报告的故障模式（断掉 N 跳的 originator 链）就只能直接查
		// agent_task_queue 才能诊断。
		slog.WarnContext(ctx, "agent invoke denied: delegation chain carries no human originator",
			"agent_id", uuidToString(agent.ID),
			"agent_permission_mode", agent.PermissionMode,
			"actor_type", actorType,
			"actor_id", actorID,
			"workspace_id", workspaceID,
		)
	}
	return allowed
}

// invokeAgentDecision 是 canInvokeAgent 的纯判定，拆出来让门禁恰好有一个能观察
// 拒绝的地方。
func (h *Handler) invokeAgentDecision(ctx context.Context, agent db.Agent, actorType, actorID, originatorUserID, workspaceID string) bool {
	effectiveUser := actorID
	if actorType != "member" {
		// agent / system：绝不信任直接主体，只信任链顶解析出的人类 originator。
		effectiveUser = originatorUserID
	}

	// agent 所有者总能调用自己的 agent。
	if effectiveUser != "" && uuidToString(agent.OwnerID) == effectiveUser {
		return true
	}

	if agent.PermissionMode != "public_to" {
		// private（或任何未知模式）默认拒绝：无 admin 绕过、无 A2A 绕过。只有
		// 上面那个 owner 分支能通过。
		return false
	}

	targets, err := h.Queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}

	// agent 与 system 触发器是工作区内部主体：workspace target 即使没解析出人类
	// originator 也接收它们。这是「刻意为之、产品批准的例外」（MUL-3963）：
	// webhook / system / workspace 级自动化必须能触发 `public_to workspace` 的
	// agent，哪怕链顶没有人类。它被收得很紧——只放宽 *workspace* 这一个 target。
	// member/team target 仍要求有解析出的人类 originator 才能匹配，因此无法归因
	// 的 agent/system 触发对 member-/team- 作用域的近似 private 白名单失败关闭，
	// 永远混不进某人的「指定人群」授权里。
	workspaceBroad := actorType == "agent" || actorType == "system"
	isWorkspaceMember := false
	if effectiveUser != "" {
		if _, err := h.getWorkspaceMember(ctx, effectiveUser, workspaceID); err == nil {
			isWorkspaceMember = true
		}
	}

	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			if isWorkspaceMember || workspaceBroad {
				return true
			}
		case "member":
			// 需要有解析出的人类。没有 originator（effectiveUser == ""）的
			// agent/system 触发在这里永远不匹配——失败关闭。
			if effectiveUser != "" && uuidToString(t.TargetID) == effectiveUser {
				return true
			}
		case "team":
			// 预留：V1 尚不存在团队归属，team target 谁也不接收（对 system/agent
			// 同样失败关闭）。
		}
	}
	return false
}

// canAccessPrivateAgent 门控「查看」类表面（列表/详情导航、聊天记录读取、
// 取消任务授权）。它不是触发门禁——触发见 canInvokeAgent。
//
// 规则：
//   - agent actor 恒通过（保留 A2A 协作与巡检）。
//   - agent 所有者恒通过。
//   - 工作区 owner/admin 通过（保留治理/库存可见性）。
//   - 普通成员只有在命中 public_to agent 的 workspace 或 member target 时才
//     通过；private agent 保持仅 owner+admin 可见。
func (h *Handler) canAccessPrivateAgent(ctx context.Context, agent db.Agent, actorType, actorID, workspaceID string) bool {
	if actorType == "agent" {
		return true
	}
	if uuidToString(agent.OwnerID) == actorID {
		return true
	}
	member, err := h.getWorkspaceMember(ctx, actorID, workspaceID)
	if err != nil {
		return false
	}
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	if agent.PermissionMode != "public_to" {
		return false
	}
	targets, err := h.Queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	return memberHitsInvocationTargets(targets, actorID)
}

// memberHitsInvocationTargets 是决定「普通成员是否在 public_to agent 白名单上」
// 的纯谓词，单 agent 视图门禁与 ListAgents 批量过滤器共用。workspace target 接收
// 任何成员；member target 只接收匹配用户；team target 不起作用。
func memberHitsInvocationTargets(targets []db.AgentInvocationTarget, userID string) bool {
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			return true
		case "member":
			if uuidToString(t.TargetID) == userID {
				return true
			}
		}
	}
	return false
}

// memberAllowedToViewAgent 是 ListAgents / 聚合端点的过滤谓词。调用方提供该
// agent 已批量加载的 invocation targets，让列表端点避免 N+1。工作区 owner/admin
// 与 agent 所有者看得到一切；普通成员只有命中 public_to agent 白名单才看得到它，
// 永远看不到别人的 private agent。
func memberAllowedToViewAgent(agent db.Agent, targets []db.AgentInvocationTarget, userID, role string) bool {
	if roleAllowed(role, "owner", "admin") {
		return true
	}
	if uuidToString(agent.OwnerID) == userID {
		return true
	}
	if agent.PermissionMode != "public_to" {
		return false
	}
	return memberHitsInvocationTargets(targets, userID)
}

// invokeOriginatorFromRequest 为 HTTP 发起的调用解析调用链顶端的人类用户 id。
// 成员就是自己的 originator；agent actor 从 X-Task-ID 头（CLI 在每次请求上都
// 打）所指的任务继承 originator，与 TaskService.resolveOriginatorFromTriggerComment
// 一致。无法归因到人类时返回 ""——canInvokeAgent 随即对 member/team target 失败
// 关闭。
func (h *Handler) invokeOriginatorFromRequest(r *http.Request, actorType, actorID string) string {
	if actorType == "member" {
		return actorID
	}
	if actorType == "agent" {
		if taskIDHeader := r.Header.Get("X-Task-ID"); taskIDHeader != "" {
			if taskUUID, err := util.ParseUUID(taskIDHeader); err == nil {
				if task, err := h.Queries.GetAgentTask(r.Context(), taskUUID); err == nil {
					return uuidToString(task.OriginatorUserID)
				}
			}
		}
	}
	return ""
}

// assignAuthorityScope 告诉 invoke 门禁「一次指派是针对什么」做出的，让无法归因
// 的 autopilot 运行只能为它可验证正在做的工作借用权威（MUL-6691）。
//
// 调用方具体为哪种形态背书决定了可借到什么，因此几种 kind 彼此区分，而不是单一
// 的「某个 issue」指针。零值什么也不授予——不带 scope 的调用方保持 pre-MUL-4857
// 的「真实 originator 或拒绝」行为，这正是 member-only 入口点想要的。
type assignAuthorityScope struct {
	// Issue 是指派绑定的、已加载的 issue：scopeKindChildOf 时是 PARENT，
	// scopeKindExistingIssue 时是 issue 本身。它的 workspace 必须与请求一致，且
	// 发言的运行必须可验证地挂在它上面（issueBoundToAutopilotTask），任何权威
	// 才可被借用。
	Issue *db.Issue

	Kind assignScopeKind
}

// assignScopeKind 区分指派形态，因为不同形态不接受同样的回退：只有子创建——即
// MUL-4857 已上线的那个表面——才能回退到粗粒度的 autopilot-创建者权威。
type assignScopeKind int

const (
	// scopeKindNone: 完全不借任何 autopilot 权威。
	scopeKindNone assignScopeKind = iota
	// scopeKindChildOf: 在 Issue 下创建子项。
	scopeKindChildOf
	// scopeKindExistingIssue: 指派 Issue 本身。
	scopeKindExistingIssue
	// scopeKindNewTopLevelIssue: 创建无父 issue——它尚不存在，因此绑定对象是发言
	// 任务自己的 autopilot 运行。
	scopeKindNewTopLevelIssue
)

// scopeChildOf 把指派绑到即将在其下创建子项的 parent 上。nil parent 得到空 scope，
// 什么也不授予。
func scopeChildOf(parent *db.Issue) assignAuthorityScope {
	if parent == nil {
		return assignAuthorityScope{}
	}
	return assignAuthorityScope{Issue: parent, Kind: scopeKindChildOf}
}

// scopeExistingIssue 把指派绑到正在被更新的 issue 上。
func scopeExistingIssue(issue *db.Issue) assignAuthorityScope {
	if issue == nil {
		return assignAuthorityScope{}
	}
	return assignAuthorityScope{Issue: issue, Kind: scopeKindExistingIssue}
}

// scopeNewTopLevelIssue 标记一个无父创建，此时被指派的 issue 尚不存在。
func scopeNewTopLevelIssue() assignAuthorityScope {
	return assignAuthorityScope{Kind: scopeKindNewTopLevelIssue}
}

// scopeNoDelegation 完全禁用 autopilot 回退——给那些只以 member 身份运行、绝不能
// 长出 agent 借用路径的入口点用。
func scopeNoDelegation() assignAuthorityScope {
	return assignAuthorityScope{}
}

// effectiveInvocationAuthorityFromRequest 返回 canInvokeAgent 使用的人类主体，
// 且不改变归因。真实的链顶 originator 总是赢。只有「无法归因的 AGENT 请求」才
// 可能回退到 autopilot 权威，且仅限调用方背书过的 scope 内。
func (h *Handler) effectiveInvocationAuthorityFromRequest(r *http.Request, scope assignAuthorityScope, actorType, actorID, workspaceID string) string {
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	if originatorUserID != "" {
		return originatorUserID
	}
	return h.autopilotAssignAuthorityFromRequest(r, scope, actorType, actorID, workspaceID)
}

// autopilotAssignAuthorityFromRequest 为「无法归因的 agent 运行」做出的指派解析
// 被借用的权威，按优先级：
//
//  1. 发言任务「自己」的可追责人类（autopilotTaskAssignAuthority）——精确、已记在
//     任务行上，对 autopilot 的两种执行模式都可用；
//  2. autopilot 的 member 创建者（autopilotDelegationAuthority, MUL-4857）——更
//     粗的既有规则，只保留在已上线的地方：在 `origin_type=autopilot` 的 issue 下
//     创建子项，让早于归因打点的 leader 任务不丢今天已有的路径。
//
// (2) 刻意不对新接线的表面开放。它自身不做任何活性或归因检查，把它扩到 assign
// 动词上，会让已完成任务、`owner_fallback` 任务或完全没有归因的任务在 autopilot
// 创建者恰好有权限时去指派 private agent——比本修复所需严格更宽的授权。请求路径
// 还额外要求发言任务仍处于 `running`；评论重放解析器保持各自的语义、不受影响。
func (h *Handler) autopilotAssignAuthorityFromRequest(r *http.Request, scope assignAuthorityScope, actorType, actorID, workspaceID string) string {
	if actorType != "agent" || scope.Kind == scopeKindNone {
		return ""
	}
	task, ok := h.taskFromRequestHeader(r)
	if !ok {
		return ""
	}
	// 已结束的运行在任何分支下都不再经 HTTP 出借权威。
	if task.Status != "running" {
		return ""
	}
	if user := h.autopilotTaskAssignAuthority(r.Context(), scope, actorType, actorID, workspaceID, task); user != "" {
		return user
	}
	if scope.Kind != scopeKindChildOf || scope.Issue == nil {
		return ""
	}
	return h.autopilotDelegationAuthority(r.Context(), *scope.Issue, actorType, actorID, task)
}

// autopilotTaskAssignAuthority 为「无法归因的 autopilot 运行」做出的指派解析有效
// 调用人类，键在运行「自己」的可追责人类而非 autopilot 创建者上（MUL-6691）。
//
// 为什么用 accountable user 而不是 autopilot.created_by_id：自 MUL-4302 起，
// schedule/webhook 运行的可追责人类是 trigger_owner——最后一个塑造该触发触发器
// 的人——不必是 autopilot 最初的创建者。用创建者做授权、而审计轨迹点名触发器
// 所有者，会让审计记一个人、private-agent/OAuth 访问却来自另一个人。任务行已带
// 解析好的值，所以这里直接读它，而不是重推一个不同的值。
//
// 安全。门禁不变，变的只是递给它的人类。以下条件必须全部成立，任何缺失、不匹配
// 或读不到都返回 "" 让 canInvokeAgent 失败关闭：
//
//   - actor 是 agent 且正是该任务所属的 agent；
//   - 任务仍 `running`——结束的运行不能再出借权限；
//   - 任务不带 originator（真实 originator 被调用方优先采用，绝不能被这个更粗的
//     值悄悄替换）；
//   - originator_source 为 `trigger_owner` 或 `rule_owner`。这就是 autopilot
//     血统证明：只有 autopilot 入队路径打这两个标签。`owner_fallback` 刻意排除
//     ——那个值是 AGENT OWNER，借它会让任何无法归因的 autopilot 运行调用自己
//     owner 的 private agent，正是 canInvokeAgent 要防的 owner 白名单绕过。
//     `delegation` 也排除，让权威不向后代运行传播；
//   - 指派绑在该运行可验证拥有的工作上——对既有 issue 是
//     issueBoundToAutopilotTask（对 run_only 创建的 issue 会再次证明 run_only
//     血统，create_issue 任务无法借全新顶层 issue 逃出它同一 issue 的绑定），对
//     无父 issue 创建则是任务自己经核验的 autopilot 运行；
//   - issue（若有）在请求的 workspace 内；
//   - 可追责人类仍是该 workspace 的成员。
//
// 返回的 id 仅用于 AUTHORIZATION：新建的 issue 与其入队的任何任务都另行归因、
// 保持不可归因，从而保留「没有人类直接发起」的审计语义。
func (h *Handler) autopilotTaskAssignAuthority(ctx context.Context, scope assignAuthorityScope, actorType, actorID, workspaceID string, task db.AgentTaskQueue) string {
	if actorType != "agent" {
		return ""
	}
	if !task.AgentID.Valid || uuidToString(task.AgentID) != actorID {
		return ""
	}
	if task.Status != "running" {
		return ""
	}
	if task.OriginatorUserID.Valid || !task.AccountableUserID.Valid {
		return ""
	}
	if !task.OriginatorSource.Valid {
		return ""
	}
	switch attribution.Source(task.OriginatorSource.String) {
	case attribution.SourceTriggerOwner, attribution.SourceRuleOwner:
	default:
		return ""
	}

	switch scope.Kind {
	case scopeKindChildOf, scopeKindExistingIssue:
		if scope.Issue == nil {
			return ""
		}
		if uuidToString(scope.Issue.WorkspaceID) != workspaceID {
			return ""
		}
		if !h.issueBoundToAutopilotTask(ctx, *scope.Issue, task, workspaceID) {
			return ""
		}
	case scopeKindNewTopLevelIssue:
		if !h.taskRunsAutopilotInWorkspace(ctx, task, workspaceID) {
			return ""
		}
	default:
		return ""
	}

	accountable := uuidToString(task.AccountableUserID)
	if accountable == "" {
		return ""
	}
	if _, err := h.getWorkspaceMember(ctx, accountable, workspaceID); err != nil {
		return ""
	}
	return accountable
}

// issueBoundToAutopilotTask 判断 `task` 是否可验证地拥有 `issue` 上的工作——这
// 正是让被借用的 autopilot 权威不至于变成工作区级能力的东西（MUL-4857 确立的
// confused-deputy 边界）。两种形态，每种对应一种 autopilot 执行模式：
//
//   - create_issue：autopilot 创建了该 issue（origin_type=autopilot），且发言任务
//     是正在它上面工作的运行（task.issue_id == issue.id）——正是 MUL-4857 的绑定。
//   - run_only：不存在 autopilot issue，因此绑定是「作者身份」。issue 由本任务
//     创建（CreateIssue 记为 origin_type=agent_create + origin_id=<创建任务 id>，
//     MUL-4305），且任务必须独立证明 run_only 血统（它自己的 autopilot 运行在本
//     工作区内）。没有后半截，create_issue 模式的任务只要新建一个顶层 issue——
//     它会打上自己的任务 id——再指派它，就能逃出 `task.issue_id == issue.id` 的
//     绑定。用任务 id 而非 autopilot id，还能阻止同一 autopilot 的两次并发运行
//     在彼此的 issue 上互借，且无需迁移或回填。
//
// 因此 run_only leader 只够得到自己创建的 issue（以及经子 scope 到达它们的子项）
// ——绝不碰既存或外来的 issue。create_issue leader 只够得到 autopilot 自己的
// issue。
func (h *Handler) issueBoundToAutopilotTask(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, workspaceID string) bool {
	if !issue.OriginType.Valid || !issue.OriginID.Valid || !issue.ID.Valid {
		return false
	}
	switch issue.OriginType.String {
	case "autopilot":
		return task.IssueID.Valid && uuidToString(task.IssueID) == uuidToString(issue.ID)
	case "agent_create":
		if !task.ID.Valid || uuidToString(issue.OriginID) != uuidToString(task.ID) {
			return false
		}
		return h.taskRunsAutopilotInWorkspace(ctx, task, workspaceID)
	}
	return false
}

// taskRunsAutopilotInWorkspace 验证 run_only 血统：任务必须属于某个 autopilot
// RUN，且该 autopilot 住在请求的 workspace 里。既用于还没有可绑定的 issue 时，
// 也用于绑到 run_only 创建的 issue 时。create_issue 模式的 leader 任务不带
// autopilot_run_id（它走普通 issue 指派路径入队），所以在这里永远不合格，必须
// 走 `origin_type=autopilot` 绑定。
func (h *Handler) taskRunsAutopilotInWorkspace(ctx context.Context, task db.AgentTaskQueue, workspaceID string) bool {
	if !task.AutopilotRunID.Valid {
		return false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false
	}
	run, err := h.Queries.GetAutopilotRun(ctx, task.AutopilotRunID)
	if err != nil || !run.AutopilotID.Valid {
		return false
	}
	// 工作区作用域读取：来自其它租户的 autopilot 运行在这里解析不到 autopilot，
	// 因此外来 run id 无法出借权威（MUL-4252）。
	if _, err := h.Queries.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{
		ID:          run.AutopilotID,
		WorkspaceID: wsUUID,
	}); err != nil {
		return false
	}
	return true
}

// autopilotDelegationAuthority 为 A2A invoke 门禁（canInvokeAgent）解析有效调用
// 人类——当触发评论由「无法归因的 autopilot 派发」在它自己创建的那条 issue 上
// 中继式 @ 委托时（MUL-4857）。
//
// schedule/webhook 的 autopilot 运行按设计（MUL-4302）不带链顶人类 originator。
// 没有它，canInvokeAgent 对 DEFAULT private agent（以及 member 作用域的 public_to
// agent）失败关闭，于是运行中 @ 委托静默地什么都不入队——尽管同一个 autopilot 的
// 首次派发曾经 autopilot 创建者被放行（autopilotAdmitInvoke ->
// canCreatorInvokeAgent）。这里正是为中继委托路径恢复那首次派发的权威：门禁仍然
// 会跑，只是键改成 autopilot 创建者——不会重新打开无约束的 agent-to-agent 绕过。
//
// 安全（confused-deputy 防御，评审见 MUL-4857）：创建者的权威只在「发言运行被
// 验证为正在这个 autopilot 创建的 issue 上做工作」时授予。仅凭 issue 血统 + 空
// originator 不够——在别的 issue 上跑任务的 agent 也可以合法地在这里评论，且自
// MUL-6490 起这种跨 issue 血统确实持久化在 source_task_id 上（人类 originator 链
// 能跨跳存活），否则它只要在该 autopilot 的 issue 上 @ 一句，就能借到陌生
// autopilot 创建者的 invoke 权。下面 task.issue_id 的检查就是关死它的闸：本函数
// 是 same-issue 要求的唯一所有者，这正是打点自身不再携带该要求的原因。
// `task` 因而必须来自服务端可信源——create/preview 的 X-Task-ID 头，或
// reconcile/edit 时存储的 comment.source_task_id——绝不能是客户端字段，且只有在
// 以下全部成立时才授予权威：
//   - 评论作者是 agent 且正是该任务的 agent；
//   - issue 是 autopilot 起源（origin_type=autopilot、origin_id 已设）；
//   - 发言任务正在这个 issue 上运行（task.issue_id == issue.id）。
//
// 最后一条是承重的：每条 issue_id 是这个 autopilot issue 的无法归因 agent 任务，
// 都是该 autopilot 发起的这份工作的一部分（派发的 leader 任务，或它 @ 出来的
// 后代），而外来运行的 task 带不同的 issue_id、会被拒绝。注意我们不以
// autopilot_run_id 为键：create_issue 模式（即报告里的场景）的 leader 任务走普通
// issue 指派路径入队、不带 autopilot_run_id——运行靠自己的 issue_id 而非任务的
// 往回链接。
//
// 任何不匹配、缺失血统或查询错误都返回 ""，门禁保持失败关闭。只有 MEMBER 创建
// 的 autopilot 才产出用户 id；agent 创建的 autopilot 没有可作门禁键的人类，而
// canInvokeAgent 里既有的 agent-actor workspace-target 例外已覆盖它该覆盖的那
// 一种情况（public_to workspace）。返回的 id 仅用于 AUTHORIZATION——入队任务的
// originator/归因另行计算、保持不可归因。
func (h *Handler) autopilotDelegationAuthority(ctx context.Context, issue db.Issue, authorType, authorID string, task db.AgentTaskQueue) string {
	if authorType != "agent" {
		return ""
	}
	if !issue.OriginType.Valid || issue.OriginType.String != "autopilot" || !issue.OriginID.Valid {
		return ""
	}
	// 发言运行必须由该 agent 作者发起、且正在这个 autopilot issue 上做工作——而
	// 不是一条仅仅在此评论的外来运行。
	if !task.AgentID.Valid || uuidToString(task.AgentID) != authorID {
		return ""
	}
	if !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		return ""
	}
	ap, err := h.Queries.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{
		ID:          issue.OriginID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || ap.CreatedByType != "member" || !ap.CreatedByID.Valid {
		return ""
	}
	return uuidToString(ap.CreatedByID)
}

// autopilotDelegationAuthorityFromRequest 为正在创建/预览的 HTTP 评论解析
// MUL-4857 委托权威。发言任务取自服务端可信的 X-Task-ID 头（CLI 在每次 agent
// 请求上打）；autopilotDelegationAuthority 再验证它的血统。member actor 或没有
// 命名到合法任务时返回 ""，保持门禁关闭。
func (h *Handler) autopilotDelegationAuthorityFromRequest(r *http.Request, issue db.Issue, actorType, actorID string) string {
	if actorType != "agent" {
		return ""
	}
	task, ok := h.taskFromRequestHeader(r)
	if !ok {
		return ""
	}
	return h.autopilotDelegationAuthority(r.Context(), issue, actorType, actorID, task)
}

// autopilotDelegationAuthorityFromComment 在调和对已持久化评论（取消后的重新
// 触发）时解析 MUL-4857 委托权威。发言任务取自存储的 comment.source_task_id——
// 与 CreateComment 为作者运行打下的同一条服务端可信血统——其血统由
// autopilotDelegationAuthority 验证。
func (h *Handler) autopilotDelegationAuthorityFromComment(ctx context.Context, issue db.Issue, comment db.Comment) string {
	if comment.AuthorType != "agent" || !comment.SourceTaskID.Valid {
		return ""
	}
	task, err := h.Queries.GetAgentTask(ctx, comment.SourceTaskID)
	if err != nil {
		return ""
	}
	return h.autopilotDelegationAuthority(ctx, issue, comment.AuthorType, uuidToString(comment.AuthorID), task)
}

// commentSourceTaskID 返回 agent 当前正在执行的任务 id（来自服务端可信的
// X-Task-ID 头），否则返回无效 UUID。这正是 CreateComment 打到 source_task_id
// 上的同一条血统，所以一次编辑重新打上的与「同一内容重新创作」会打上的完全
// 一致，两个解析器永远不会分歧（MUL-6490）。
//
// 该任务不要求「正在被编辑评论的 issue」上运行：这个戳记录的是「哪条运行写的
// 这个」，而一条运行可以合法地写在别的 issue 上。额外要求 same-issue 工作的权威
// 规则会自己做那个检查——autopilotDelegationAuthority 在授予 autopilot 创建者
// 的 invoke 权前验证 task.issue_id == issue.id，因此跨 issue 血统在那里依然失败
// 关闭（MUL-4857）。
func (h *Handler) commentSourceTaskID(r *http.Request) pgtype.UUID {
	task, ok := h.taskFromRequestHeader(r)
	if !ok {
		return pgtype.UUID{}
	}
	return task.ID
}

// taskFromRequestHeader 从 X-Task-ID 头（CLI 每次请求都打）解析 agent 当前执行
// 的任务。头缺失、畸形或指向不存在的任务时返回 ok=false。
func (h *Handler) taskFromRequestHeader(r *http.Request) (db.AgentTaskQueue, bool) {
	taskIDHeader := r.Header.Get("X-Task-ID")
	if taskIDHeader == "" {
		return db.AgentTaskQueue{}, false
	}
	taskUUID, err := util.ParseUUID(taskIDHeader)
	if err != nil {
		return db.AgentTaskQueue{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		return db.AgentTaskQueue{}, false
	}
	return task, true
}

// accessibleAgentIDs 返回工作区内 actor 被允许看到的 agent id 集合，供需要滤掉
// 成员访问不到的 private / 未上白名单 agent 的工作区级聚合端点（运行计数、活动
// 直方图、任务快照）使用。出错返回 nil 与 false。
func (h *Handler) accessibleAgentIDs(ctx context.Context, workspaceID, actorType, actorID, role string) (map[string]struct{}, bool) {
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, false
	}
	agents, err := h.Queries.ListAllAgents(ctx, wsUUID)
	if err != nil {
		return nil, false
	}
	targetsByAgent, ok := h.loadInvocationTargetsByAgent(ctx, agents)
	if !ok {
		return nil, false
	}
	allowed := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if actorType == "member" {
			if !memberAllowedToViewAgent(a, targetsByAgent[uuidToString(a.ID)], actorID, role) {
				continue
			}
		}
		allowed[uuidToString(a.ID)] = struct{}{}
	}
	return allowed, true
}

// restrictedAgentIDs 返回「存在于」工作区但绝不能被点名的 agent id。无法直接
// 丢弃一行的聚合端点（dashboard rollup——其按 agent 的总数必须与并排渲染的
// 工作区级序列持续对账）用它把这些 agent 折叠到单个哨兵上。
//
// 两类群体因两种不同原因落进集合：
//
//  1. 每个 `kind != "user"` 的 agent——agent builder 会话背后的隐藏执行载体。
//     这些对「所有人」都受限，owner 与 admin 也不例外：没有列表端点返回它们
//     （ListAgents / ListAllAgents 按 kind 过滤），于是任何客户端都解析不到名字，
//     然而它们跑真实任务、记真实用量，rollup 同样聚合。放任不管它们会以裸 UUID
//     现身，其开销与失败画像会归到打开该 builder 会话的人头上。
//  2. 仅对普通成员：他们不能查看的 user agent。
//
// 硬删除的 agent 刻意不进集合：它们已不在 agent 表里，没有可见性需要保护，
// dashboard 把她们渲染成独立的「deleted agents」桶——把她们折进来会把真实删除
// 重新标记成权限边界。
//
// 由于 (1)，本函数总是读 agent 表，但对能看见全部 user agent 的 actor（agent
// actor、工作区 owner/admin）跳过按 agent 的 invocation-target 查询——那里面没有
// 任何东西能改变它们的答案。
func (h *Handler) restrictedAgentIDs(ctx context.Context, workspaceID, actorType, actorID, role string) (map[string]struct{}, bool) {
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, false
	}
	agents, err := h.Queries.ListAllAgentsAnyKind(ctx, wsUUID)
	if err != nil {
		return nil, false
	}

	// 按 agent 的可见性对这位 actor 是否还能限制任何东西。
	judgeUserAgents := actorType == "member" && !roleAllowed(role, "owner", "admin")
	var targetsByAgent map[string][]db.AgentInvocationTarget
	if judgeUserAgents {
		var ok bool
		if targetsByAgent, ok = h.loadInvocationTargetsByAgent(ctx, agents); !ok {
			return nil, false
		}
	}

	restricted := make(map[string]struct{})
	for _, a := range agents {
		if a.Kind == "user" {
			if !judgeUserAgents ||
				memberAllowedToViewAgent(a, targetsByAgent[uuidToString(a.ID)], actorID, role) {
				continue
			}
		}
		restricted[uuidToString(a.ID)] = struct{}{}
	}
	return restricted, true
}

// loadInvocationTargetsByAgent 为一组 agent 批量加载 invocation targets 并按
// agent id 字符串分桶，避免列表/聚合路径逐个查询。
func (h *Handler) loadInvocationTargetsByAgent(ctx context.Context, agents []db.Agent) (map[string][]db.AgentInvocationTarget, bool) {
	ids := make([]pgtype.UUID, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	out := make(map[string][]db.AgentInvocationTarget, len(agents))
	if len(ids) == 0 {
		return out, true
	}
	rows, err := h.Queries.ListAgentInvocationTargetsByAgentIDs(ctx, ids)
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		aid := uuidToString(row.AgentID)
		out[aid] = append(out[aid], row)
	}
	return out, true
}

// canEnqueueSquadLeader 判断给定 actor 能否触发 squad 的 private leader。它加载
// leader agent 并委托给 canInvokeAgent，让 leader 触发路径与直接指派/提及一样遵守
// 调用权限。非公开 leader 要求 owner/白名单；system 发起的触发（如 github
// webhook）按 system 主体判定（仅 workspace target）。
func (h *Handler) canEnqueueSquadLeader(ctx context.Context, leaderID pgtype.UUID, actorType, actorID, originatorUserID, workspaceID string) bool {
	agent, err := h.Queries.GetAgent(ctx, leaderID)
	if err != nil {
		return false
	}
	return h.canInvokeAgent(ctx, agent, actorType, actorID, originatorUserID, workspaceID)
}
