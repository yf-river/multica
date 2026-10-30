ALTER TABLE upstream_sync_config ADD COLUMN baseline_committed_at timestamptz;
ALTER TABLE upstream_sync_config ADD COLUMN head_committed_at timestamptz;
ALTER TABLE upstream_sync_config ADD COLUMN pending_commits integer;
