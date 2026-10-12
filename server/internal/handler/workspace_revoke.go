// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace_revoke.go）实现"成员离开/被移出 workspace"时一次性收敛
// 全部服务端状态的内部流程：撤销其名下所有 runtime、归档相关 agent、取消在途
// 任务、清理订阅与凭据并删除成员行。核心是 revokeAndRemoveMember（单事务完成
// 所有写库）与配套的提交后事件发布/日志辅助，由 DeleteMember（管理员踢人）和
// LeaveWorkspace（自己退出）两个 handler 共用。
package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// revokeAndRemoveMember 把"成员离开 workspace"之后该收敛的服务端状态一次性
// 做完：他拥有的每个 runtime 都被强制下线（unusable）；钉在这些 runtime 上的
// 每个 agent 被归档；这些 runtime 上的每个在途任务被取消——选择"取消"而不是
// "失败"，是为了让 daemon 的任务状态轮询器能优雅地打断正在运行的 agent；
// 同时删除该成员的持久订阅与 daemon_token 行；最后删掉成员行本身。
//
// 所有 DB 写操作都在同一个事务里完成，这样部分撤销永远不会让 workspace 处于
// 半收敛状态——比如"成员看起来没了，但 runtime 行还活着"。事务提交后，才作废
// daemon_token 缓存并发布事件（见 publishRevocation），让已连接的客户端和其余
// workspace 成员立刻看到新状态。
//
// 范围说明：这里撤销的是 owner_id 与 userID 匹配的每个 runtime，无论 daemon
// 以何种方式认证。目前多数 daemon 回退用 PAT/JWT，daemon_token 行在生产中
// 并未使用，删它们对这类 daemon 是空操作，但等 mdt_ 流程上线后会真正生效。
// 无论哪种方式，"归档 agent + 取消任务 + 强制下线"的写操作才是生产环境真正的
// 安全网：即使 daemon 拿着仍然有效的 PAT 抢着重连上线，也会发现自己没有任何
// 可运行的 agent、没有可认领的排队任务，而分发器（gate 在 agent.archived_at
// IS NULL 上）也不会再派新活给它——同一事务里删除成员行，意味着后续
// requireWorkspaceMember 校验会对该 daemon 用 PAT 认证的请求直接回 404。
//
// archivedBy 是触发这次撤销的操作者：DeleteMember 场景下是请求者（踢人的
// 管理员），LeaveWorkspace 场景下是离开者本人。
func (h *Handler) revokeAndRemoveMember(ctx context.Context, workspaceID, userID, memberID, archivedBy pgtype.UUID) (revocationResult, error) {
	var empty revocationResult

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)

	// 必须在事务触碰 member / issue_subscriber 之前就取这把锁。代理式
	// auto-subscribe 规则会加同一把 (workspace, user) 锁，所以正在拆解过程中的
	// 一次运行不可能在本事务"删成员"和"清订阅"之间再塞进一行新订阅。排在最前
	// 也保证所有持锁方按相同顺序取锁，这些路径之间就不会互相死锁（MUL-5483
	// 第 7 轮评审结论）。
	if err := qtx.LockSubscriberWrites(ctx, db.LockSubscriberWritesParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return empty, err
	}

	runtimes, err := qtx.ListAgentRuntimesByOwner(ctx, db.ListAgentRuntimesByOwnerParams{
		WorkspaceID: workspaceID,
		OwnerID:     userID,
	})
	if err != nil {
		return empty, err
	}

	result := revocationResult{Runtimes: runtimes}

	if len(runtimes) > 0 {
		runtimeIDs := make([]pgtype.UUID, len(runtimes))
		daemonIDs := make([]string, 0, len(runtimes))
		for i, rt := range runtimes {
			runtimeIDs[i] = rt.ID
			if rt.DaemonID.Valid && rt.DaemonID.String != "" {
				daemonIDs = append(daemonIDs, rt.DaemonID.String)
			}
		}

		result.ArchivedAgents, err = qtx.ArchiveAgentsByRuntime(ctx, db.ArchiveAgentsByRuntimeParams{
			ArchivedBy: archivedBy,
			RuntimeIds: runtimeIDs,
		})
		if err != nil {
			return empty, err
		}

		// 同时按 runtime 和已归档 agent 双维度取消任务。agent.runtime_id 会被
		// UpdateAgent 重新指派，而历史 agent_task_queue 行上的 runtime_id 不会
		// 跟着改写，所以已归档 agent 仍可能有钉在另一个 runtime 上的排队/运行中
		// 任务；ClaimAgentTask 又不过滤 agent.archived_at，若不处理这些任务在
		// agent 归档后仍然可被认领。
		archivedAgentIDs := make([]pgtype.UUID, len(result.ArchivedAgents))
		for i, a := range result.ArchivedAgents {
			archivedAgentIDs[i] = a.ID
		}
		result.CancelledTasks, err = qtx.CancelAgentTasksByRuntimeOrAgent(ctx, db.CancelAgentTasksByRuntimeOrAgentParams{
			RuntimeIds: runtimeIDs,
			AgentIds:   archivedAgentIDs,
		})
		if err != nil {
			return empty, err
		}
		if err = service.SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, result.CancelledTasks...); err != nil {
			return empty, err
		}

		result.OfflineRuntimeIDs, err = qtx.ForceOfflineRuntimesByIDs(ctx, runtimeIDs)
		if err != nil {
			return empty, err
		}

		if len(daemonIDs) > 0 {
			result.RevokedTokenHashes, err = qtx.DeleteDaemonTokensByWorkspaceAndDaemons(ctx, db.DeleteDaemonTokensByWorkspaceAndDaemonsParams{
				WorkspaceID: workspaceID,
				DaemonIds:   daemonIDs,
			})
			if err != nil {
				return empty, err
			}
		}
	}

	// channel_user_binding 过去带一条 ON DELETE CASCADE 的成员外键，被移除成员
	// 的 IM 绑定会随之自动消失。MUL-3515 §4 删掉了所有 channel_* 外键，把这条
	// 完整性规则移到应用层：与"删成员行"在同一事务里清理绑定。入站路径也会
	// 复查成员资格（见 ChannelStore.IsWorkspaceMember），但主动清理能避免一条
	// 过期绑定在"移除后又加回"时残留。
	if err := qtx.DeleteChannelUserBindingsByWorkspaceMember(ctx, db.DeleteChannelUserBindingsByWorkspaceMemberParams{
		WorkspaceID:   workspaceID,
		MulticaUserID: userID,
	}); err != nil {
		return empty, err
	}

	// agent_invocation_target 携带的是"面向成员"的调用授权，且没有 DB 外键
	// （MUL-3963 保持新表无外键，与 MUL-3515 的 channel 泛化口径一致）。要把
	// 这位离开成员名下的授权在"删成员行"的同一事务里清掉，否则用户被重新邀请
	// 后会悄悄收回旧的对某些 agent 的调用权限（那些 agent 曾经把他们加进
	// allow-list）。范围限定本 workspace：同一用户可能属于其他 workspace，在这
	// 里移除他不应影响他在别处 agent 上的授权。
	if err := qtx.DeleteAgentInvocationTargetsByMember(ctx, db.DeleteAgentInvocationTargetsByMemberParams{
		WorkspaceID: workspaceID,
		TargetID:    userID,
	}); err != nil {
		return empty, err
	}

	// 私有的 quick action 只有创建者自己能看、能跑（quick_action 没有外键，
	// 不会隐式被删）。创建者一离开，这些行对剩下的成员全都不可达，却仍占着
	// workspace 的活动动作配额，所以要在同一事务里清掉。公开的 action 是
	// workspace 的"公家家具"，保留。
	if err := qtx.DeletePrivateQuickActionsByCreator(ctx, db.DeletePrivateQuickActionsByCreatorParams{
		WorkspaceID: workspaceID,
		CreatedByID: userID,
	}); err != nil {
		return empty, err
	}

	// 保存的视图遵循上面私有 quick action 的规则：离开成员的私有视图对剩下
	// 所有人都不可见却仍占配额，重新邀请也不该让它们复活。共享视图保留——那是
	// 其他成员可能依赖的 workspace 公家家具。按用户记忆的视图栏偏好离开了成员
	// 就无意义，一并删除。
	if err := qtx.DeletePrivateIssueViewsByOwner(ctx, db.DeletePrivateIssueViewsByOwnerParams{
		WorkspaceID: workspaceID,
		OwnerID:     userID,
	}); err != nil {
		return empty, err
	}
	if err := qtx.DeleteIssueViewPreferencesByUser(ctx, db.DeleteIssueViewPreferencesByUserParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return empty, err
	}

	// issue_subscriber 同样没有外键（与上面两处清理同属 MUL-3515 的规则），而
	// MUL-5483 又给了 agent 一条自主写成员订阅行的路径。在本事务里清掉它们，
	// 才能阻止离开的成员继续累积收件箱消息，也阻止重新邀请后静默恢复 TA 过去
	// 关注的所有内容的可见性。
	if err := qtx.DeleteSubscriptionsByMember(ctx, db.DeleteSubscriptionsByMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return empty, err
	}

	member, err := qtx.GetMember(ctx, memberID)
	if err != nil {
		return empty, err
	}

	// autopilot_subscriber 是另一种无外键的订阅模板。残留的过期行曾导致详情
	// API 返回一个成员选择器已无法渲染的用户；接下来那次"整体替换"式 PATCH 会
	// 因成员校验失败而挂掉，从而挡住对那个 autopilot 的一切编辑。只清理本
	// workspace 下的模板，同一用户在其他 workspace 的订阅不受影响。
	if err := qtx.DeleteAutopilotSubscribersByMember(ctx, db.DeleteAutopilotSubscribersByMemberParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	}); err != nil {
		return empty, err
	}

	if err := qtx.DeleteMember(ctx, memberID); err != nil {
		return empty, err
	}

	// 上面的任务/agent/成员变更是一组"一经提交就生效"的事实集。提交前把每个
	// 事件都记下来；发布则刻意推迟——要等调用方确认事务不会回滚之后再做。
	for _, task := range result.CancelledTasks {
		event, eventErr := service.RecordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, task,
			map[string]any{"reason": "member_revoked"})
		if eventErr != nil {
			return empty, eventErr
		}
		result.Events = append(result.Events, event)
	}
	for _, agent := range result.ArchivedAgents {
		event, eventErr := service.RecordDurableEventTx(ctx, qtx, h.buildAgentDomainEvent(protocol.EventAgentArchived, agent, "member", uuidToString(archivedBy)))
		if eventErr != nil {
			return empty, eventErr
		}
		result.Events = append(result.Events, event)
	}
	memberEvent, eventErr := service.RecordDurableEventTx(ctx, qtx, buildMemberDomainEvent(
		protocol.EventMemberRemoved, member, "member", uuidToString(archivedBy), map[string]any{"action": "revoked"},
	))
	if eventErr != nil {
		return empty, eventErr
	}
	result.Events = append(result.Events, memberEvent)

	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}

	return result, nil
}

