CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_observer_judgement_status
    ON public.life_observer_judgement (observer_id, status, created_at DESC);
