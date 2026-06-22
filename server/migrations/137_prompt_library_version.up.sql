CREATE TABLE prompt_library_version (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prompt_id UUID NOT NULL REFERENCES prompt_library_item(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    project_id UUID REFERENCES project(id) ON DELETE SET NULL,
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    prompt_type TEXT NOT NULL DEFAULT '通用',
    content TEXT NOT NULL,
    variables JSONB NOT NULL DEFAULT '[]'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT NOT NULL DEFAULT '手动创建',
    source_candidate_id UUID REFERENCES prompt_evaluation_optimization_candidate(id) ON DELETE SET NULL,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_library_version_version_check CHECK (version > 0),
    CONSTRAINT prompt_library_version_source_check CHECK (source IN ('手动创建', '手动更新', '优化候选发布', '历史回填')),
    CONSTRAINT prompt_library_version_unique UNIQUE (prompt_id, version)
);

CREATE INDEX idx_prompt_library_version_workspace_created
    ON prompt_library_version(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_library_version_prompt_version
    ON prompt_library_version(prompt_id, version DESC);

INSERT INTO prompt_library_version (
    prompt_id, workspace_id, project_id, version, name, description, prompt_type,
    content, variables, tags, source, created_by, created_at
)
SELECT
    id, workspace_id, project_id, version, name, description, prompt_type,
    content, variables, tags, '历史回填', created_by, created_at
FROM prompt_library_item
ON CONFLICT (prompt_id, version) DO NOTHING;
