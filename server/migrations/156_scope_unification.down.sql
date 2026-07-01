DROP INDEX IF EXISTS idx_project_workspace_owner_scope;
DROP INDEX IF EXISTS idx_project_workspace_scope;

ALTER TABLE project
    DROP COLUMN IF EXISTS owner_id,
    DROP COLUMN IF EXISTS scope;

DROP INDEX IF EXISTS idx_issue_workspace_owner_scope;
DROP INDEX IF EXISTS idx_issue_workspace_scope;

ALTER TABLE issue
    DROP COLUMN IF EXISTS owner_id,
    DROP COLUMN IF EXISTS scope;

DROP INDEX IF EXISTS idx_squad_workspace_scope;

ALTER TABLE squad
    DROP CONSTRAINT IF EXISTS squad_scope_check;

ALTER TABLE squad
    RENAME COLUMN scope TO visibility;

ALTER TABLE squad
    ALTER COLUMN visibility SET DEFAULT 'workspace',
    ADD CONSTRAINT squad_visibility_check CHECK (visibility IN ('personal', 'workspace'));

CREATE INDEX idx_squad_workspace_visibility
    ON squad(workspace_id, visibility)
    WHERE archived_at IS NULL;

DROP INDEX IF EXISTS agent_workspace_name_active_unique;
DROP INDEX IF EXISTS agent_personal_owner_name_active_unique;
DROP INDEX IF EXISTS agent_personal_no_owner_name_active_unique;

ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_scope_check;

UPDATE agent
SET scope = CASE scope
    WHEN 'workspace' THEN 'workspace'
    ELSE 'private'
END;

ALTER TABLE agent
    RENAME COLUMN scope TO visibility;

ALTER TABLE agent
    ALTER COLUMN visibility SET DEFAULT 'private',
    ADD CONSTRAINT agent_visibility_check CHECK (visibility IN ('workspace', 'private'));

CREATE UNIQUE INDEX agent_workspace_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND visibility = 'workspace';

CREATE UNIQUE INDEX agent_private_owner_name_active_unique
    ON agent(workspace_id, owner_id, name)
    WHERE archived_at IS NULL AND visibility = 'private' AND owner_id IS NOT NULL;

CREATE UNIQUE INDEX agent_private_no_owner_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND visibility = 'private' AND owner_id IS NULL;

DROP INDEX IF EXISTS idx_agent_runtime_workspace_scope;

ALTER TABLE agent_runtime
    DROP CONSTRAINT IF EXISTS agent_runtime_scope_check;

UPDATE agent_runtime
SET scope = CASE scope
    WHEN 'workspace' THEN 'public'
    ELSE 'private'
END;

ALTER TABLE agent_runtime
    RENAME COLUMN scope TO visibility;

ALTER TABLE agent_runtime
    ALTER COLUMN visibility SET DEFAULT 'private',
    ADD CONSTRAINT agent_runtime_visibility_check CHECK (visibility IN ('private', 'public'));
