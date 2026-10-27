CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_memory_evidence_source
    ON public.life_memory_evidence (source_type, source_id);
