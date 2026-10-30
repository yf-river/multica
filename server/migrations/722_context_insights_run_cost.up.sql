ALTER TABLE context_insights_run ADD COLUMN cost bytea, ADD COLUMN seq bigint NOT NULL DEFAULT -1;
