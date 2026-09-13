// issue_trigger.go —— issue 写入是否会启动 agent 运行的单一判定（预览与写路径共享），替换此前四处漂移的副本。
package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RunEnqueueSource 标识哪种 issue 写入会启动 agent 运行。它会出现在预览响应里，让 UI 能解释每个触发。
type RunEnqueueSource string

const (
	// RunSourceAssign 覆盖 issue 创建与指派变更——issue 被交给 agent/群组。指派进 backlog 时静默停放。
	RunSourceAssign RunEnqueueSource = "assign"
	// RunSourceStatus 覆盖把已指派的 issue 从 backlog 提升到活跃状态。
	RunSourceStatus RunEnqueueSource = "status"
)

// IssueTriggerProbe 携带 WillEnqueueRun 单凭 issue 状态解析不了的请求级检查。
//
// CanAccessAgent 是私有 agent 闸门。写路径在 HTTP 边界强制它（assign 的 validateAssigneePair、群组入队助手里
// 的 canEnqueueSquadLeader），因此传入放行一切的 probe，避免闸门被复制或沉进 service 层。预览传真实闸门，
// 绝不向看不到它的成员泄露私有 agent 的就绪态。nil 函数视为放行一切。
//
// IsSelfLoop 判断把该 issue 提出 backlog 是否等于“调用 agent 重新触发自己正在跑的任务”。只有 status 来源
// 查询它。nil 表示 “非自环”。
//
// SuppressActiveSelfAssignment 判断一次直接 agent 指派是否属于“可信 agent 在 (issue, agent) 对已有非终态任务时
// 替自己声明所有权”。所有权更新仍成功；只抑制重复入队。跨 issue 交接给新目标仍可运行。nil 表示 “不抑制”。
type IssueTriggerProbe struct {
	CanAccessAgent               func(agent db.Agent) bool
	IsSelfLoop                   func() bool
	SuppressActiveSelfAssignment func(agentID pgtype.UUID) bool
}

// IssueTriggerInput 以“写后”形态描述一次预期的 issue 写入。AssigneeChanged / StatusChanged 标记写入动了哪些字段；
// IsCreate 标记全新 issue（没有可取消的旧任务、也没有自环）。
type IssueTriggerInput struct {
	Issue           db.Issue
	PrevStatus      string
	IsCreate        bool
	AssigneeChanged bool
	StatusChanged   bool
}

// IssueRunTrigger 是预览与写路径共享的解析结论。AgentID 是真正要跑的 agent——agent issue 的指派者、群组 issue
// 的群组长。
type IssueRunTrigger struct {
	IssueID      pgtype.UUID
	AgentID      pgtype.UUID
	AssigneeType string
	Source       RunEnqueueSource
}

// allowAllAgents 是放行一切的默认 probe（私有 agent 闸门由调用方在 HTTP 边界执行时用）。
func allowAllAgents(db.Agent) bool { return true }

