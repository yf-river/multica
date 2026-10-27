CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_companion_profile_agent
    ON public.companion_profile (agent_id);
