CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_life_chronicle_evidence_source
    ON public.life_chronicle_evidence (source_type, source_id);
