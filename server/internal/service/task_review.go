// 审查任务的入队实现。审查任务挂在被审 issue 上，但没有触发评论：轮次上下文通过
// handoff_note 带进开场，审查规范来自托管 Agent 的 instructions。
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// EnqueueIssueReviewTask 为托管审查 Agent 创建一次审查运行。ownerUserID 是启用审查的
// 成员，作为本次运行的负责人类（审查不是某个人委托的动作，但仍需可追溯的人类归属）。
// 与提及/指派路径一样，重复入队由 (issue_id, agent_id) pending 唯一索引兜底。
func (s *TaskService) EnqueueIssueReviewTask(ctx context.Context, issue db.Issue, agentID, ownerUserID pgtype.UUID, handoff, headSHA string) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load review agent: %w", err)
	}
	if agent.WorkspaceID != issue.WorkspaceID || agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("review agent is unavailable")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("review agent has no runtime")
	}
	runtime, err := s.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load review runtime: %w", err)
	}
	if runtime.Provider != "codex" && runtime.Provider != "codebuddy" {
		return db.AgentTaskQueue{}, fmt.Errorf("代码审查只支持 Codex 或 CodeBuddy 运行时")
	}
	ready, err := AgentReadiness(ctx, s.runtimeLookup(), agent)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("check review agent readiness: %w", err)
	}
	if !ready.Ready() {
		return db.AgentTaskQueue{}, fmt.Errorf("审查 Agent 暂不可用：%s", ready.Detail)
	}

	attr := attribution.TriggerOwner(ownerUserID, attribution.EvidenceKind("issue_review"), issue.ID)
	src, delegatedFrom, evidenceKind, evidenceRef := attributionCreateParams(attr)
	createParams := db.CreateAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              agent.ID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		HandoffNote:          pgtype.Text{String: handoff, Valid: handoff != ""},
		HeadSha:              headShaText(headSHA),
		OriginatorUserID:     attr.UserID,
		AccountableUserID:    attr.AccountableUserID,
		OriginatorSource:     src,
		DelegatedFromTaskID:  delegatedFrom,
		TriggerEvidenceKind:  evidenceKind,
		TriggerEvidenceRefID: evidenceRef,
	}

	var task db.AgentTaskQueue
	var queuedEvent events.Event
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		var createErr error
		task, createErr = qtx.CreateAgentTask(ctx, createParams)
		if createErr != nil {
			return createErr
		}
		if s.TxStarter != nil {
			queuedEvent, createErr = RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(issue.WorkspaceID), task)
			if createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		if isDuplicatePendingTaskErr(err) {
			slog.Debug("review task enqueue coalesced: pending task already exists",
				"issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agent.ID))
			return db.AgentTaskQueue{}, ErrDuplicatePendingTask
		}
		return db.AgentTaskQueue{}, fmt.Errorf("create review task: %w", err)
	}

	slog.Info("review task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(agent.ID),
	)
	// 顺序与提及/指派路径一致：先广播 queued，再唤醒 daemon。
	if queuedEvent.ID != "" {
		publishCommittedEvent(s.Bus, queuedEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	}
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}
