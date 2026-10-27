ALTER TABLE life_memory_evidence DROP CONSTRAINT IF EXISTS life_memory_evidence_source_type_check;
ALTER TABLE life_memory_evidence ADD CONSTRAINT life_memory_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY[
        'material', 'chat_message', 'task', 'comment', 'project', 'manual',
        'external', 'memory', 'experiment_round', 'chronicle', 'observer_knowledge'
    ]));

ALTER TABLE life_chronicle_evidence DROP CONSTRAINT IF EXISTS life_chronicle_evidence_source_type_check;
ALTER TABLE life_chronicle_evidence ADD CONSTRAINT life_chronicle_evidence_source_type_check
    CHECK (source_type = ANY (ARRAY[
        'material', 'chat_message', 'task', 'comment', 'project', 'manual',
        'external', 'memory', 'experiment_round', 'chronicle', 'observer_knowledge'
    ]));
