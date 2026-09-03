-- Life relationships are validated and cleaned up by application transactions.
-- Drop constraints created by an earlier unpublished Life migration when an
-- existing development database is upgraded to this baseline.
DO $$
DECLARE
    constraint_row record;
BEGIN
    FOR constraint_row IN
        SELECT source_ns.nspname AS source_schema,
               source_table.relname AS source_table,
               constraint_def.conname
        FROM pg_constraint AS constraint_def
        JOIN pg_class AS source_table ON source_table.oid = constraint_def.conrelid
        JOIN pg_namespace AS source_ns ON source_ns.oid = source_table.relnamespace
        JOIN pg_class AS target_table ON target_table.oid = constraint_def.confrelid
        JOIN pg_namespace AS target_ns ON target_ns.oid = target_table.relnamespace
        WHERE constraint_def.contype = 'f'
          AND source_ns.nspname = 'public'
          AND target_ns.nspname = 'public'
          AND (source_table.relname LIKE 'life_%'
               OR source_table.relname = 'companion_profile'
               OR target_table.relname LIKE 'life_%'
               OR target_table.relname = 'companion_profile')
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.%I DROP CONSTRAINT IF EXISTS %I',
            constraint_row.source_schema,
            constraint_row.source_table,
            constraint_row.conname
        );
    END LOOP;
END;
$$;
