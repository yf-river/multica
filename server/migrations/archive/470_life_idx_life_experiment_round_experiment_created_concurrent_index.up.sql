CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_experiment_round_experiment_created
    ON public.life_experiment_round (experiment_id, created_at DESC);
