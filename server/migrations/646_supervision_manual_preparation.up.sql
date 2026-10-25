CREATE TABLE IF NOT EXISTS supervision_manual_preparation (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    material_cutoff timestamptz NOT NULL DEFAULT now(),
    preparation_http_requests integer NOT NULL DEFAULT 0,
    preparation_started_at timestamptz
);
ALTER TABLE supervision_trace_material ADD COLUMN IF NOT EXISTS manual boolean NOT NULL DEFAULT false;
