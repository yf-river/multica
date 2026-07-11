-- Intentional one-way normalization: the retired archived state cannot be
-- reconstructed. A rollback restores only the current active representation.
ALTER TABLE chat_session
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chat_session_status_check'
          AND conrelid = 'chat_session'::regclass
    ) THEN
        ALTER TABLE chat_session
            ADD CONSTRAINT chat_session_status_check CHECK (status = 'active');
    END IF;
END $$;
