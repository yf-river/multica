-- Move pre-outbox background case operations onto the durable current path.
-- A persisted 运行中 row belonged to an in-process goroutine; after restart
-- there is no worker to finish it, so it must become claimable again.
UPDATE prompt_evaluation_case_operation
SET status = '已入队',
    error_message = '',
    started_at = NULL,
    completed_at = NULL,
    updated_at = now()
WHERE status = '运行中';

INSERT INTO domain_event_outbox (
    event_type,
    stream_key,
    workspace_id,
    actor_type,
    actor_id,
    payload
)
SELECT
    'prompt_evaluation_case_operation:requested',
    'prompt_evaluation_case_operation:' || operation.id::text,
    operation.workspace_id,
    CASE WHEN operation.created_by IS NULL THEN 'system' ELSE 'member' END,
    operation.created_by::text,
    jsonb_build_object('operation_id', operation.id::text)
FROM prompt_evaluation_case_operation AS operation
WHERE operation.status = '已入队'
  AND NOT EXISTS (
      SELECT 1
      FROM domain_event_outbox AS event
      WHERE event.event_type = 'prompt_evaluation_case_operation:requested'
        AND event.payload->>'operation_id' = operation.id::text
  );
