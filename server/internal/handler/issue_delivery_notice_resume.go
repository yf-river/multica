package handler

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// 发布恢复时绑定当时的交付候选，避免事后用任务状态猜测它处理了哪一次发布。
func bindPublishedResume(ctx context.Context, tx pgx.Tx, window, source, next string) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET context=COALESCE(context,'{}'::jsonb)-'published_closeout' WHERE id=$1`, next); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE agent_task_queue q SET context=COALESCE(q.context,'{}'::jsonb)||jsonb_build_object('published_closeout',jsonb_build_object('delivery_id',d.id,'candidate_id',d.candidate_id,'window_id',w.id,'release_id',w.release_id)) FROM release_window w,agent_task_queue old,issue_delivery d,issue i WHERE q.id=$3 AND old.id=$2 AND w.id=$1 AND w.environment_id='prod' AND d.workspace_id=i.workspace_id AND d.issue_id=old.issue_id AND i.id=d.issue_id AND i.assignee_type='agent' AND i.status<>'done' AND i.assignee_id=old.agent_id AND q.agent_id=old.agent_id AND d.phase IN('publishing','published') AND d.candidate_id<>'' AND d.plan_snapshot->>'candidate_commit' ~ '^[0-9a-f]{40}$' AND w.release_id ~ '^[0-9a-f]{40}-[0-9]+$' AND split_part(w.release_id,'-',1)=d.plan_snapshot->>'candidate_commit' AND split_part(d.candidate_id,'-',1)=d.plan_snapshot->>'candidate_commit' AND (old.trigger_comment_id=d.release_comment_id OR EXISTS(SELECT 1 FROM delivery_notice n WHERE n.delivery_id=d.id AND n.stage='batch_ready' AND (n.comment_id=old.trigger_comment_id OR n.comment_id=ANY(old.coalesced_comment_ids))))`, window, source, next)
	return err
}

// 只等待同一生产窗口真实恢复的负责人；没有承接、执行失败或未完成任务时，正常结果通知负责兜底。
func (h *Handler) resumedPublishedCloseout(ctx context.Context, ws, issue, delivery, candidate string) (pending, completed bool, err error) {
	err = h.DB.QueryRow(ctx, `SELECT COALESCE(bool_or(q.status IN('queued','dispatched','running','waiting_local_directory','deferred')),false),COALESCE(bool_or(q.status='completed' AND i.status='done' AND i.updated_at>=q.created_at),false) FROM issue_delivery d JOIN issue i ON i.id=d.issue_id JOIN agent_task_queue q ON q.issue_id=i.id AND q.agent_id=i.assignee_id JOIN release_paused_task p ON p.resume_task_id=q.id AND p.task_id=q.rerun_of_task_id AND p.state='queued' JOIN release_window w ON w.id=p.window_id AND w.environment_id='prod' AND w.state='finished' WHERE d.id=$3 AND d.workspace_id=$1 AND d.issue_id=$2 AND d.candidate_id=$4 AND d.phase='published' AND i.assignee_type='agent' AND q.context->'published_closeout'->>'delivery_id'=d.id::text AND q.context->'published_closeout'->>'candidate_id'=d.candidate_id AND q.context->'published_closeout'->>'window_id'=w.id::text AND q.context->'published_closeout'->>'release_id'=w.release_id AND w.release_id ~ '^[0-9a-f]{40}-[0-9]+$' AND split_part(w.release_id,'-',1)=d.plan_snapshot->>'candidate_commit' AND split_part(d.candidate_id,'-',1)=d.plan_snapshot->>'candidate_commit'`, ws, issue, delivery, candidate).Scan(&pending, &completed)
	return
}
