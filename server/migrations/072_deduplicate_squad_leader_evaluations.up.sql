DELETE FROM activity_log
WHERE id IN (
    SELECT id
    FROM (
        SELECT
            id,
            row_number() OVER (
                PARTITION BY issue_id, actor_id, details->>'task_id'
                ORDER BY created_at, id
            ) AS occurrence
        FROM activity_log
        WHERE actor_type = 'agent'
          AND action = 'squad_leader_evaluated'
          AND details->>'task_id' IS NOT NULL
    ) ranked
    WHERE occurrence > 1
);

DROP INDEX IF EXISTS idx_activity_log_squad_no_action_task;

CREATE UNIQUE INDEX IF NOT EXISTS activity_log_squad_evaluation_task_unique
    ON activity_log (issue_id, actor_id, (details->>'task_id'))
    WHERE actor_type = 'agent' AND action = 'squad_leader_evaluated';
