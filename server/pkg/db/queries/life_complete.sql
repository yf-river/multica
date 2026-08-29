-- name: GetActiveLifeIdentity :one
SELECT * FROM life_identity_version
WHERE workspace_id = $1 AND user_id = $2 AND status = 'active'
LIMIT 1;

-- name: ListLifeIdentityVersions :many
SELECT * FROM life_identity_version
WHERE workspace_id = $1 AND user_id = $2
ORDER BY version DESC;

-- name: GetNextLifeIdentityVersion :one
SELECT COALESCE(MAX(version), 0)::int + 1 AS version
FROM life_identity_version
WHERE workspace_id = $1 AND user_id = $2;

-- name: CreateLifeIdentityVersion :one
INSERT INTO life_identity_version (
    workspace_id, user_id, version, status, stable_core, relationship_contract,
    growth_profile, expression_profile, interests, change_reason,
    confirmed_at, confirmed_by_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    sqlc.narg(confirmed_at), sqlc.narg(confirmed_by_id)
)
RETURNING *;

-- name: SupersedeActiveLifeIdentity :exec
UPDATE life_identity_version
SET status = 'superseded'
WHERE workspace_id = $1 AND user_id = $2 AND status = 'active';

-- name: SetCompanionCurrentIdentity :exec
UPDATE companion_profile
SET current_identity_version_id = $3, updated_at = now()
WHERE workspace_id = $1 AND user_id = $2;

-- name: GetLifeIdentityVersionForUser :one
SELECT * FROM life_identity_version
WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: ActivateExistingLifeIdentityVersion :one
UPDATE life_identity_version
SET status = 'active', confirmed_at = COALESCE(confirmed_at, now()), confirmed_by_id = $4
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status IN ('draft', 'superseded')
RETURNING *;

-- name: ListLifeRelationshipEvents :many
SELECT * FROM life_relationship_event
WHERE workspace_id = $1 AND user_id = $2
ORDER BY created_at DESC;

-- name: CreateLifeRelationshipEvent :one
INSERT INTO life_relationship_event (
    workspace_id, user_id, event_type, status, user_position,
    companion_position, context, revisit_after
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg(revisit_after))
RETURNING *;

