UPDATE agent
SET runtime_config = jsonb_set(
    runtime_config,
    '{internal_squad}',
    CASE
        WHEN jsonb_typeof(runtime_config->'internal_squad') = 'object'
            THEN runtime_config->'internal_squad'
        ELSE '{}'::jsonb
    END || jsonb_build_object(
        'role_key',
        CASE name
            WHEN 'PM-项目经理' THEN 'pm'
            WHEN '01-需求澄清' THEN '01-clarify'
            WHEN '02-方案设计' THEN '02-design'
            WHEN '03-任务拆分' THEN '03-task-split'
            WHEN '04-开发' THEN '04-implement'
            WHEN '05-验证测试' THEN '05-verify'
        END
    ),
    true
)
WHERE name IN (
    'PM-项目经理',
    '01-需求澄清',
    '02-方案设计',
    '03-任务拆分',
    '04-开发',
    '05-验证测试'
)
  AND COALESCE(runtime_config->'internal_squad'->>'role_key', '') = '';
