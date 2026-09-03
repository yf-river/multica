CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_experiment_user_updated
    ON public.life_experiment (workspace_id, user_id, updated_at DESC);
