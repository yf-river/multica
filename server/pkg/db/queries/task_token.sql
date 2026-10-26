-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, id)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()))
RETURNING *;

-- name: GetTaskTokenByHash :one
SELECT task_token.* FROM task_token
WHERE token_hash = $1 AND expires_at > now()
  -- 旧凭据不能越过任务终态或重新派发的轮次边界，运行时配置变化也不能恢复权限。
  AND EXISTS (
    SELECT 1 FROM agent_task_queue task
    WHERE task.id = task_token.task_id
      AND task.status IN ('dispatched', 'running')
      AND task.dispatched_at IS NOT NULL
      AND task_token.created_at >= task.dispatched_at
  );

-- name: DeleteTaskTokensByTask :exec
DELETE FROM task_token WHERE task_id = $1;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token WHERE expires_at <= now();
