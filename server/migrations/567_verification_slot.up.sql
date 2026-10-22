-- 固定验收槽位默认关闭，由管理入口核对环境身份后启用。
CREATE TABLE verification_slot (
    name text NOT NULL CHECK (name IN ('verify-1','verify-2','verify-3')),
    enabled boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','preparing','verifying','draining','quarantined')),
    generation bigint NOT NULL DEFAULT 0,
    workspace_id uuid,
    delivery_id uuid,
    worker_token uuid,
    source_commit text NOT NULL DEFAULT '',
    candidate_id text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'idle' AND delivery_id IS NULL AND worker_token IS NULL)
        OR (state <> 'idle' AND delivery_id IS NOT NULL AND worker_token IS NOT NULL))
);

CREATE TABLE verification_event (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    environment text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 0,
    candidate_id text NOT NULL DEFAULT '',
    stage text NOT NULL,
    detail text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
