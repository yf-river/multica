CREATE TABLE prompt_library_item (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    project_id UUID REFERENCES project(id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    prompt_type TEXT NOT NULL DEFAULT '通用',
    content TEXT NOT NULL,
    variables JSONB NOT NULL DEFAULT '[]'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT '启用',
    version INTEGER NOT NULL DEFAULT 1,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_library_item_status_check CHECK (status IN ('启用', '归档')),
    CONSTRAINT prompt_library_item_version_check CHECK (version > 0),
    CONSTRAINT prompt_library_item_name_unique UNIQUE (workspace_id, name)
);

CREATE INDEX prompt_library_item_workspace_project_idx ON prompt_library_item(workspace_id, project_id);
CREATE INDEX prompt_library_item_workspace_type_idx ON prompt_library_item(workspace_id, prompt_type);
