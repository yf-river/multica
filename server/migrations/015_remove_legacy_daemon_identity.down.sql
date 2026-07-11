ALTER TABLE agent_runtime
ADD COLUMN IF NOT EXISTS legacy_daemon_id text;
