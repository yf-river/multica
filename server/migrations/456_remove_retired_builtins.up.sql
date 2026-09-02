-- Remove retired built-in squads and the runtime profile that no longer has a
-- corresponding backend. The target sets are materialized before any delete
-- so cleanup is scoped and repeatable.
BEGIN;

CREATE TEMP TABLE retired_squads ON COMMIT DROP AS
SELECT id
FROM squad
WHERE lower(trim(name)) IN ('pm', 'multica 编码小队')
   OR instructions ILIKE ANY (ARRAY[
       '%generic-project-sop-flow-v2%',
       '%multica-coding%',
       '%user-center-sop-flow%'
   ]);

CREATE TEMP TABLE retired_agents ON COMMIT DROP AS
SELECT DISTINCT agent_id AS id
FROM (
    SELECT leader_id AS agent_id FROM squad WHERE id IN (SELECT id FROM retired_squads)
    UNION ALL
    SELECT member_id AS agent_id
    FROM squad_member
    WHERE squad_id IN (SELECT id FROM retired_squads) AND member_type = 'agent'
) candidates
WHERE agent_id IS NOT NULL;

-- These UUID links intentionally have no foreign key in the hot queue and
-- issue tables. Clear them before removing the owning rows.
UPDATE issue
SET assignee_type = NULL, assignee_id = NULL
WHERE (assignee_type = 'squad' AND assignee_id IN (SELECT id FROM retired_squads))
   OR (assignee_type = 'agent' AND assignee_id IN (SELECT id FROM retired_agents));

DELETE FROM issue
WHERE (creator_type = 'agent' AND creator_id IN (SELECT id FROM retired_agents));

DELETE FROM squad WHERE id IN (SELECT id FROM retired_squads);
DELETE FROM agent WHERE id IN (SELECT id FROM retired_agents);

DELETE FROM runtime_profile WHERE lower(protocol_family) = 'openclaw';

ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_protocol_family_check;
ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_protocol_family_check
CHECK (protocol_family IN (
    'claude', 'codebuddy', 'codex', 'copilot', 'opencode', 'hermes',
    'gemini', 'pi', 'cursor', 'kimi', 'kiro', 'antigravity', 'codearts',
    'deveco', 'reasonix', 'dsh', 'traecli', 'grok', 'qwen', 'qwenpaw',
    'mcode', 'dim', 'zeroclaw', 'qoder', 'qoderclicn'
));

COMMIT;
