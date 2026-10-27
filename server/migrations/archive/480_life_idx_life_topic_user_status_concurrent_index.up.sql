CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_topic_user_status
    ON public.life_topic (workspace_id, user_id, status, last_observed_at DESC);
