-- name: CreateKnowledgeImport :one
INSERT INTO knowledge_import (id, workspace_id, created_by, source_url, resource_uri)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetKnowledgeImport :one
SELECT * FROM knowledge_import WHERE id = $1 AND workspace_id = $2;

-- name: ListKnowledgeImports :many
SELECT * FROM knowledge_import WHERE workspace_id = $1
ORDER BY created_at DESC, id DESC LIMIT 20;

-- name: UpdateKnowledgeImport :one
UPDATE knowledge_import SET status = $3, upstream_task_id = $4, error = $5, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status IN ('fetching', 'indexing')
RETURNING *;

-- name: ExpireInterruptedKnowledgeImports :exec
UPDATE knowledge_import SET status = 'failed', error = '导入被中断，请先检查资源列表再重试', updated_at = now()
WHERE workspace_id = $1 AND status = 'fetching' AND created_at < now() - interval '2 minutes';
