CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_memory_dependency_derived
    ON public.life_memory_dependency (derived_memory_id);
