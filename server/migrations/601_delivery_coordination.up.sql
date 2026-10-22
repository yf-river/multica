CREATE TABLE IF NOT EXISTS delivery_owner (
 workspace_id uuid NOT NULL, issue_id uuid NOT NULL,
 execution_id text NOT NULL, token uuid NOT NULL,
 generation bigint NOT NULL DEFAULT 1,
 expires_at timestamptz NOT NULL,
 recovery_evidence text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS delivery_publication (
 resource text NOT NULL, workspace_id uuid, delivery_id uuid,
 worker_token uuid, generation bigint NOT NULL DEFAULT 0,
 expires_at timestamptz, snapshot jsonb NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'idle', updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS delivery_evidence (
 id uuid NOT NULL, workspace_id uuid NOT NULL, issue_id uuid NOT NULL,
 delivery_id uuid NOT NULL, candidate_id text NOT NULL,
 source_commit text NOT NULL, identity jsonb NOT NULL,
 evidence jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
