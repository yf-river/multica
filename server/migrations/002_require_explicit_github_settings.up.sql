UPDATE workspace
SET settings = settings || jsonb_build_object(
    'github_enabled', CASE
        WHEN jsonb_typeof(settings -> 'github_enabled') = 'boolean' THEN settings -> 'github_enabled'
        ELSE 'true'::jsonb
    END,
    'github_pr_sidebar_enabled', CASE
        WHEN jsonb_typeof(settings -> 'github_pr_sidebar_enabled') = 'boolean' THEN settings -> 'github_pr_sidebar_enabled'
        ELSE 'true'::jsonb
    END,
    'co_authored_by_enabled', CASE
        WHEN jsonb_typeof(settings -> 'co_authored_by_enabled') = 'boolean' THEN settings -> 'co_authored_by_enabled'
        ELSE 'true'::jsonb
    END
)
WHERE NOT settings ? 'github_enabled'
   OR jsonb_typeof(settings -> 'github_enabled') <> 'boolean'
   OR NOT settings ? 'github_pr_sidebar_enabled'
   OR jsonb_typeof(settings -> 'github_pr_sidebar_enabled') <> 'boolean'
   OR NOT settings ? 'co_authored_by_enabled'
   OR jsonb_typeof(settings -> 'co_authored_by_enabled') <> 'boolean';

ALTER TABLE workspace
    ALTER COLUMN settings SET DEFAULT '{
        "github_enabled": true,
        "github_pr_sidebar_enabled": true,
        "co_authored_by_enabled": true
    }'::jsonb;

ALTER TABLE workspace
    ADD CONSTRAINT workspace_github_settings_shape CHECK (
        settings ? 'github_enabled'
        AND jsonb_typeof(settings -> 'github_enabled') = 'boolean'
        AND settings ? 'github_pr_sidebar_enabled'
        AND jsonb_typeof(settings -> 'github_pr_sidebar_enabled') = 'boolean'
        AND settings ? 'co_authored_by_enabled'
        AND jsonb_typeof(settings -> 'co_authored_by_enabled') = 'boolean'
    );
