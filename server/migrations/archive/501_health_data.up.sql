CREATE TABLE health_state (
  workspace_id uuid NOT NULL,
  user_id uuid NOT NULL,
  revision bigint NOT NULL DEFAULT 0,
  epoch bigint NOT NULL DEFAULT 0,
  data jsonb NOT NULL DEFAULT '{"profile":{},"observation_ids":[]}',
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE health_observation (
  id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  user_id uuid NOT NULL,
  data jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE health_revision (
  workspace_id uuid NOT NULL,
  user_id uuid NOT NULL,
  revision bigint NOT NULL,
  data jsonb NOT NULL,
  reason text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE health_report (
  id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  user_id uuid NOT NULL,
  filename text NOT NULL,
  sha256 text NOT NULL,
  size_bytes bigint NOT NULL,
  page_count integer NOT NULL,
  issue_id uuid,
  agent_id uuid,
  attempt integer NOT NULL DEFAULT 0,
  epoch bigint NOT NULL DEFAULT 0,
  attempt_started_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE health_report_result (
  id uuid NOT NULL,
  report_id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  user_id uuid NOT NULL,
  attempt integer NOT NULL,
  revision bigint NOT NULL,
  data jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
