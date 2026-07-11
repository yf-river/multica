-- Prompt evaluation used to discover its internal agent by either of two
-- display names. Resolve the persisted legacy name once so runtime code has a
-- single current identity. If a current active agent already occupies the
-- unique name in the same scope, keep that agent active and archive the legacy
-- duplicate; historical foreign-key references still point to the same row.
UPDATE agent AS legacy
SET archived_at = now(),
    updated_at = now()
WHERE legacy.name = 'Multica 训练评估 Agent'
  AND legacy.archived_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM agent AS current
      WHERE current.workspace_id = legacy.workspace_id
        AND current.name = 'Multica 训练评估智能体'
        AND current.archived_at IS NULL
        AND current.scope = legacy.scope
        AND (
            legacy.scope = 'workspace'
            OR current.owner_id IS NOT DISTINCT FROM legacy.owner_id
        )
  );

UPDATE agent
SET name = 'Multica 训练评估智能体',
    updated_at = now()
WHERE name = 'Multica 训练评估 Agent';
