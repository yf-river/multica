ALTER TABLE runtime_profile
    ADD COLUMN IF NOT EXISTS visibility text NOT NULL DEFAULT 'workspace'
    CHECK (visibility IN ('workspace', 'private'));
