CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_memory_user_kind_updated
    ON public.life_memory (workspace_id, user_id, kind, updated_at DESC);
