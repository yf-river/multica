CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_derivation_target
    ON public.life_derivation (workspace_id, user_id, target_type, target_id);
