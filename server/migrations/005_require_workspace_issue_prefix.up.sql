UPDATE workspace
SET issue_prefix = COALESCE(
    NULLIF(upper(left(regexp_replace(name, '[^a-zA-Z]', '', 'g'), 3)), ''),
    'WS'
)
WHERE btrim(issue_prefix) = '';

ALTER TABLE workspace
    ALTER COLUMN issue_prefix DROP DEFAULT,
    ADD CONSTRAINT workspace_issue_prefix_nonempty CHECK (btrim(issue_prefix) <> '');
