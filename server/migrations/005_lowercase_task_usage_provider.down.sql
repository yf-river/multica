-- Lowercase provider values remain valid for the previous application. Only
-- remove the new write constraints; lossy historical casing is not restored.
ALTER TABLE task_usage
    DROP CONSTRAINT IF EXISTS task_usage_provider_canonical;
ALTER TABLE task_usage_hourly
    DROP CONSTRAINT IF EXISTS task_usage_hourly_provider_canonical;
ALTER TABLE task_usage_hourly_dirty
    DROP CONSTRAINT IF EXISTS task_usage_hourly_dirty_provider_canonical;
