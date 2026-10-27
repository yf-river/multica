CREATE TABLE task_interaction (
    task_id UUID NOT NULL,
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID,
    provider TEXT NOT NULL CHECK (provider IN ('codex', 'codebuddy', 'dsh')),
    kind TEXT NOT NULL CHECK (kind IN ('question', 'approval', 'plan')),
    request JSONB NOT NULL,
    response JSONB,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'responded', 'expired')),
    author_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '60 seconds',
    responded_at TIMESTAMPTZ,
    delivery_mode TEXT NOT NULL DEFAULT 'live' CHECK (delivery_mode IN ('live', 'suspended')),
    followup_task_id UUID
);
