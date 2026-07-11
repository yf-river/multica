-- Chat session deletion is now a transactional hard delete. The retired soft
-- archive state only hid retained conversations and forced every list/send
-- path to carry a second, unreachable lifecycle. Preserve those conversations
-- by making them current before removing the state column.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'chat_session'
          AND column_name = 'status'
    ) THEN
        UPDATE chat_session SET status = 'active' WHERE status <> 'active';
        ALTER TABLE chat_session DROP CONSTRAINT IF EXISTS chat_session_status_check;
        ALTER TABLE chat_session DROP COLUMN status;
    END IF;
END $$;
