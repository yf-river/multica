-- The schema rollback removes the uniqueness gate. Rows deduplicated by the up
-- migration are intentionally not reconstructed because their payloads were
-- duplicate automatic facts, not distinct business history.
BEGIN;

DROP INDEX IF EXISTS idx_squad_sop_terminal_event_task_unique;

COMMIT;
