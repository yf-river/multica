-- Automatic terminal events are idempotency records for the task -> SOP run
-- projection. Historical races could insert duplicates because application
-- code used SELECT-then-INSERT. Keep the earliest durable event and enforce
-- the current single-path invariant in the database.
BEGIN;

-- Keep older application processes from inserting a new duplicate between the
-- cleanup and index build. The lock is held only for this small data migration
-- and is released with the transaction.
LOCK TABLE squad_sop_step_event IN SHARE ROW EXCLUSIVE MODE;

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY run_id, task_id, event_type
               ORDER BY created_at ASC, id ASC
           ) AS duplicate_number
    FROM squad_sop_step_event
    WHERE task_id IS NOT NULL
      AND created_by_type = 'system'
      AND event_type IN ('步骤完成', '步骤失败')
)
DELETE FROM squad_sop_step_event e
USING ranked r
WHERE e.id = r.id
  AND r.duplicate_number > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_squad_sop_terminal_event_task_unique
    ON squad_sop_step_event (run_id, task_id, event_type)
    WHERE task_id IS NOT NULL
      AND created_by_type = 'system'
      AND event_type IN ('步骤完成', '步骤失败');

COMMIT;
