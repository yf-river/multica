CREATE TABLE context_insights_chunk (
 task_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 chat_session_id uuid,
 issue_id uuid,
 object_id text NOT NULL,
 part integer NOT NULL,
 total integer NOT NULL,
 data bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
