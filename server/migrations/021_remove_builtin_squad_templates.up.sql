-- Built-in PM and Multica coding squads are retired. This migration is
-- intentionally irreversible and removes their owned execution history.
BEGIN;

CREATE TEMP TABLE target_builtin_squads ON COMMIT DROP AS
SELECT id
FROM public.squad
WHERE lower(btrim(name)) IN ('pm', 'multica 编码小队')
   OR lower(coalesce(sop_profile->>'profile_key', '')) IN (
       'generic-project-sop-flow-v2',
       'multica-coding',
       'user-center-sop-flow',
       'user-center-sop-flow-v2'
   );
ALTER TABLE target_builtin_squads ADD PRIMARY KEY (id);

CREATE TEMP TABLE target_builtin_agents ON COMMIT DROP AS
SELECT DISTINCT member_id AS id
FROM public.squad_member
WHERE member_type = 'agent'
  AND squad_id IN (SELECT id FROM target_builtin_squads)
UNION
SELECT leader_id
FROM public.squad
WHERE id IN (SELECT id FROM target_builtin_squads)
UNION
SELECT id
FROM public.agent
WHERE lower(coalesce(runtime_config #>> '{internal_squad,template_key}', '')) IN (
          'user-center-sop-flow',
          'user-center-sop-flow-v2',
          'generic-project-sop-flow-v2',
          'multica-coding'
      )
   OR lower(name) LIKE 'pm-v2%'
   OR lower(name) LIKE 'multica 编码小队 ·%';
ALTER TABLE target_builtin_agents ADD PRIMARY KEY (id);

CREATE TEMP TABLE target_builtin_runtimes ON COMMIT DROP AS
SELECT DISTINCT runtime_id AS id
FROM public.agent
WHERE id IN (SELECT id FROM target_builtin_agents);
ALTER TABLE target_builtin_runtimes ADD PRIMARY KEY (id);

CREATE TEMP TABLE target_builtin_autopilots ON COMMIT DROP AS
SELECT id
FROM public.autopilot
WHERE (assignee_type = 'squad' AND assignee_id IN (SELECT id FROM target_builtin_squads))
   OR (assignee_type = 'agent' AND assignee_id IN (SELECT id FROM target_builtin_agents))
   OR (created_by_type = 'agent' AND created_by_id IN (SELECT id FROM target_builtin_agents));
ALTER TABLE target_builtin_autopilots ADD PRIMARY KEY (id);

-- Issues created or assigned by the retired squads/agents, including all
-- descendants created as part of their execution chain.
CREATE TEMP TABLE target_builtin_issues (id uuid PRIMARY KEY) ON COMMIT DROP;
WITH RECURSIVE roots AS (
    SELECT i.id
    FROM public.issue i
    WHERE (i.assignee_type = 'squad' AND i.assignee_id IN (SELECT id FROM target_builtin_squads))
       OR (i.assignee_type = 'agent' AND i.assignee_id IN (SELECT id FROM target_builtin_agents))
       OR (i.creator_type = 'agent' AND i.creator_id IN (SELECT id FROM target_builtin_agents))
       OR (i.origin_type = 'autopilot' AND i.origin_id IN (SELECT id FROM target_builtin_autopilots))
), descendants AS (
    SELECT id FROM roots
    UNION
    SELECT child.id
    FROM public.issue child
    JOIN descendants parent ON parent.id = child.parent_issue_id
)
INSERT INTO target_builtin_issues (id)
SELECT id FROM descendants;

-- A life companion/observer must never be removed accidentally. Internal
-- templates are expected to be ordinary work agents; fail atomically if a
-- deployment has repurposed one for the life system.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.companion_profile
        WHERE agent_id IN (SELECT id FROM target_builtin_agents)
    ) OR EXISTS (
        SELECT 1 FROM public.life_observer
        WHERE agent_id IN (SELECT id FROM target_builtin_agents)
    ) THEN
        RAISE EXCEPTION 'retired squad agent is referenced by life data; aborting destructive migration';
    END IF;
END $$;

DO $$
DECLARE n bigint;
BEGIN
    DELETE FROM public.inbox_item
     WHERE issue_id IN (SELECT id FROM target_builtin_issues)
        OR (recipient_type = 'agent' AND recipient_id IN (SELECT id FROM target_builtin_agents))
        OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM target_builtin_agents));
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed inbox items: %', n;

    DELETE FROM public.activity_log
     WHERE issue_id IN (SELECT id FROM target_builtin_issues)
        OR (actor_type = 'agent' AND actor_id IN (SELECT id FROM target_builtin_agents));
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed activity logs: %', n;

    DELETE FROM public.issue_subscriber
     WHERE issue_id IN (SELECT id FROM target_builtin_issues)
        OR (user_type = 'agent' AND user_id IN (SELECT id FROM target_builtin_agents));
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed issue subscribers: %', n;

    DELETE FROM public.comment
     WHERE author_type = 'agent' AND author_id IN (SELECT id FROM target_builtin_agents);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed agent comments: %', n;

    DELETE FROM public.issue_reaction
     WHERE actor_type = 'agent' AND actor_id IN (SELECT id FROM target_builtin_agents);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed issue reactions: %', n;

    DELETE FROM public.pinned_item
     WHERE item_type = 'issue' AND item_id IN (SELECT id FROM target_builtin_issues);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed pinned issues: %', n;

    DELETE FROM public.attachment
     WHERE issue_id IN (SELECT id FROM target_builtin_issues)
        OR (uploader_type = 'agent' AND uploader_id IN (SELECT id FROM target_builtin_agents));
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed attachments: %', n;

    DELETE FROM public.autopilot
     WHERE id IN (SELECT id FROM target_builtin_autopilots);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed autopilots: %', n;

    DELETE FROM public.task_usage_hourly
     WHERE agent_id IN (SELECT id FROM target_builtin_agents);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed hourly usage: %', n;
    DELETE FROM public.task_usage_hourly_dirty
     WHERE agent_id IN (SELECT id FROM target_builtin_agents);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed dirty usage: %', n;

    -- issue children are explicitly included because parent_issue_id uses
    -- SET NULL; all normal issue-owned rows then cascade from this delete.
    DELETE FROM public.issue
     WHERE id IN (SELECT id FROM target_builtin_issues);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed issues: %', n;

    DELETE FROM public.squad
     WHERE id IN (SELECT id FROM target_builtin_squads);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed squads: %', n;

    DELETE FROM public.agent
     WHERE id IN (SELECT id FROM target_builtin_agents);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed agents: %', n;

    DELETE FROM public.agent_runtime r
     WHERE (r.owner_id IN (SELECT id FROM target_builtin_agents)
         OR r.id IN (SELECT id FROM target_builtin_runtimes))
       AND NOT EXISTS (SELECT 1 FROM public.agent a WHERE a.runtime_id = r.id);
    GET DIAGNOSTICS n = ROW_COUNT; RAISE NOTICE 'removed orphan runtimes: %', n;
END $$;

COMMIT;
