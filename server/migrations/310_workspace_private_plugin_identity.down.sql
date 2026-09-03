-- Migration 344 replaces the V1 plugin schema wholesale.  In a rollback that
-- crosses that boundary the table is already gone, so this historical down
-- step must remain a safe no-op instead of aborting the whole rollback.
DO $$
BEGIN
    IF to_regclass('public.plugin_identity') IS NOT NULL THEN
        ALTER TABLE plugin_identity
            DROP CONSTRAINT IF EXISTS plugin_identity_scope_check,
            DROP COLUMN IF EXISTS owner_workspace_id;
    END IF;
END
$$;
