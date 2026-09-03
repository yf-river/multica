-- The index is restored by the direction-specific pre-migration hook.  A
-- concurrent index cannot be created from a conditional SQL/DO block because
-- PostgreSQL forbids CREATE INDEX CONCURRENTLY inside a transaction block.
SELECT 1;
