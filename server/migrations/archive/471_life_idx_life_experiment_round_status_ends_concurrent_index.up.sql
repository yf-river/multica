CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_experiment_round_status_ends
    ON public.life_experiment_round (status, ends_at) WHERE status = 'running';
