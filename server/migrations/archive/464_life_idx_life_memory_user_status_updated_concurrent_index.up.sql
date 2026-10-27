CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_memory_user_status_updated
    ON public.life_memory (workspace_id, user_id, status, updated_at DESC);
