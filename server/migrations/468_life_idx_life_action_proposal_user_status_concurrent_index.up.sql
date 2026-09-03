CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_action_proposal_user_status
    ON public.life_action_proposal (workspace_id, user_id, status, updated_at DESC);
