-- Internal Squad agents used to duplicate identity in the top-level Chinese
-- keys `模板` / `角色` while current code stores stable identity under
-- `internal_squad`. Normalize the two current built-in templates once, then
-- remove the duplicate presentation fields. Existing structured values win.
WITH role_map(template_key, role_name, role_key) AS (
    VALUES
        ('user-center-sop-flow-v2', 'PM-项目经理', 'pm'),
        ('user-center-sop-flow-v2', '01-需求澄清', '01-clarify'),
        ('user-center-sop-flow-v2', '02-方案设计', '02-design'),
        ('user-center-sop-flow-v2', '03-任务拆分', '03-task-split'),
        ('user-center-sop-flow-v2', '04-开发', '04-implement'),
        ('user-center-sop-flow-v2', '05-验证测试', '05-verify'),
        ('multica-coding', '队长', 'captain'),
        ('multica-coding', '方案设计者', 'designer'),
        ('multica-coding', '开发者', 'developer'),
        ('multica-coding', '验收者', 'acceptor'),
        ('multica-coding', '规约维护者', 'spec-maintainer'),
        ('multica-coding', '部署运行者', 'operator')
)
UPDATE agent AS a
SET runtime_config = jsonb_set(
        a.runtime_config,
        '{internal_squad}',
        jsonb_build_object(
            'template_key', role_map.template_key,
            'role_key', role_map.role_key,
            'squad_scope', CASE WHEN a.scope = 'personal' THEN 'personal' ELSE 'workspace' END,
            'agent_scope', a.scope,
            'owner_id', CASE WHEN a.scope = 'personal' THEN COALESCE(a.owner_id::text, '') ELSE '' END
        ) || CASE
            WHEN jsonb_typeof(a.runtime_config->'internal_squad') = 'object'
                THEN a.runtime_config->'internal_squad'
            ELSE '{}'::jsonb
        END,
        true
    ) - '用途' - '角色' - '模板'
FROM role_map
WHERE a.runtime_config->>'模板' = role_map.template_key
  AND a.runtime_config->>'角色' = role_map.role_name;