-- name: ResolveLifeRelationshipEvent :one
UPDATE life_relationship_event
SET status = $4, resolution = $5, resolved_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: UpsertLifeMaterial :one
INSERT INTO life_material (
    workspace_id, user_id, source_type, source_key, source_revision,
    content, metadata, occurred_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (workspace_id, user_id, source_type, source_key, source_revision)
DO UPDATE SET content = EXCLUDED.content, metadata = EXCLUDED.metadata, occurred_at = EXCLUDED.occurred_at
RETURNING *;

-- name: ListLifeMaterials :many
SELECT * FROM life_material
WHERE workspace_id = $1 AND user_id = $2
  AND (sqlc.narg(source_type)::text IS NULL OR source_type = sqlc.narg(source_type))
  AND (sqlc.narg(before_time)::timestamptz IS NULL OR occurred_at < sqlc.narg(before_time))
ORDER BY occurred_at DESC
LIMIT $3;

-- name: ListLifeMaterialsByEvidenceSources :many
SELECT m.*
FROM life_material m
WHERE m.workspace_id = $1 AND m.user_id = $2
  AND EXISTS (
      SELECT 1 FROM life_memory_evidence e
      WHERE e.memory_id = ANY($3::uuid[])
        AND e.source_type = m.source_type
        AND (e.source_id = m.id OR e.source_id::text = m.source_key)
  );

-- name: GetLatestLifeMaterialBySource :one
SELECT * FROM life_material
WHERE workspace_id = $1 AND user_id = $2 AND source_type = $3 AND source_key = $4
ORDER BY occurred_at DESC LIMIT 1;

-- name: GetLifeMaterialForUser :one
SELECT * FROM life_material WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: ListLifeMaterialsByIDs :many
SELECT * FROM life_material
WHERE workspace_id = $1 AND user_id = $2 AND id = ANY(sqlc.arg(material_ids)::uuid[])
ORDER BY occurred_at, id;

-- name: GetLifeMaterialBySourceRevision :one
SELECT * FROM life_material
WHERE workspace_id = $1 AND user_id = $2
  AND source_type = $3 AND source_key = $4 AND source_revision = $5;

-- name: ListLifeChatTurnMaterials :many
WITH target AS (
    SELECT material.occurred_at
    FROM life_material material
    WHERE material.workspace_id = $1 AND material.user_id = $2
      AND material.source_type = 'chat_message' AND material.source_key = sqlc.arg(through_message_id)::text
), previous_reply AS (
    SELECT max(material.occurred_at) AS occurred_at
    FROM life_material material, target
    WHERE material.workspace_id = $1 AND material.user_id = $2
      AND material.source_type = 'chat_message'
      AND material.metadata->>'chat_session_id' = sqlc.arg(chat_session_id)::text
      AND material.metadata->>'role' = 'assistant'
      AND material.occurred_at < target.occurred_at
)
SELECT material.*
FROM life_material material, target, previous_reply
WHERE material.workspace_id = $1 AND material.user_id = $2
  AND material.source_type = 'chat_message'
  AND material.metadata->>'chat_session_id' = sqlc.arg(chat_session_id)::text
  AND material.occurred_at > COALESCE(previous_reply.occurred_at, '-infinity'::timestamptz)
  AND material.occurred_at <= target.occurred_at
ORDER BY material.occurred_at, material.id;

-- name: ListLifeMaterialsInPeriod :many
SELECT * FROM life_material
WHERE workspace_id = $1 AND user_id = $2
  AND occurred_at >= sqlc.arg(period_start)::timestamptz
  AND occurred_at < sqlc.arg(period_end)::timestamptz
ORDER BY occurred_at, id;

-- name: ListMissingLifeChroniclePeriods :many
WITH bounds AS (
    SELECT min(occurred_at) AS first_at, sqlc.arg(before_time)::timestamptz AS before_time
    FROM life_material
    WHERE workspace_id = $1 AND user_id = $2
), periods AS (
    SELECT 'day'::text AS period_kind, value AS period_start, value + interval '1 day' AS period_end
    FROM bounds, LATERAL generate_series(
        date_trunc('day', first_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        (date_trunc('day', before_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') - interval '1 day',
        interval '1 day'
    ) value
    WHERE first_at IS NOT NULL
    UNION ALL
    SELECT 'week', value, value + interval '1 week'
    FROM bounds, LATERAL generate_series(
        date_trunc('week', first_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        (date_trunc('week', before_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') - interval '1 week',
        interval '1 week'
    ) value
    WHERE first_at IS NOT NULL
    UNION ALL
    SELECT 'month', value, value + interval '1 month'
    FROM bounds, LATERAL generate_series(
        date_trunc('month', first_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        (date_trunc('month', before_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') - interval '1 month',
        interval '1 month'
    ) value
    WHERE first_at IS NOT NULL
    UNION ALL
    SELECT 'year', value, value + interval '1 year'
    FROM bounds, LATERAL generate_series(
        date_trunc('year', first_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
        (date_trunc('year', before_time AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') - interval '1 year',
        interval '1 year'
    ) value
    WHERE first_at IS NOT NULL
), with_material AS (
    SELECT period.*
    FROM periods period
    WHERE EXISTS (
        SELECT 1 FROM life_material material
        WHERE material.workspace_id = $1 AND material.user_id = $2
          AND material.occurred_at >= period.period_start
          AND material.occurred_at < period.period_end
    )
), ready AS (
    SELECT period.*
    FROM with_material period
    WHERE period.period_kind = 'day'
       OR (period.period_kind IN ('week', 'month') AND NOT EXISTS (
            SELECT 1
            FROM life_material material
            WHERE material.workspace_id = $1 AND material.user_id = $2
              AND material.occurred_at >= period.period_start
              AND material.occurred_at < period.period_end
              AND NOT EXISTS (
                  SELECT 1 FROM life_chronicle_entry child
                  WHERE child.workspace_id = $1 AND child.user_id = $2
                    AND child.status = 'published' AND child.period_kind = 'day'
                    AND material.occurred_at >= child.period_start
                    AND material.occurred_at < child.period_end
              )
       ))
       OR (period.period_kind = 'year' AND NOT EXISTS (
            SELECT 1
            FROM life_material material
            WHERE material.workspace_id = $1 AND material.user_id = $2
              AND material.occurred_at >= period.period_start
              AND material.occurred_at < period.period_end
              AND NOT EXISTS (
                  SELECT 1 FROM life_chronicle_entry child
                  WHERE child.workspace_id = $1 AND child.user_id = $2
                    AND child.status = 'published' AND child.period_kind = 'month'
                    AND material.occurred_at >= child.period_start
                    AND material.occurred_at < child.period_end
              )
       ))
)
SELECT period_kind, period_start::timestamptz AS period_start, period_end::timestamptz AS period_end
FROM ready period
WHERE NOT EXISTS (
        SELECT 1 FROM life_chronicle_entry entry
        WHERE entry.workspace_id = $1 AND entry.user_id = $2
          AND entry.status = 'published' AND entry.period_kind = period.period_kind
          AND entry.period_start = period.period_start AND entry.period_end = period.period_end
    )
  AND NOT EXISTS (
        SELECT 1 FROM life_cognition_job job
        WHERE job.workspace_id = $1 AND job.user_id = $2
          AND job.job_type = 'chronicle_generate'
          AND job.dedupe_key = period.period_kind || ':' || to_char(period.period_start AT TIME ZONE 'UTC', 'YYYY-MM-DD')
    )
ORDER BY period_end, CASE period_kind WHEN 'day' THEN 1 WHEN 'week' THEN 2 WHEN 'month' THEN 3 ELSE 4 END
LIMIT sqlc.arg(max_periods)::int;

-- name: DeleteLifeMaterialsByIDs :exec
DELETE FROM life_material WHERE id = ANY($1::uuid[]) AND workspace_id = $2 AND user_id = $3;

-- name: RecordLifeDerivation :exec
INSERT INTO life_derivation (
    workspace_id, user_id, source_type, source_id, target_type, target_id, job_id
) VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg(job_id))
ON CONFLICT DO NOTHING;

-- name: ListLifeDerivationsBySources :many
SELECT * FROM life_derivation
WHERE workspace_id = $1 AND user_id = $2
  AND (source_type || ':' || source_id) = ANY($3::text[]);

-- name: ScrubLifeCognitionTasksByMaterialIDs :exec
WITH queued_batches AS MATERIALIZED (
    SELECT job.id,
           (SELECT COALESCE(jsonb_agg(value ORDER BY value), '[]'::jsonb)
              FROM jsonb_array_elements_text(
                  COALESCE(job.input->'material_ids', jsonb_build_array(job.input->>'material_id'))
              ) material_id(value)
             WHERE value <> ALL($3::text[])) AS remaining_material_ids,
           (SELECT COALESCE(jsonb_agg(value ORDER BY value), '[]'::jsonb)
              FROM jsonb_array_elements_text(COALESCE(job.input->'processing_cursors', '[]'::jsonb)) cursor(value)
             WHERE replace(value, 'material:', '') <> ALL($3::text[])) AS remaining_cursors
      FROM life_cognition_job job
     WHERE job.workspace_id = $1 AND job.user_id = $2
       AND job.task_id IS NULL AND job.status IN ('queued', 'failed')
       AND (job.input->>'material_id' = ANY($3::text[]) OR EXISTS (
           SELECT 1
             FROM jsonb_array_elements_text(COALESCE(job.input->'material_ids', '[]'::jsonb)) material_id(value)
            WHERE value = ANY($3::text[])
       ))
), update_nonempty_batches AS (
    UPDATE life_cognition_job job
       SET input = jsonb_set(
               jsonb_set(job.input - 'material_id', '{material_ids}', batch.remaining_material_ids),
               '{processing_cursors}', batch.remaining_cursors
           ),
           updated_at = now()
      FROM queued_batches batch
     WHERE job.id = batch.id AND jsonb_array_length(batch.remaining_material_ids) > 0
), delete_empty_batches AS (
    DELETE FROM life_cognition_job job
     USING queued_batches batch
     WHERE job.id = batch.id AND jsonb_array_length(batch.remaining_material_ids) = 0
), affected_tasks AS MATERIALIZED (
    SELECT DISTINCT task.id
    FROM agent_task_queue task
    JOIN life_cognition_job job ON job.task_id = task.id
    WHERE job.workspace_id = $1 AND job.user_id = $2
      AND (EXISTS (
          SELECT 1
          FROM jsonb_array_elements(
              COALESCE(task.context->'input'->'new_materials', '[]'::jsonb)
              || COALESCE(task.context->'input'->'material_index', '[]'::jsonb)
          ) material
          WHERE material->>'id' = ANY($3::text[])
      ) OR EXISTS (
          SELECT 1
          FROM jsonb_array_elements(COALESCE(task.context->'input'->'period_chronicles', '[]'::jsonb)) chronicle,
               jsonb_array_elements(COALESCE(chronicle->'evidence', '[]'::jsonb)) evidence
          WHERE evidence->>'source_id' = ANY($3::text[])
      ))
), scrubbed_jobs AS (
    UPDATE life_cognition_job job
    SET input = '{}'::jsonb,
        output = NULL,
        status = CASE WHEN job.status IN ('queued', 'running', 'failed') THEN 'cancelled' ELSE job.status END,
        error = CASE WHEN job.status IN ('queued', 'running', 'failed') THEN 'source permanently forgotten' ELSE '' END,
        updated_at = now()
    WHERE job.task_id IN (SELECT id FROM affected_tasks)
    RETURNING job.task_id
)
UPDATE agent_task_queue task
SET context = jsonb_build_object('type', 'life_cognition', 'source_forgotten', true),
    result = NULL,
    status = CASE WHEN task.status IN ('queued', 'dispatched', 'running') THEN 'cancelled' ELSE task.status END,
    error = CASE WHEN task.status IN ('queued', 'dispatched', 'running') THEN 'source permanently forgotten' ELSE NULL END,
    completed_at = CASE WHEN task.status IN ('queued', 'dispatched', 'running') THEN now() ELSE task.completed_at END,
    session_id = NULL,
    work_dir = NULL,
    trigger_summary = '人生后台任务（来源已永久删除）'
WHERE task.id IN (SELECT id FROM affected_tasks);

-- name: DeleteLifeDerivedRecordsByTargets :exec
WITH targets AS MATERIALIZED (
    SELECT target_type, target_id
    FROM life_derivation
    WHERE workspace_id = $1 AND user_id = $2
      AND (source_type || ':' || source_id) = ANY($3::text[])
), delete_observation_topics AS (
    DELETE FROM life_observation_topic WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'observation_topic')
), delete_observer_judgements AS (
    DELETE FROM life_observer_judgement WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'observer_judgement')
), delete_experiment_observations AS (
    DELETE FROM life_experiment_observation WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'experiment_observation')
), clear_experiment_reviews AS (
    UPDATE life_experiment_round
    SET review_draft = NULL,
        review = NULL,
        reviewed_at = NULL,
        status = CASE WHEN status = 'reviewed' THEN 'awaiting_review' ELSE status END,
        updated_at = now()
    WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'experiment_round_review')
), delete_chronicles AS (
    DELETE FROM life_chronicle_entry WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'chronicle_entry')
), delete_proactive_inbox AS (
    DELETE FROM inbox_item
    WHERE details->>'life_proactive_check_id' IN (
        SELECT target_id::text FROM targets WHERE target_type = 'proactive_check'
    )
), delete_proactive_checks AS (
    DELETE FROM life_proactive_check WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'proactive_check')
), delete_relationship_events AS (
    DELETE FROM life_relationship_event WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'relationship_event')
), delete_action_proposals AS (
    DELETE FROM life_action_proposal WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'action_proposal')
), delete_thoughts AS (
    DELETE FROM life_internal_thought WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'internal_thought')
), delete_commitments AS (
    DELETE FROM life_commitment WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'commitment')
), delete_topics AS (
    DELETE FROM life_topic WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'topic')
), clear_upgrade_evaluations AS (
    UPDATE life_upgrade_evaluation
    SET result = NULL, status = 'unknown', rollback_recommended = false,
        completed_at = now()
    WHERE id IN (SELECT target_id FROM targets WHERE target_type = 'upgrade_evaluation')
)
DELETE FROM life_derivation derivation
WHERE derivation.workspace_id = $1 AND derivation.user_id = $2
  AND (
      (derivation.source_type || ':' || derivation.source_id) = ANY($3::text[])
      OR EXISTS (
          SELECT 1 FROM targets
          WHERE targets.target_type = derivation.target_type
            AND targets.target_id = derivation.target_id
      )
  );

-- name: DeleteLifeDerivationsByMemoryIDs :exec
DELETE FROM life_derivation
WHERE workspace_id = $1 AND user_id = $2
  AND target_type = 'memory' AND target_id = ANY($3::uuid[]);

-- name: DeleteLifeCommitmentsByMemoryIDs :exec
DELETE FROM life_commitment WHERE source_memory_id = ANY($1::uuid[]) AND workspace_id = $2 AND user_id = $3;

-- name: DeleteLifeObserverJudgementsBySources :exec
DELETE FROM life_observer_judgement j
USING life_observer o
WHERE j.observer_id = o.id AND o.workspace_id = $1 AND o.user_id = $2
  AND EXISTS (
      SELECT 1 FROM jsonb_array_elements(j.evidence) evidence
      WHERE evidence->>'source_id' = ANY($3::text[])
  );

-- name: DeleteEmptyLifeObservationTopics :exec
DELETE FROM life_observation_topic t
WHERE t.workspace_id = $1 AND t.user_id = $2
  AND NOT EXISTS (SELECT 1 FROM life_observation_topic_judgement tj WHERE tj.topic_id = t.id);

-- name: DeleteEmptyLifeTopics :exec
DELETE FROM life_topic t
WHERE t.workspace_id = $1 AND t.user_id = $2
  AND NOT EXISTS (SELECT 1 FROM life_topic_memory tm WHERE tm.topic_id = t.id);

-- name: CreateLifeForgetTombstone :exec
INSERT INTO life_forget_tombstone (
    workspace_id, user_id, source_type, source_key, content_hash
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: IsLifeMaterialForgotten :one
SELECT EXISTS (
    SELECT 1 FROM life_forget_tombstone
    WHERE workspace_id = $1 AND user_id = $2
      AND source_type = $3 AND source_key = $4 AND content_hash = $5
) AS forgotten;

-- name: GetNextLifeMemoryRevision :one
SELECT COALESCE(MAX(revision), 0)::int + 1 AS revision
FROM life_memory_revision WHERE memory_id = $1;

-- name: CreateLifeMemoryRevision :one
INSERT INTO life_memory_revision (
    memory_id, revision, kind, status, content, confidence, urgency,
    uncertainty, scope, change_type, change_reason, changed_by_type, changed_by_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListLifeMemoryRevisions :many
SELECT * FROM life_memory_revision
WHERE memory_id = $1
ORDER BY revision DESC;

-- name: ListLifeTopics :many
SELECT * FROM life_topic
WHERE workspace_id = $1 AND user_id = $2
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY last_observed_at DESC;

-- name: GetLifeTopicForUser :one
SELECT * FROM life_topic WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: CreateLifeTopic :one
INSERT INTO life_topic (
    workspace_id, user_id, title, summary, status, confidence, uncertainty,
    first_observed_at, last_observed_at, last_reviewed_at, review_after
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, sqlc.narg(last_reviewed_at), sqlc.narg(review_after))
RETURNING *;

-- name: UpdateLifeTopic :one
UPDATE life_topic
SET title = $4, summary = $5, status = $6, confidence = $7,
    uncertainty = $8, last_observed_at = $9,
    last_reviewed_at = sqlc.narg(last_reviewed_at),
    review_after = sqlc.narg(review_after), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: LinkLifeTopicMemory :exec
INSERT INTO life_topic_memory (topic_id, memory_id, relation)
VALUES ($1, $2, $3)
ON CONFLICT (topic_id, memory_id) DO UPDATE SET relation = EXCLUDED.relation;

-- name: ListLifeCommitments :many
SELECT * FROM life_commitment
WHERE workspace_id = $1 AND user_id = $2
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY COALESCE(due_at, revisit_after, created_at) ASC;

-- name: CreateLifeCommitment :one
INSERT INTO life_commitment (
    workspace_id, user_id, source_memory_id, issue_id, content, status,
    due_at, revisit_after
) VALUES (
    $1, $2, sqlc.narg(source_memory_id), sqlc.narg(issue_id), $3, $4,
    sqlc.narg(due_at), sqlc.narg(revisit_after)
)
RETURNING *;

-- name: UpdateLifeCommitmentStatus :one
UPDATE life_commitment
SET status = $4, outcome = $5,
    completed_at = CASE WHEN $4 = 'completed' THEN now() ELSE completed_at END,
    cancelled_at = CASE WHEN $4 = 'cancelled' THEN now() ELSE cancelled_at END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: ConfirmLifeCommitment :one
UPDATE life_commitment
SET status = 'confirmed', due_at = COALESCE(sqlc.narg(due_at), due_at), revisit_after = COALESCE(sqlc.narg(revisit_after), revisit_after), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'candidate'
RETURNING *;

-- name: ListDueLifeCommitments :many
SELECT * FROM life_commitment
WHERE status = 'confirmed'
  AND COALESCE(revisit_after, due_at) <= now()
ORDER BY COALESCE(revisit_after, due_at)
LIMIT $1;

-- name: AdvanceLifeCommitmentRevisit :exec
UPDATE life_commitment
SET revisit_after = $2, updated_at = now()
WHERE id = $1 AND status = 'confirmed';

-- name: ListDueLifeMemoryReviews :many
SELECT m.*, cp.agent_id
FROM life_memory m
JOIN companion_profile cp USING (workspace_id, user_id)
WHERE m.status <> 'archived' AND m.review_after IS NOT NULL AND m.review_after <= now()
ORDER BY m.review_after
LIMIT $1;

-- name: MarkLifeMemoryReviewScheduled :exec
UPDATE life_memory SET review_after = $2 WHERE id = $1;

-- name: ListDueLifeRelationshipEvents :many
SELECT e.*, cp.agent_id
FROM life_relationship_event e
JOIN companion_profile cp USING (workspace_id, user_id)
WHERE e.status IN ('open', 'waiting') AND e.revisit_after IS NOT NULL AND e.revisit_after <= now()
ORDER BY e.revisit_after
LIMIT $1;

-- name: AdvanceLifeRelationshipEventRevisit :exec
UPDATE life_relationship_event
SET revisit_after = $2, updated_at = now()
WHERE id = $1 AND status IN ('open', 'waiting');

-- name: ListLifeInternalThoughts :many
SELECT * FROM life_internal_thought
WHERE workspace_id = $1 AND user_id = $2 AND companion_agent_id = $3
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY last_developed_at DESC;

-- name: UpsertLifeInternalThought :one
INSERT INTO life_internal_thought (
    workspace_id, user_id, companion_agent_id, thought_type, title, content, status, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (workspace_id, user_id, companion_agent_id, thought_type, title)
DO UPDATE SET content = EXCLUDED.content, status = EXCLUDED.status,
    metadata = EXCLUDED.metadata, last_developed_at = now(), updated_at = now()
RETURNING *;

-- name: ListDueLifeInternalThoughts :many
SELECT * FROM life_internal_thought
WHERE status = 'active' AND last_developed_at <= now() - interval '3 days'
ORDER BY last_developed_at
LIMIT $1;

-- name: MarkLifeInternalThoughtScheduled :exec
UPDATE life_internal_thought
SET last_developed_at = now(), updated_at = now()
WHERE id = $1;

-- name: ListAllLifeInternalThoughts :many
SELECT * FROM life_internal_thought
WHERE workspace_id = $1 AND user_id = $2
ORDER BY updated_at DESC;

-- name: CreateLifeCognitionJob :one
INSERT INTO life_cognition_job (
    workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, user_id, job_type, dedupe_key)
DO UPDATE SET scheduled_at = LEAST(life_cognition_job.scheduled_at, EXCLUDED.scheduled_at)
RETURNING *;

-- name: QueueLifeMaterialUnderstanding :one
SELECT queue_life_material_understanding(
    sqlc.arg(workspace_id)::uuid,
    sqlc.arg(user_id)::uuid,
    sqlc.arg(companion_agent_id)::uuid,
    sqlc.arg(material_id)::uuid
) AS job_id;

-- name: ClaimDueLifeCognitionJobs :many
WITH due AS (
    SELECT id
    FROM life_cognition_job
    WHERE status IN ('queued', 'failed')
      AND scheduled_at <= now()
      AND attempt < max_attempts
    ORDER BY scheduled_at
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE life_cognition_job j
SET status = 'running', started_at = now(), attempt = attempt + 1,
    error = '', updated_at = now()
FROM due
WHERE j.id = due.id
RETURNING j.*;

-- name: AttachLifeCognitionJobTask :exec
UPDATE life_cognition_job
SET task_id = $2, updated_at = now()
WHERE id = $1 AND status = 'running';

-- name: UpdateRunningLifeCognitionJobInput :exec
UPDATE life_cognition_job
SET input = $2, updated_at = now()
WHERE id = $1 AND status = 'running';

-- name: CreateLifeCognitionAgentTask :one
INSERT INTO agent_task_queue (
    agent_id, runtime_id, status, priority, context, initiator_user_id,
    force_fresh_session, trigger_summary
) VALUES ($1, $2, 'queued', 0, $3, $4, true, $5)
RETURNING *;

-- name: CompleteLifeCognitionJob :one
UPDATE life_cognition_job
SET status = 'completed', output = $4, completed_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'running'
RETURNING *;

-- name: FailLifeCognitionJob :exec
UPDATE life_cognition_job
SET status = CASE WHEN attempt >= max_attempts THEN 'cancelled' ELSE 'failed' END,
    error = $2, scheduled_at = now() + interval '5 minutes', updated_at = now()
WHERE id = $1;

-- name: GetLifeCognitionJobForTask :one
SELECT * FROM life_cognition_job WHERE task_id = $1;

-- name: ListRunningLifeCognitionJobsWithTask :many
SELECT j.*, t.status AS task_status, t.error AS task_error, t.result AS task_result
FROM life_cognition_job j
JOIN agent_task_queue t ON t.id = j.task_id
WHERE j.status = 'running' AND t.status IN ('completed', 'failed', 'cancelled')
ORDER BY j.started_at
LIMIT $1;

-- name: ListLifeCognitionJobs :many
SELECT * FROM life_cognition_job
WHERE workspace_id = $1 AND user_id = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: RetryLifeCognitionJob :one
UPDATE life_cognition_job
SET status = 'queued', attempt = 0, task_id = NULL, error = '',
    scheduled_at = now(), started_at = NULL, completed_at = NULL, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'cancelled'
RETURNING *;

-- name: UpsertLifeProactivePolicy :one
INSERT INTO life_proactive_policy (
    workspace_id, user_id, enabled, timezone, quiet_hours, minimum_interval, next_check_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET
    enabled = EXCLUDED.enabled, timezone = EXCLUDED.timezone,
    quiet_hours = EXCLUDED.quiet_hours, minimum_interval = EXCLUDED.minimum_interval,
    next_check_at = EXCLUDED.next_check_at, updated_at = now()
RETURNING *;

-- name: GetLifeProactivePolicy :one
SELECT * FROM life_proactive_policy WHERE workspace_id = $1 AND user_id = $2;

-- name: ListDueLifeProactivePolicies :many
SELECT p.*, cp.agent_id
FROM life_proactive_policy p
JOIN companion_profile cp USING (workspace_id, user_id)
WHERE p.enabled AND p.next_check_at <= now()
ORDER BY p.next_check_at
LIMIT $1;

-- name: ListLifeProfilesForScheduling :many
SELECT cp.* FROM companion_profile cp ORDER BY cp.created_at;

-- name: AdvanceLifeProactivePolicy :exec
UPDATE life_proactive_policy
SET next_check_at = $3, updated_at = now()
WHERE workspace_id = $1 AND user_id = $2;

-- name: CreateLifeProactiveCheckFull :one
INSERT INTO life_proactive_check (
    workspace_id, user_id, companion_agent_id, status, trigger_source,
    reason, context_snapshot, message
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: RecordLifeProactiveSpeech :exec
UPDATE life_proactive_policy
SET last_spoke_at = now(), unanswered_count = unanswered_count + 1, updated_at = now()
WHERE workspace_id = $1 AND user_id = $2;

-- name: ListPendingLifeProactiveReviews :many
SELECT c.*, cp.agent_id
FROM life_proactive_check c
JOIN companion_profile cp USING (workspace_id, user_id)
WHERE c.status = 'spoke'
  AND c.user_responded_at IS NOT NULL
  AND c.value_assessment = ''
ORDER BY c.user_responded_at
LIMIT $1;

-- name: RecordLifeProactiveAssessment :one
UPDATE life_proactive_check
SET value_assessment = $4
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
  AND status = 'spoke' AND user_responded_at IS NOT NULL
RETURNING *;

-- name: AdjustLifeProactiveInterval :exec
UPDATE life_proactive_policy
SET minimum_interval = make_interval(hours => sqlc.arg(hours)::int), updated_at = now()
WHERE workspace_id = $1 AND user_id = $2;

-- name: CreateLifeExperimentObservation :one
INSERT INTO life_experiment_observation (
    round_id, material_id, observation_type, content, captured_by, observed_at
) VALUES ($1, sqlc.narg(material_id), $2, $3, $4, $5)
RETURNING *;

-- name: ListLifeExperimentObservations :many
SELECT * FROM life_experiment_observation
WHERE round_id = $1 ORDER BY observed_at;

-- name: ListRunningLifeExperimentRoundsForChecks :many
SELECT r.*, e.workspace_id, e.user_id, cp.agent_id
FROM life_experiment_round r
JOIN life_experiment e ON e.id = r.experiment_id
JOIN companion_profile cp USING (workspace_id, user_id)
WHERE r.status = 'running' OR (r.status = 'awaiting_review' AND r.review_draft IS NULL)
ORDER BY r.ends_at
LIMIT $1;

-- name: SaveLifeExperimentReviewDraft :one
UPDATE life_experiment_round r
SET review_draft = $4, updated_at = now()
FROM life_experiment e
WHERE r.id = $1 AND r.experiment_id = e.id
  AND e.workspace_id = $2 AND e.user_id = $3
  AND r.status IN ('running', 'awaiting_review')
RETURNING r.*;

-- name: ListLifeModules :many
SELECT * FROM life_module
WHERE workspace_id = $1 AND user_id = $2
ORDER BY updated_at DESC;

-- name: GetLifeModuleForUser :one
SELECT * FROM life_module WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: UpdateLifeModuleStatus :one
UPDATE life_module
SET status = $4,
    enabled_at = CASE WHEN $4 = 'active' THEN now() ELSE enabled_at END,
    disabled_at = CASE WHEN $4 IN ('paused', 'retired') THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: GetNextLifeModuleVersion :one
SELECT COALESCE(MAX(version), 0)::int + 1 AS version FROM life_module_version WHERE module_id = $1;

-- name: SetLifeModuleCurrentVersion :exec
UPDATE life_module SET current_version = $2, status = 'active', enabled_at = now(), disabled_at = NULL, updated_at = now() WHERE id = $1;

-- name: ListLifeModuleVersions :many
SELECT * FROM life_module_version WHERE module_id = $1 ORDER BY version DESC;

-- name: CreateLifeModule :one
INSERT INTO life_module (workspace_id, user_id, source_experiment_id, name, status)
VALUES ($1, $2, sqlc.narg(source_experiment_id), $3, $4)
RETURNING *;

-- name: CreateLifeModuleVersion :one
INSERT INTO life_module_version (
    module_id, version, definition, change_reason, confirmed_at, confirmed_by_id
) VALUES ($1, $2, $3, $4, now(), $5)
RETURNING *;

-- name: ListLifeObservers :many
SELECT * FROM life_observer
WHERE workspace_id = $1 AND user_id = $2
ORDER BY created_at;

-- name: GetLifeObserverForUser :one
SELECT * FROM life_observer WHERE id = $1 AND workspace_id = $2 AND user_id = $3;

-- name: UpdateLifeObserverStatus :one
UPDATE life_observer
SET status = $4, next_run_at = CASE WHEN $4 = 'active' THEN now() ELSE next_run_at END, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: GetNextLifeObserverVersion :one
SELECT COALESCE(MAX(version), 0)::int + 1 AS version FROM life_observer_version WHERE observer_id = $1;

-- name: SetLifeObserverCurrentVersion :exec
UPDATE life_observer SET current_version = $2, updated_at = now() WHERE id = $1;

-- name: ListDueLifeObservers :many
SELECT * FROM life_observer
WHERE status = 'active' AND next_run_at <= now()
ORDER BY next_run_at
LIMIT $1;

-- name: AdvanceLifeObserverSchedule :exec
UPDATE life_observer
SET last_run_at = now(), next_run_at = $2, updated_at = now()
WHERE id = $1;

-- name: CreateLifeObserver :one
INSERT INTO life_observer (
    workspace_id, user_id, agent_id, name, basis_type, status, current_version
) VALUES ($1, $2, $3, $4, $5, 'active', 1)
RETURNING *;

-- name: CreateLifeObserverVersion :one
INSERT INTO life_observer_version (
    observer_id, version, personality, perspective, expression_profile,
    change_reason, confirmed_at, confirmed_by_id
) VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
RETURNING *;

-- name: GetLifeObserverForAgent :one
SELECT * FROM life_observer
WHERE workspace_id = $1 AND user_id = $2 AND agent_id = $3 AND status = 'active';

-- name: GetCurrentLifeObserverVersion :one
SELECT v.* FROM life_observer_version v
JOIN life_observer o ON o.id = v.observer_id AND o.current_version = v.version
WHERE o.id = $1;

-- name: ListLifeObserverKnowledge :many
SELECT * FROM life_observer_knowledge
WHERE observer_id = $1 ORDER BY created_at;

-- name: GetLifeObserverKnowledgeForUser :one
SELECT k.* FROM life_observer_knowledge k
JOIN life_observer o ON o.id = k.observer_id
WHERE k.id = $1 AND o.workspace_id = $2 AND o.user_id = $3 AND o.status = 'active';

-- name: CreateLifeObserverKnowledge :one
INSERT INTO life_observer_knowledge (observer_id, title, content, source)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateLifeObserverJudgement :one
INSERT INTO life_observer_judgement (
    observer_id, status, title, content, evidence, confidence, uncertainty, published_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg(published_at))
RETURNING *;

-- name: ListPublishedLifeObserverJudgements :many
SELECT j.*, o.name AS observer_name
FROM life_observer_judgement j
JOIN life_observer o ON o.id = j.observer_id
WHERE o.workspace_id = $1 AND o.user_id = $2 AND j.status = 'published'
ORDER BY j.published_at DESC;

-- name: ListLifeObserverJudgementsForUser :many
SELECT j.*, o.name AS observer_name
FROM life_observer_judgement j
JOIN life_observer o ON o.id = j.observer_id
WHERE o.workspace_id = $1 AND o.user_id = $2
ORDER BY j.created_at DESC;

-- name: ListLifeObservationTopics :many
SELECT * FROM life_observation_topic
WHERE workspace_id = $1 AND user_id = $2
ORDER BY created_at DESC;

-- name: UpdateLifeObservationTopic :one
UPDATE life_observation_topic
SET status = $4, companion_response = $5,
    surfaced_at = CASE WHEN $4 IN ('surfaced','discussing') THEN COALESCE(surfaced_at, now()) ELSE surfaced_at END,
    resolved_at = CASE WHEN $4 = 'resolved' THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: CreateLifeObservationTopic :one
INSERT INTO life_observation_topic (
    workspace_id, user_id, title, summary, status, surfaced_at
) VALUES ($1, $2, $3, $4, $5, sqlc.narg(surfaced_at))
RETURNING *;

-- name: MergeLifeObservationTopic :one
UPDATE life_observation_topic
SET title = $4, summary = $5, status = $6,
    surfaced_at = CASE WHEN $6 IN ('surfaced','discussing') THEN COALESCE(surfaced_at, now()) ELSE surfaced_at END,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3
RETURNING *;

-- name: LinkLifeObservationTopicJudgement :exec
INSERT INTO life_observation_topic_judgement (topic_id, judgement_id)
VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: CreateLifeChronicleRevision :one
INSERT INTO life_chronicle_revision (
    entry_id, revision, facts, feelings, understanding_then,
    understanding_later, actions, change_reason
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetNextLifeChronicleRevision :one
SELECT COALESCE(MAX(revision), 0)::int + 1 AS revision FROM life_chronicle_revision WHERE entry_id = $1;

-- name: SetLifeChronicleRevision :exec
UPDATE life_chronicle_entry SET revision = $2 WHERE id = $1;

-- name: CreateGeneratedLifeChronicleEntry :one
INSERT INTO life_chronicle_entry (
    workspace_id, user_id, period_start, period_end, facts, feelings,
    understanding_then, understanding_later, actions, period_kind, status, generated_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'published', $11)
ON CONFLICT (workspace_id, user_id, period_kind, period_start, period_end)
WHERE status = 'published'
DO UPDATE SET facts = EXCLUDED.facts,
    feelings = EXCLUDED.feelings,
    understanding_then = EXCLUDED.understanding_then,
    understanding_later = EXCLUDED.understanding_later,
    actions = EXCLUDED.actions,
    generated_by = EXCLUDED.generated_by,
    revision = life_chronicle_entry.revision + 1,
    updated_at = now()
RETURNING *;

-- name: ListLifeChroniclesInPeriod :many
SELECT * FROM life_chronicle_entry
WHERE workspace_id = $1 AND user_id = $2 AND status = 'published'
  AND period_kind = $3 AND period_start >= $4 AND period_end <= $5
ORDER BY period_start, id;

-- name: ListLifeChronicleContextEntries :many
SELECT * FROM life_chronicle_entry
WHERE workspace_id = $1 AND user_id = $2 AND status = 'published'
  AND (period_kind IN ('month', 'year', 'event') OR period_end >= now() - interval '90 days')
ORDER BY period_start DESC, id DESC;

-- name: CreateLifeChronicleEvidenceLink :exec
INSERT INTO life_chronicle_evidence (entry_id, source_type, source_id)
VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: CreateLifeUpgradeEvaluation :one
INSERT INTO life_upgrade_evaluation (
    workspace_id, user_id, identity_version_id, candidate_label,
    baseline_label, scenarios, status
) VALUES ($1, $2, sqlc.narg(identity_version_id), $3, $4, $5, 'pending')
RETURNING *;

-- name: ListLifeUpgradeEvaluations :many
SELECT * FROM life_upgrade_evaluation
WHERE workspace_id = $1 AND user_id = $2
ORDER BY created_at DESC;

-- name: StartLifeUpgradeEvaluation :one
UPDATE life_upgrade_evaluation SET status = 'running', started_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'pending'
RETURNING *;

-- name: CompleteLifeUpgradeEvaluation :one
UPDATE life_upgrade_evaluation
SET status = $4, result = $5, rollback_recommended = $6, completed_at = now()
WHERE id = $1 AND workspace_id = $2 AND user_id = $3 AND status = 'running'
RETURNING *;
