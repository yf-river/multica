CREATE TABLE upstream_deployment (
 id uuid NOT NULL,
 project_id text NOT NULL,
 action text NOT NULL CHECK(action IN ('prepare','deploy')),
 version text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'pending',
 result jsonb NOT NULL DEFAULT '{}',
 error text NOT NULL DEFAULT '',
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
