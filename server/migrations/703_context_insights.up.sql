CREATE TABLE context_insights_config (
 singleton boolean NOT NULL DEFAULT true CHECK(singleton),
 enabled_since timestamptz NOT NULL DEFAULT now()
);
INSERT INTO context_insights_config(singleton) VALUES(true);
CREATE TABLE context_insights_session (
 id text NOT NULL,
 daemon_id text NOT NULL,
 session_id text NOT NULL,
 parent_session_id text NOT NULL DEFAULT '',
 workspace_id uuid NOT NULL,
 chat_session_id uuid,
 issue_id uuid,
 agent_id uuid NOT NULL,
 seq bigint NOT NULL DEFAULT -1,
 projections jsonb NOT NULL DEFAULT '{}',
 version integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE context_insights_run (
 session_key text NOT NULL,
 task_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE context_insights_event (
 session_key text NOT NULL,
 seq bigint NOT NULL,
 task_id uuid NOT NULL,
 occurred_at timestamptz NOT NULL,
 kind text NOT NULL,
 content jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE context_insights_preference (
 user_id uuid NOT NULL,
 preferences jsonb NOT NULL DEFAULT '{}',
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE context_insights_price (
 version text NOT NULL,
 catalog jsonb NOT NULL,
 fetched_at timestamptz NOT NULL DEFAULT now()
);
