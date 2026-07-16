UPDATE prompt_evaluation_run
SET metrics = metrics
    || CASE
        WHEN metrics ? '输入 token'
            THEN jsonb_build_object('输入token', metrics->'输入 token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN metrics ? '输出 token'
            THEN jsonb_build_object('输出token', metrics->'输出 token')
        ELSE '{}'::jsonb
    END
    || CASE
        WHEN metrics ? '提示词版本'
            THEN jsonb_build_object('prompt_version', metrics->'提示词版本')
        ELSE '{}'::jsonb
    END
WHERE metrics ?| ARRAY['输入 token', '输出 token', '提示词版本'];