// WillEnqueueRun 是回答 “这次 issue 写入会不会启动 agent 运行、为谁启动” 的单一谓词。它是 issue 更新 / 批量更新
// 写路径与预览端点共享的事实来源，取代了四处漂移的副本（漏掉 squad、漏掉自环、四个入口不一致——见 MUL-3375）。
//
// 它刻意与评论触发（指派者回退评论路由）是不同的谓词：issue 写入在 backlog 停放，而评论在任何状态都会触发。
// 两者只共享叶子就绪检查（AgentReadiness、pending 任务去重），不共享顶层决策。
//
// 决策必须与真实入队条件一致，预览才不会宣称一个写路径随后丢弃的全新运行。写经 CreateAgentTask 入队，
// 由 (issue_id, agent_id) 部分唯一索引（作用于 queued/dispatched pending 任务）守卫；下方的 pending 检查镜像该
// 守卫，且只有 status 来源需要它：
//   - status 来源（backlog → active）可能对着已持有 pending 任务的指派者再触发（例如该 issue 停在 backlog 时
//     @mention 起的任务）；该检查让预览不承诺会被唯一索引合并掉的运行。
//   - assign 来源（create / 指派变更）跳过该检查：create 面向无旧任务的新 issue；改派不再取消既有任务
//     （#4963 / MUL-4113）——罕见情况下新指派者已持有 pending 任务，插入只是在同一唯一索引上 no-op，
//     指派者最终仍只有一条 pending 运行。
func (s *IssueService) WillEnqueueRun(ctx context.Context, in IssueTriggerInput, probe IssueTriggerProbe) (IssueRunTrigger, bool) {
	issue := in.Issue
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return IssueRunTrigger{}, false
	}
	canAccess := probe.CanAccessAgent
	if canAccess == nil {
		canAccess = allowAllAgents
	}

	// status 来源还要求【离开】backlog 类目，而不只是换状态键。自定义状态之前，“键离开 `backlog`”总是类目变化，
	// 两者是同一条件；现在 `backlog` → 仍属 `backlog` 类目的自定义状态只是停车位内部移动，就此启动运行会破坏
	// backlog 的唯一承诺（MUL-6463）。
	//
	// 转换两侧都归一化到其继承的规范状态：`backlog` 类目里的自定义状态与 Backlog 一样停放，`todo` 类目里的
	// 自定义状态与 Todo 一样启动运行。内置键无需查询即解析为自身，使该决策对无自定义状态的工作区逐位一致——
	// 在管理员定义自定义状态之前所有工作区都是如此（MUL-6243）。
	currentStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)
	prevStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, in.PrevStatus)

	var source RunEnqueueSource
	switch {
	case in.IsCreate || in.AssigneeChanged:
		// backlog 是停车场：指派进 backlog 从不启动运行。
		if currentStatus == "backlog" {
			return IssueRunTrigger{}, false
		}
		source = RunSourceAssign
	case in.StatusChanged && prevStatus == "backlog" &&
		currentStatus != "backlog" &&
		currentStatus != "done" && currentStatus != "cancelled":
		if probe.IsSelfLoop != nil && probe.IsSelfLoop() {
			return IssueRunTrigger{}, false
		}
		source = RunSourceStatus
	default:
		return IssueRunTrigger{}, false
	}

	switch issue.AssigneeType.String {
	case "agent":
		agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
		if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
			return IssueRunTrigger{}, false
		}
		if !canAccess(agent) {
			return IssueRunTrigger{}, false
		}
		if source == RunSourceAssign && !in.IsCreate && probe.SuppressActiveSelfAssignment != nil &&
			probe.SuppressActiveSelfAssignment(issue.AssigneeID) {
			return IssueRunTrigger{}, false
		}
		if source == RunSourceStatus && s.hasPendingRun(ctx, issue.ID, issue.AssigneeID) {
			return IssueRunTrigger{}, false
		}
		return IssueRunTrigger{
			IssueID:      issue.ID,
			AgentID:      issue.AssigneeID,
			AssigneeType: "agent",
			Source:       source,
		}, true

	case "squad":
		// 对 (issue, agent) 对的自我指派抑制刻意只作用于直接 agent 所有权。指派群组会改变执行上下文（组长 briefing、
		// 角色、成员路由），因此即使操作 agent 正是该群组长，也是有意的群组交接而非冗余的直接自我声明。下方 status
		// 路径仍用组长的 pending 任务守卫。
		squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return IssueRunTrigger{}, false
		}
		leader, err := s.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil {
			return IssueRunTrigger{}, false
		}
		verdict, err := AgentReadiness(ctx, s.runtimeLookup(s.Queries), leader)
		if err != nil || !verdict.Ready() {
			return IssueRunTrigger{}, false
		}
		if !canAccess(leader) {
			return IssueRunTrigger{}, false
		}
		if source == RunSourceStatus && s.hasPendingRun(ctx, issue.ID, squad.LeaderID) {
			return IssueRunTrigger{}, false
		}
		return IssueRunTrigger{
			IssueID:      issue.ID,
			AgentID:      squad.LeaderID,
			AssigneeType: "squad",
			Source:       source,
		}, true
	}
	return IssueRunTrigger{}, false
}

// hasPendingRun 判断 agent 是否已为该 issue 持有 queued/dispatched 任务（(issue_id, agent_id) 唯一索引槽位）。
// 错误按 fail closed 处理成 “有 pending”，预览便不会过度承诺运行。
func (s *IssueService) hasPendingRun(ctx context.Context, issueID, agentID pgtype.UUID) bool {
	pending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
		// 按被评审 head 做去重键：HEAD 推进后，针对旧 HEAD 的 pending run 不会遮蔽新请求（TEN-356）。
		HeadSha: headShaText(s.TaskService.ResolveIssueReviewSHA(ctx, issueID)),
	})
	if err != nil {
		return true
	}
	return pending
}
