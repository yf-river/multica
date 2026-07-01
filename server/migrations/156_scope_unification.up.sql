-- Canonical resource scope. Current dev/int environments intentionally drop
-- Agent/Squad state separately before this migration is relied on; this file
-- only normalizes schema and preserved runtime/project/issue rows.

ALTER TABLE agent_runtime
    DROP CONSTRAINT IF EXISTS agent_runtime_visibility_check;

UPDATE agent_runtime
SET visibility = CASE visibility
    WHEN 'public' THEN 'workspace'
    ELSE 'personal'
END;

ALTER TABLE agent_runtime
    RENAME COLUMN visibility TO scope;

ALTER TABLE agent_runtime
    ALTER COLUMN scope SET DEFAULT 'personal',
    ADD CONSTRAINT agent_runtime_scope_check CHECK (scope IN ('personal', 'workspace'));

CREATE INDEX IF NOT EXISTS idx_agent_runtime_workspace_scope
    ON agent_runtime(workspace_id, scope);

DROP INDEX IF EXISTS agent_workspace_name_active_unique;
DROP INDEX IF EXISTS agent_private_owner_name_active_unique;
DROP INDEX IF EXISTS agent_private_no_owner_name_active_unique;

ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_visibility_check;

ALTER TABLE agent
    RENAME COLUMN visibility TO scope;

ALTER TABLE agent
    ALTER COLUMN scope SET DEFAULT 'personal',
    ADD CONSTRAINT agent_scope_check CHECK (scope IN ('personal', 'workspace'));

CREATE UNIQUE INDEX agent_workspace_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND scope = 'workspace';

CREATE UNIQUE INDEX agent_personal_owner_name_active_unique
    ON agent(workspace_id, owner_id, name)
    WHERE archived_at IS NULL AND scope = 'personal' AND owner_id IS NOT NULL;

CREATE UNIQUE INDEX agent_personal_no_owner_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND scope = 'personal' AND owner_id IS NULL;

DROP INDEX IF EXISTS idx_squad_workspace_visibility;

ALTER TABLE squad
    DROP CONSTRAINT IF EXISTS squad_visibility_check;

ALTER TABLE squad
    RENAME COLUMN visibility TO scope;

ALTER TABLE squad
    ALTER COLUMN scope SET DEFAULT 'workspace',
    ADD CONSTRAINT squad_scope_check CHECK (scope IN ('personal', 'workspace'));

CREATE INDEX idx_squad_workspace_scope
    ON squad(workspace_id, scope)
    WHERE archived_at IS NULL;

ALTER TABLE issue
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'workspace'
        CHECK (scope IN ('personal', 'workspace')),
    ADD COLUMN owner_id UUID REFERENCES "user"(id);

CREATE INDEX idx_issue_workspace_scope
    ON issue(workspace_id, scope);

CREATE INDEX idx_issue_workspace_owner_scope
    ON issue(workspace_id, owner_id, scope);

ALTER TABLE project
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'workspace'
        CHECK (scope IN ('personal', 'workspace')),
    ADD COLUMN owner_id UUID REFERENCES "user"(id);

CREATE INDEX idx_project_workspace_scope
    ON project(workspace_id, scope);

CREATE INDEX idx_project_workspace_owner_scope
    ON project(workspace_id, owner_id, scope);
