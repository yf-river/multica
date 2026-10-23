package service

import (
	"context"

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
