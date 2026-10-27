CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS life_identity_one_active
    ON public.life_identity_version (workspace_id, user_id) WHERE status = 'active';
