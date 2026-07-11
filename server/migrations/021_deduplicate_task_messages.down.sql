DROP INDEX IF EXISTS idx_task_message_task_id_seq;
CREATE INDEX idx_task_message_task_id_seq ON task_message(task_id, seq);
