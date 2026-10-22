ALTER TABLE supervision_config ADD COLUMN IF NOT EXISTS instructions text NOT NULL DEFAULT '';
