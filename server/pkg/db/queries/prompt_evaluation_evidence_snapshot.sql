-- name: CreatePromptEvaluationEvidenceSnapshot :one
INSERT INTO prompt_evaluation_evidence_snapshot (
  workspace_id, run_id, snapshot_type, schema_version, summary, evidence, created_by
) VALUES (
  $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListPromptEvaluationEvidenceSnapshotsByRun :many
SELECT
  id,
  workspace_id,
  run_id,
  snapshot_type,
  schema_version,
  summary,
  created_by,
  created_at
FROM prompt_evaluation_evidence_snapshot
WHERE workspace_id = $1
  AND run_id = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: GetPromptEvaluationEvidenceSnapshotInWorkspace :one
SELECT * FROM prompt_evaluation_evidence_snapshot
WHERE id = $1
  AND workspace_id = $2;
