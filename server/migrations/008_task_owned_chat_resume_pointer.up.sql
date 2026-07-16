ALTER TABLE chat_session
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS work_dir,
    DROP COLUMN IF EXISTS runtime_id;
