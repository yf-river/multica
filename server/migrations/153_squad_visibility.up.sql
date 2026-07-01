ALTER TABLE squad
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('workspace', 'personal'));

CREATE INDEX idx_squad_workspace_visibility
    ON squad(workspace_id, visibility)
    WHERE archived_at IS NULL;
