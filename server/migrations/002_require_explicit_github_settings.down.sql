ALTER TABLE workspace
    DROP CONSTRAINT IF EXISTS workspace_github_settings_shape;

ALTER TABLE workspace
    ALTER COLUMN settings SET DEFAULT '{}'::jsonb;

UPDATE workspace
SET settings = CASE
        WHEN settings -> 'github_auto_link_prs_enabled' = 'true'::jsonb
            THEN settings - 'github_auto_link_prs_enabled'
        ELSE settings
    END;

UPDATE workspace
SET settings = CASE
        WHEN settings -> 'co_authored_by_enabled' = 'true'::jsonb
            THEN settings - 'co_authored_by_enabled'
        ELSE settings
    END;

UPDATE workspace
SET settings = CASE
        WHEN settings -> 'github_pr_sidebar_enabled' = 'true'::jsonb
            THEN settings - 'github_pr_sidebar_enabled'
        ELSE settings
    END;

UPDATE workspace
SET settings = CASE
        WHEN settings -> 'github_enabled' = 'true'::jsonb
            THEN settings - 'github_enabled'
        ELSE settings
    END;
