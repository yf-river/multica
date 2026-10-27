CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_material_user_time
    ON public.life_material (workspace_id, user_id, occurred_at DESC);
