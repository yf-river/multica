CREATE TABLE prompt_evaluation_evidence_snapshot (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    run_id UUID NOT NULL REFERENCES prompt_evaluation_run(id) ON DELETE CASCADE,
    snapshot_type TEXT NOT NULL DEFAULT '手动归档',
    schema_version TEXT NOT NULL DEFAULT 'multica.prompt_evaluation.evidence_snapshot.v1',
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_evidence_snapshot_type_check
        CHECK (snapshot_type IN ('手动归档', '验收归档', '自动归档'))
);

CREATE INDEX idx_prompt_evaluation_evidence_snapshot_run_created
    ON prompt_evaluation_evidence_snapshot(run_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_evidence_snapshot_workspace_created
    ON prompt_evaluation_evidence_snapshot(workspace_id, created_at DESC);
