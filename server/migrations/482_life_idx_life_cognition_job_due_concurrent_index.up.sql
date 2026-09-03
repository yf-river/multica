CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_cognition_job_due
    ON public.life_cognition_job (status, scheduled_at) WHERE status IN ('queued', 'failed');
