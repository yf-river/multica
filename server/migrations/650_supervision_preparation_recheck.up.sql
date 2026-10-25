ALTER TABLE supervision_round ADD COLUMN IF NOT EXISTS preparation_recheck_scheduled boolean NOT NULL DEFAULT false;
ALTER TABLE supervision_manual_preparation ADD COLUMN IF NOT EXISTS preparation_recheck_scheduled boolean NOT NULL DEFAULT false;
