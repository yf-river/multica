DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM life_memory_evidence
        WHERE source_type IN ('material', 'chronicle', 'observer_knowledge')
    ) OR EXISTS (
        SELECT 1 FROM life_chronicle_evidence
        WHERE source_type IN ('material', 'chronicle', 'observer_knowledge')
    ) THEN
        RAISE EXCEPTION 'cannot roll back life evidence source types while extended evidence rows exist';
    END IF;
END
$$;

ALTER TABLE life_memory_evidence DROP CONSTRAINT IF EXISTS life_memory_evidence_source_type_check;
ALTER TABLE life_memory_evidence ADD CONSTRAINT life_memory_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY[
        'chat_message', 'task', 'comment', 'project', 'manual', 'external',
        'memory', 'experiment_round'
    ]));

ALTER TABLE life_chronicle_evidence DROP CONSTRAINT IF EXISTS life_chronicle_evidence_source_type_check;
ALTER TABLE life_chronicle_evidence ADD CONSTRAINT life_chronicle_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY[
        'chat_message', 'task', 'comment', 'project', 'manual', 'external',
        'memory', 'experiment_round'
    ]));
