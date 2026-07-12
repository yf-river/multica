-- The current daemon persists `api_invalid_request` at write time. Normalize
-- rows created before that classifier existed so resume queries can rely on
-- failure_reason instead of reparsing provider error text forever.
UPDATE agent_task_queue
SET failure_reason = 'api_invalid_request'
WHERE status = 'failed'
  AND failure_reason = 'agent_error'
  AND COALESCE(error, '') ILIKE '%400%'
  AND COALESCE(error, '') ILIKE '%invalid_request_error%';
