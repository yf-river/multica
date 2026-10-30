ALTER TABLE context_insights_config ALTER COLUMN enabled_since DROP NOT NULL, ALTER COLUMN enabled_since DROP DEFAULT;
UPDATE context_insights_config SET enabled_since=NULL WHERE NOT EXISTS (SELECT 1 FROM context_insights_session);
