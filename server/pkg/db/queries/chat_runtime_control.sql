-- name: GetChatRuntimeSelection :one
-- 本次输入封口后的用户命令不参与当前执行；已保存的计划批准是退出计划模式的用户决定。
WITH boundary AS (
    SELECT m.created_at, m.id, m.task_id FROM chat_message m
    WHERE m.chat_session_id = sqlc.arg(chat_session_id) AND m.id = sqlc.arg(input_message_id)
), user_controls AS (
    SELECT btrim(m.content, E' \t\n\r') AS command, m.created_at, m.id
    FROM chat_message m CROSS JOIN boundary b
    WHERE m.chat_session_id = sqlc.arg(chat_session_id) AND m.role = 'user'
      AND m.message_kind NOT IN ('channel_command', 'onboarding_kickoff')
      AND (m.created_at, m.id) <= (b.created_at, b.id)
      AND NOT EXISTS (
          SELECT 1 FROM agent_task_queue pending
          WHERE pending.id = m.task_id AND pending.status IN ('queued', 'deferred')
            AND m.task_id IS DISTINCT FROM b.task_id
      )
      AND (btrim(m.content, E' \t\n\r') = '/plan' OR btrim(m.content, E' \t\n\r') LIKE '/plan %'
        OR btrim(m.content, E' \t\n\r') = '/goal' OR btrim(m.content, E' \t\n\r') LIKE '/goal %')
), plan_selections AS (
    SELECT command <> '/plan off' AS active, created_at, id FROM user_controls
    WHERE command = '/plan' OR command LIKE '/plan %'
    UNION ALL
    SELECT false, i.responded_at, i.id
    FROM task_interaction i JOIN agent_task_queue t ON t.id = i.task_id CROSS JOIN boundary b
    WHERE t.chat_session_id = sqlc.arg(chat_session_id) AND t.created_at <= b.created_at
      AND i.kind = 'plan' AND i.status = 'responded' AND i.response->>'decision' = 'allow'
)
SELECT EXISTS(SELECT 1 FROM plan_selections) AS has_plan_mode,
    COALESCE((SELECT active FROM plan_selections ORDER BY created_at DESC, id DESC LIMIT 1), false)::boolean AS plan_mode,
    EXISTS(SELECT 1 FROM user_controls WHERE command = '/goal' OR command LIKE '/goal %') AS native_goal;
