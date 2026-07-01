DROP INDEX IF EXISTS agent_private_no_owner_name_active_unique;
DROP INDEX IF EXISTS agent_private_owner_name_active_unique;
DROP INDEX IF EXISTS agent_workspace_name_active_unique;

DELETE FROM agent a
USING (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY workspace_id, name ORDER BY updated_at DESC) AS rn
    FROM agent
) ranked
WHERE a.id = ranked.id AND ranked.rn > 1;

ALTER TABLE agent
    ADD CONSTRAINT agent_workspace_name_unique UNIQUE (workspace_id, name);
