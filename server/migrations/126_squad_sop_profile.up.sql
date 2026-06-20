ALTER TABLE squad
  ADD COLUMN sop_profile JSONB NOT NULL DEFAULT '{}'::jsonb;
