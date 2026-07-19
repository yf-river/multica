ALTER TABLE chat_idempotency_record DROP COLUMN IF EXISTS created_at;
ALTER TABLE github_pending_installation
    DROP COLUMN IF EXISTS received_at,
    DROP COLUMN IF EXISTS updated_at;
ALTER TABLE github_pull_request
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS updated_at;
ALTER TABLE lark_chat_session_binding DROP COLUMN IF EXISTS created_at;
ALTER TABLE task_token DROP COLUMN IF EXISTS created_at;
