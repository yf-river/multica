-- 1. Persist allowance and recheck state together; this unapplied extension keeps legacy defaults compatible.
ALTER TABLE supervision_round
 ADD COLUMN IF NOT EXISTS preparation_http_requests integer NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS preparation_started_at timestamptz,
 ADD COLUMN IF NOT EXISTS preparation_recheck_scheduled boolean NOT NULL DEFAULT false;
