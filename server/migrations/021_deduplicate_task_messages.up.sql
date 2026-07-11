-- A daemon retry reuses the stable per-task sequence number. Keep the first
-- persisted frame as the execution record and enforce that idempotency key.
DELETE FROM task_message newer
USING task_message earlier
WHERE newer.task_id = earlier.task_id
  AND newer.seq = earlier.seq
  AND (newer.created_at, newer.id) > (earlier.created_at, earlier.id);

DROP INDEX IF EXISTS idx_task_message_task_id_seq;
CREATE UNIQUE INDEX idx_task_message_task_id_seq ON task_message(task_id, seq);
