// runtime_teardown.go —— runtime 拆除的共享变更阶段：删除 agent_runtime 行前解绑用户 agent、取消任务、
// 停用 autopilot、摘离任务历史并清理系统 agent 的依赖行。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var (
	// ErrRuntimeNotDrained 表示该 runtime 或它的某个绑定用户 agent 仍持有非终态任务。调用方必须中止事务，
	// 而不是删除 runtime 后指望数据库级联。
	ErrRuntimeNotDrained = errors.New("runtime still has non-terminal tasks")
	// ErrRuntimeWorkspaceMismatch 保护租户隔离：legacy 或损坏数据把 agent 绑到别的工作区的 runtime 时拦截。
	ErrRuntimeWorkspaceMismatch = errors.New("runtime agent workspace mismatch")
)

// RuntimeTeardownOptions 控制“用户确认删除”与“保留型 GC”之间唯一有意的语义差别：手动删除取消活跃工作；
// 自动 GC 必须 fail closed、保持活跃工作不动。
type RuntimeTeardownOptions struct {
	CancelNonTerminalTasks bool
}

// RuntimeTeardownResult 汇报已提交的业务对象变更，供调用方在自身事务提交后发布事件。
type RuntimeTeardownResult struct {
	UnboundAgents    []db.Agent
	CancelledTasks   []db.AgentTaskQueue
	PausedAutopilots []db.Autopilot
}

// ValidateRuntimeAgentWorkspaces 拒绝改动跨工作区的绑定。agent.runtime_id 没有组合工作区外键背书，
// 因此在 runtime 拆除改数据前必须先做这道应用层守卫。
func ValidateRuntimeAgentWorkspaces(runtime db.AgentRuntime, agents []db.Agent) error {
	for _, agent := range agents {
		if agent.WorkspaceID != runtime.WorkspaceID {
			return fmt.Errorf("%w: agent %x runtime %x", ErrRuntimeWorkspaceMismatch, agent.ID.Bytes, runtime.ID.Bytes)
		}
	}
	return nil
}

// TeardownRuntime 执行删除 agent_runtime 行前立即使用的共享变更阶段。它自行取 runtime 与绑定用户 agent 行锁；
// 已为确认检查锁过它们的调用方可在自己事务里安全地再次获取同样锁。
//
// 用户 agent 是持久业务对象，解绑后存活。系统 agent 是 runtime 本地的基建，只在修剪掉其非 FK 依赖后删除；
// 它们 runtime 本地的构建草稿与聊天草稿恢复随之删除。任务历史在 runtime 删除前摘离，避免 legacy 的
// ON DELETE CASCADE 抹掉它。
func TeardownRuntime(ctx context.Context, qtx *db.Queries, runtimeID pgtype.UUID, opts RuntimeTeardownOptions) (RuntimeTeardownResult, error) {
	var out RuntimeTeardownResult

	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if err != nil {
		return out, fmt.Errorf("load runtime: %w", err)
	}
	lockedAgents, err := qtx.ListUserAgentsByRuntimeForUpdate(ctx, runtimeID)
	if err != nil {
		return out, fmt.Errorf("lock runtime agents: %w", err)
	}
	if err := ValidateRuntimeAgentWorkspaces(runtime, lockedAgents); err != nil {
		return out, err
	}

	lockedAgentIDs := make([]pgtype.UUID, len(lockedAgents))
	for i, agent := range lockedAgents {
		lockedAgentIDs[i] = agent.ID
	}

	if opts.CancelNonTerminalTasks {
		cancelled, err := qtx.CancelAgentTasksByRuntimeOrAgent(ctx, db.CancelAgentTasksByRuntimeOrAgentParams{
			RuntimeIds: []pgtype.UUID{runtimeID},
			AgentIds:   lockedAgentIDs,
		})
		if err != nil {
			return out, fmt.Errorf("cancel tasks: %w", err)
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled...); err != nil {
			return out, err
		}
		// runtime 删除还会移除系统 agent 及其 chat 会话。在这些链接消失前记录每条取消，使 durable 事件保留原
		// 工作区与 chat 作用域。
		if _, err := enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, cancelled); err != nil {
			return out, fmt.Errorf("record cancelled task events: %w", err)
		}
		out.CancelledTasks = cancelled
	}

	undrained, err := qtx.CountUndrainedTasksByRuntimeOrAgent(ctx, db.CountUndrainedTasksByRuntimeOrAgentParams{
		RuntimeIds: []pgtype.UUID{runtimeID},
		AgentIds:   lockedAgentIDs,
	})
	if err != nil {
		return out, fmt.Errorf("count undrained tasks: %w", err)
	}
	if undrained > 0 {
		return out, fmt.Errorf("%w: %d", ErrRuntimeNotDrained, undrained)
	}

	unbound, err := qtx.UnbindUserAgentsFromRuntime(ctx, runtimeID)
	if err != nil {
		return out, fmt.Errorf("unbind agents: %w", err)
	}
	out.UnboundAgents = unbound

	unboundIDs := make([]pgtype.UUID, len(unbound))
	for i, agent := range unbound {
		unboundIDs[i] = agent.ID
	}
	paused, err := qtx.PauseAutopilotsByUnboundAgents(ctx, unboundIDs)
	if err != nil {
		return out, fmt.Errorf("pause autopilots: %w", err)
	}
	out.PausedAutopilots = paused

	if _, err := qtx.UnbindTasksFromRuntime(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("unbind task history: %w", err)
	}
	remaining, err := qtx.CountTasksByRuntime(ctx, runtimeID)
	if err != nil {
		return out, fmt.Errorf("confirm task history detached: %w", err)
	}
	if remaining != 0 {
		return out, fmt.Errorf("task history still references runtime after detach: %d", remaining)
	}

	// 这些表刻意没有 agent 外键，因此必须在系统 agent 行消失前修剪。
	if err := qtx.DeleteAgentInvocationTargetsBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up agent invocation targets: %w", err)
	}
	if err := qtx.DeleteChannelInstallationsBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up channel installations: %w", err)
	}
	if err := qtx.DeleteChatPinnedAgentsBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up chat pins: %w", err)
	}
	if err := qtx.DeleteAgentLabelAssignmentsBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up agent label assignments: %w", err)
	}
	if err := pruneRuntimeSystemAgentChatDraftRestores(ctx, qtx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up chat draft restores: %w", err)
	}
	if err := qtx.DeleteSystemAgentsByRuntime(ctx, runtimeID); err != nil {
		return out, fmt.Errorf("clean up system agents: %w", err)
	}
	return out, nil
}

// pruneRuntimeSystemAgentChatDraftRestores 在系统 agent 及其 chat 会话级联删除前，移除没有 DB 外键的行
// （聊天草稿恢复、构建草稿）。
func pruneRuntimeSystemAgentChatDraftRestores(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID) error {
	if _, err := q.LockChatSessionsBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return err
	}
	if err := q.DeleteChatDraftRestoresBySystemRuntimeAgents(ctx, runtimeID); err != nil {
		return err
	}
	return q.DeleteAgentBuilderDraftsBySystemRuntimeAgents(ctx, runtimeID)
}
