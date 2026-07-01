ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_workspace_name_unique;

CREATE UNIQUE INDEX agent_workspace_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND visibility = 'workspace';

CREATE UNIQUE INDEX agent_private_owner_name_active_unique
    ON agent(workspace_id, owner_id, name)
    WHERE archived_at IS NULL AND visibility = 'private' AND owner_id IS NOT NULL;

CREATE UNIQUE INDEX agent_private_no_owner_name_active_unique
    ON agent(workspace_id, name)
    WHERE archived_at IS NULL AND visibility = 'private' AND owner_id IS NULL;
