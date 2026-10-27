CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_commitment_due
    ON public.life_commitment (workspace_id, user_id, status, due_at);
