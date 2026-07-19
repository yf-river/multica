ALTER TABLE chat_idempotency_record ADD COLUMN created_at timestamp with time zone DEFAULT now() NOT NULL;
ALTER TABLE github_pending_installation
    ADD COLUMN received_at timestamp with time zone DEFAULT now() NOT NULL,
    ADD COLUMN updated_at timestamp with time zone DEFAULT now() NOT NULL;
ALTER TABLE github_pull_request
    ADD COLUMN created_at timestamp with time zone DEFAULT now() NOT NULL,
    ADD COLUMN updated_at timestamp with time zone DEFAULT now() NOT NULL;
ALTER TABLE lark_chat_session_binding ADD COLUMN created_at timestamp with time zone DEFAULT now() NOT NULL;
ALTER TABLE task_token ADD COLUMN created_at timestamp with time zone DEFAULT now() NOT NULL;
