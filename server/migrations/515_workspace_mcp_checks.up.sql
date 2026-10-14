ALTER TABLE workspace_mcp_server ADD COLUMN description text NOT NULL DEFAULT '', ADD COLUMN config_version bigint NOT NULL DEFAULT 1;
CREATE TABLE workspace_mcp_check (
 id uuid NOT NULL DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, server_id uuid NOT NULL,
 runtime_id uuid NOT NULL, config_version bigint NOT NULL, status text NOT NULL DEFAULT 'pending',
 tool_count integer NOT NULL DEFAULT 0, error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, completed_at timestamptz
);
