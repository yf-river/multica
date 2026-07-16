UPDATE prompt_evaluation_run
SET metrics = (metrics - '输入token' - '输出token' - 'prompt_version')
    || CASE
        WHEN NOT metrics ? '输入 token' AND metrics ? '输入token'
            THEN jsonb_build_object('输入 token', metrics->'输入token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN NOT metrics ? '输出 token' AND metrics ? '输出token'
            THEN jsonb_build_object('输出 token', metrics->'输出token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN NOT metrics ? '提示词版本' AND metrics ? 'prompt_version'
            THEN jsonb_build_object('提示词版本', metrics->'prompt_version')
        ELSE '{}'::jsonb
    END
WHERE metrics ?| ARRAY['输入token', '输出token', 'prompt_version'];
