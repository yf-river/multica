CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_chronicle_user_period
    ON public.life_chronicle_entry (workspace_id, user_id, period_start DESC);
