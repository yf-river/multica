-- Persist the external preparation allowance across runs, retries and worker recovery.
ALTER TABLE supervision_round
 ADD COLUMN IF NOT EXISTS preparation_http_requests integer NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS preparation_started_at timestamptz;
