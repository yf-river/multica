package handler

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/supervision"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// afterIssueStatusChangeForSupervision 在任务完成或重开时开关监督轮次。它和审查钩子
// 挂在同一批状态写入口：只在类别变化时动作，因此同一任务的重复状态请求不会重复建轮。
// 扫描器另有幂等补齐，覆盖服务停机期间由其他入口推进的状态变化。
func (h *Handler) afterIssueStatusChangeForSupervision(ctx context.Context, prev, issue db.Issue) {
	if h.Supervision == nil || !issue.WorkspaceID.Valid || !issue.ID.Valid {
		return
	}
	prevCategory := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, prev.Status)
	currentCategory := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status)
	if prevCategory == currentCategory {
		return
	}
	ws, id := uuidToString(issue.WorkspaceID), uuidToString(issue.ID)
	switch {
	case currentCategory == issuestatus.Done:
		// 1. 新完成轮次的截止时间取此刻：本轮只审查完成之前的执行与交付材料。
		if _, err := h.Supervision.OpenRound(ctx, ws, id, true); err != nil {
			slog.Warn("supervision: open completion round failed", "issue_id", id, "error", err)
		}
	case prevCategory == issuestatus.Done:
		// 2. 重开后旧轮次立即停止派发，已完成的结论、问题和阅读进度保留。
		if err := h.Supervision.CloseOpenRound(ctx, ws, id, supervision.RoundCloseReopened); err != nil {
			slog.Warn("supervision: close completion round failed", "issue_id", id, "error", err)
		}
	}
}
