CREATE TABLE IF NOT EXISTS release_window (
 id uuid NOT NULL, environment_id text NOT NULL, daemon_id uuid NOT NULL,
 release_id text NOT NULL, state text NOT NULL DEFAULT 'draining',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS release_paused_task (
 window_id uuid NOT NULL, task_id uuid NOT NULL, resume_task_id uuid,
 state text NOT NULL DEFAULT 'requested', session_id text NOT NULL DEFAULT '',
 work_dir text NOT NULL DEFAULT '', root_dir text NOT NULL DEFAULT '', error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
