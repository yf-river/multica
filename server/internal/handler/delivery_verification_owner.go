package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// 外部验收使用同一归属租约；通知与失败处理和领取归属共享锁，覆盖首次领取的竞态。
func externalDeliveryVerificationOwner(ctx context.Context, tx pgx.Tx, ws, issue string) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "delivery-owner:"+ws+":"+issue); err != nil {
		return false, err
	}
	var external bool
	err := tx.QueryRow(ctx, `SELECT o.expires_at>now() AND NOT EXISTS(SELECT 1 FROM agent_task_queue q WHERE q.id::text=o.execution_id) FROM delivery_owner o WHERE o.workspace_id=$1 AND o.issue_id=$2 FOR UPDATE OF o`, ws, issue).Scan(&external)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return external, err
}

func deliveryVerificationRunEnded(ctx context.Context, tx pgx.Tx, ws, issue, delivery, run string) (bool, error) {
	var ended bool
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT q.id::text=$4 AND q.status IN ('failed','cancelled','blocked','completed') AND q.completed_at<now()-interval '30 seconds' FROM issue_delivery d JOIN agent_task_queue q ON q.issue_id=d.issue_id AND (q.trigger_comment_id=CASE WHEN d.phase='preparing_release' THEN d.release_comment_id ELSE d.verification_comment_id END OR CASE WHEN d.phase='preparing_release' THEN d.release_comment_id ELSE d.verification_comment_id END=ANY(q.coalesced_comment_ids) OR EXISTS(SELECT 1 FROM delivery_notice n WHERE n.delivery_id=d.id AND (n.stage LIKE 'verification_failed:'||d.candidate_id||':%' OR n.stage LIKE 'verification_update_failed:%') AND (n.comment_id=q.trigger_comment_id OR n.comment_id=ANY(q.coalesced_comment_ids)))) WHERE d.workspace_id=$1 AND d.issue_id=$2 AND d.id=$3 ORDER BY q.created_at DESC LIMIT 1),false)`, ws, issue, delivery, run).Scan(&ended)
	return ended, err
}