// revocationResult 汇总 revokeAndRemoveMember 实际动过的行，供调用方在事务
// 提交后分发事件和分析埋点。不能在事务内发布：那样会让订阅者观察到一份仍可能
// 回滚的状态（见 TaskService.BroadcastCancelledTasks 的文档说明）。
type revocationResult struct {
	Runtimes           []db.AgentRuntime
	ArchivedAgents     []db.Agent
	CancelledTasks     []db.AgentTaskQueue
	OfflineRuntimeIDs  []db.ForceOfflineRuntimesByIDsRow
	RevokedTokenHashes []string
	Events             []events.Event
}

// isEmpty 报告这次撤销是否什么都没动（没有 runtime 需要处理）。
func (r revocationResult) isEmpty() bool {
	return len(r.Runtimes) == 0
}

// publishRevocation 执行提交后的所有副作用：作废 daemon token 缓存、逐个广播
// task:cancelled（带按 agent 的重算）、广播 agent:archived，并通知前端刷新
// runtime 列表。对空结果调用是安全的——会直接返回。
func (h *Handler) publishRevocation(ctx context.Context, result revocationResult, workspaceIDStr, actorType, actorIDStr string) {
	if result.isEmpty() {
		return
	}

	for _, hash := range result.RevokedTokenHashes {
		h.DaemonTokenCache.Invalidate(ctx, hash)
	}

	// 按旧的内存路径相同顺序做对账和发布，但用的是 revokeAndRemoveMember 提交
	// 的封套——这样即使进程重启也能重放完全一致的变更集，而不会另造出第二份
	// 事件。
	if h.TaskService != nil {
		for _, task := range result.CancelledTasks {
			h.TaskService.CaptureCancelledTasks(ctx, []db.AgentTaskQueue{task})
			h.TaskService.ReconcileAgentStatus(ctx, task.AgentID)
		}
		h.TaskService.NotifyTasksFinished(result.CancelledTasks)
	}
	for _, event := range result.Events {
		h.publishEvent(event)
	}

	// 通知已连接客户端刷新 runtime 列表。这里借用 EventDaemonRegister 事件、带
	// 上 "revoke" action——与 runtime 删除 handler 用的是同一条通道——前端便能
	// 作废自己缓存的列表，而无须引入一个客户端需要重新认识的新事件类型。
	if len(result.OfflineRuntimeIDs) > 0 {
		h.publish(protocol.EventDaemonRegister, workspaceIDStr, actorType, actorIDStr, map[string]any{
			"action": "revoke",
		})
	}
}

// logRevocation 输出一行结构化 info 日志概括这次撤销。独立于 publish 拆出来，
// 是为了保证无论事件总线是否接线，日志内容都一致。
func logRevocation(result revocationResult, workspaceID, userID string, attrs ...any) {
	if result.isEmpty() {
		return
	}
	base := []any{
		"workspace_id", workspaceID,
		"user_id", userID,
		"runtimes_revoked", len(result.Runtimes),
		"agents_archived", len(result.ArchivedAgents),
		"tasks_cancelled", len(result.CancelledTasks),
		"runtimes_taken_offline", len(result.OfflineRuntimeIDs),
		"daemon_tokens_revoked", len(result.RevokedTokenHashes),
	}
	slog.Info("member runtimes revoked", append(base, attrs...)...)
}
