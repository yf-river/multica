CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_experiment_observation_round
    ON public.life_experiment_observation (round_id, observed_at);
