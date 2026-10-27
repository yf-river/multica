-- The irreversible data cleanup runs in the migration runner's transaction
-- hook, where it can materialize and audit the complete target set before
-- deleting dependent rows. This marker keeps the schema history explicit.
SELECT 1;
