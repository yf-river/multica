CREATE TABLE knowledge_import (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    created_by UUID NOT NULL,
    source_url TEXT NOT NULL,
    resource_uri TEXT NOT NULL,
    upstream_task_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'fetching' CHECK (status IN ('fetching', 'indexing', 'completed', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
