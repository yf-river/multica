package service

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/issuestatus"

	"github.com/multica-ai/multica/server/internal/attribution"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func delegatedRecoveryEvidenceKind(task db.AgentTaskQueue) attribution.EvidenceKind {
	if task.Status == "failed" {
		return attribution.EvidenceDelegatedFailure
	}
	return attribution.EvidenceDelegatedResult
}

// RecoverDelegatedTaskResult 从持久终态事件续接真正的源协调者，复用恢复评论、投递回执和有界重放。
// 发布暂停不是真正终止；该执行由发布窗口接续，不应额外产生协调任务。
func (s *TaskService) RecoverDelegatedTaskResult(ctx context.Context, task db.AgentTaskQueue) error {
	if task.Status != "completed" && task.Status != "cancelled" {
		return nil
	}
	if task.FailureReason.String == "release_pause" {
		return nil
	}
	_, err := s.recoverDelegatedTaskFailure(ctx, task)
	return err
}

// 已终态的真正子任务由阶段屏障统一回报，不能用执行回执提前穿透屏障或再次唤醒同一接收者。
func delegatedIssueBarrierCoversResult(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, parent db.Issue, agent db.Agent) (bool, error) {
	if task.Status == "failed" || !task.IssueID.Valid {
		return false, nil
	}
	child, err := q.GetIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if child.ParentIssueID != parent.ID {
		return false, nil
	}
	status := issuestatus.Effective(ctx, q, child.WorkspaceID, child.Status)
	if status != issuestatus.Done && status != issuestatus.Cancelled {
		return false, nil
	}
	if parent.AssigneeType.String == "agent" {
		return parent.AssigneeID == agent.ID, nil
	}
	if parent.AssigneeType.String == "squad" && parent.AssigneeID.Valid {
		squad, err := q.GetSquad(ctx, parent.AssigneeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return squad.LeaderID == agent.ID, nil
	}
	return false, nil
}
