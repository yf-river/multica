CREATE TABLE IF NOT EXISTS daemon_token (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    token_hash text NOT NULL,
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    daemon_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_daemon_token_hash ON daemon_token(token_hash);
CREATE INDEX IF NOT EXISTS idx_daemon_token_workspace_daemon ON daemon_token(workspace_id, daemon_id);
