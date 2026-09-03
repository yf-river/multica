CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS life_chronicle_one_published_period
    ON public.life_chronicle_entry (workspace_id, user_id, period_kind, period_start, period_end) WHERE status = 'published';
