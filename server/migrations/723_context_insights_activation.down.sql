UPDATE context_insights_config SET enabled_since=now() WHERE enabled_since IS NULL;
ALTER TABLE context_insights_config ALTER COLUMN enabled_since SET NOT NULL, ALTER COLUMN enabled_since SET DEFAULT now();
