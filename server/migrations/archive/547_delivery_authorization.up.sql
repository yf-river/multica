CREATE TABLE issue_delivery_authorization (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('execute','release','revoke')),
    comment_id uuid NOT NULL,
    comment_revision integer NOT NULL,
    comment_snapshot jsonb NOT NULL,
    scope text NOT NULL,
    registered_by_type text NOT NULL,
    registered_by uuid NOT NULL,
    revoked boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
