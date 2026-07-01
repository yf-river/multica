DROP INDEX IF EXISTS idx_squad_workspace_visibility;

ALTER TABLE squad
    DROP COLUMN IF EXISTS visibility;
