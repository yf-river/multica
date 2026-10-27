DROP TRIGGER IF EXISTS trg_plugin_installation_config_append_only ON plugin_installation_config;
DROP TABLE IF EXISTS plugin_installation_config;
DROP TABLE IF EXISTS plugin_remote_mcp_secret;

DO $$
BEGIN
    IF to_regclass('public.plugin_contribution') IS NULL THEN
        RETURN;
    END IF;
    EXECUTE 'ALTER TABLE plugin_contribution
             DROP CONSTRAINT IF EXISTS plugin_contribution_type_check';
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.plugin_contribution'::regclass
          AND conname = 'plugin_contribution_type_check'
    ) THEN
        EXECUTE 'ALTER TABLE plugin_contribution
                 ADD CONSTRAINT plugin_contribution_type_check
                 CHECK (type IN (''agent.skill.v1''))';
    END IF;
END
$$;
