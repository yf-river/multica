CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_proactive_check_user_checked
    ON public.life_proactive_check (workspace_id, user_id, checked_at DESC);
