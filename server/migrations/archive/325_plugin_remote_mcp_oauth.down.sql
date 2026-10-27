DROP TABLE IF EXISTS plugin_remote_mcp_oauth_state;

DO $$
BEGIN
    -- A full rollback can already have removed the objects introduced by
    -- later migrations.  Keep this down migration idempotent in that shape;
    -- static SQL against a missing table fails before IF EXISTS can help.
    IF to_regclass('public.plugin_installation_config') IS NULL THEN
        RETURN;
    END IF;
    IF to_regclass('public.plugin_remote_mcp_secret') IS NOT NULL THEN
        EXECUTE 'DELETE FROM plugin_remote_mcp_secret
                 WHERE id IN (SELECT secret_ref FROM plugin_installation_config WHERE auth_type = ''oauth'')';
    END IF;
    EXECUTE 'DELETE FROM plugin_installation_config WHERE auth_type = ''oauth''';
    EXECUTE 'ALTER TABLE plugin_installation_config
             DROP CONSTRAINT IF EXISTS plugin_installation_config_discovered_digest_check,
             DROP COLUMN IF EXISTS discovered_schema_digest,
             DROP COLUMN IF EXISTS discovered_tools';
    EXECUTE 'ALTER TABLE plugin_installation_config
             DROP CONSTRAINT IF EXISTS plugin_installation_config_auth_type_check';
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.plugin_installation_config'::regclass
          AND conname = 'plugin_installation_config_auth_type_check'
    ) THEN
        EXECUTE 'ALTER TABLE plugin_installation_config
                 ADD CONSTRAINT plugin_installation_config_auth_type_check
                 CHECK (auth_type IN (''none'', ''bearer'', ''header''))';
    END IF;
END
$$;
