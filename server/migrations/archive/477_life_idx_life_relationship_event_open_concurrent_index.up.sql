CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_relationship_event_open
    ON public.life_relationship_event (workspace_id, user_id, status, revisit_after);
