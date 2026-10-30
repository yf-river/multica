ALTER TABLE context_insights_session ADD COLUMN projection_head bytea NOT NULL DEFAULT '\x7b7d'::bytea;
UPDATE context_insights_session SET projection_head=convert_to(jsonb_set(projections,'{contextTimeline}',COALESCE(projections->'contextTimeline','{}'::jsonb)-ARRAY['requests','events','nodes','archive','fileOps','commands','spans'])::text,'UTF8');
ALTER TABLE context_insights_session ALTER COLUMN projections DROP DEFAULT;
ALTER TABLE context_insights_session ALTER COLUMN projections TYPE bytea USING convert_to(projections::text,'UTF8');
ALTER TABLE context_insights_session ALTER COLUMN projections SET DEFAULT '\x7b7d'::bytea;
ALTER TABLE context_insights_event ALTER COLUMN content TYPE bytea USING convert_to(content::text,'UTF8');
