// 本文件：当某次「触发」因目标 Agent 的 CLI 无法在其机器上运行而被拒绝时，在 issue 上
// 落一条系统备注（runtime 不可用提示）。详细设计见下。
package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// noteRuntimeUnusable 在 issue 上记录「触发被拒是因为目标 Agent 的 CLI 在它的机器上跑不了」。
//
// 对每个作者都写，不只无人值守的那类。composer 里的提示块和发送后的 toast 确实能触达正在
// 输入的人，但它们只带原因码——修复命令对提示块太长、放进会消失的东西里又太重要。而
// Agent 自己发起的 @mention 根本没有人在看响应。所以两类场景都落这条持久记录，瞬态界面
// 只是它的预览而非唯一副本（MUL-6164）。
//
// 设计上就是尽力而为：拒绝本身已经发生且是否正确与这条备注无关，所以这里的失败只记日志、
// 绝不向上返回。
func (h *Handler) noteRuntimeUnusable(ctx context.Context, issue db.Issue, agent db.Agent, verdict service.AgentVerdict) {
	content := service.RuntimeUnusableNotice(agent.Name, verdict)
	// author_type='system'、author_id 为零 UUID——与子 issue 完成备注同形状；客户端按
	// author_type 分支，不关心 UUID 具体值。
	if h.TxStarter == nil {
		slog.Warn("runtime unusable notice: transaction starter unavailable", "issue_id", uuidToString(issue.ID))
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("runtime unusable notice: begin transaction failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("runtime unusable notice: create system comment failed",
			"error", err,
			"issue_id", uuidToString(issue.ID),
			"agent_id", uuidToString(agent.ID))
		return
	}
	comment := created.Comment()
	commentResp := commentToResponse(comment, nil, nil)
	commentResp.IssueRevision = created.IssueRevision
	event, err := eventoutbox.Enqueue(ctx, qtx, buildCommentCreatedEvent(issue, commentResp, "system", ""))
	if err != nil {
		slog.Warn("runtime unusable notice: enqueue event failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("runtime unusable notice: commit transaction failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	h.publishEvent(event)
}
