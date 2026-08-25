ALTER TABLE workspace
    DROP CONSTRAINT IF EXISTS workspace_issue_prefix_nonempty,
    ALTER COLUMN issue_prefix SET DEFAULT '';
