-- Preparation rows are scoped to a completion round; terminal reports are immutable.
CREATE TABLE IF NOT EXISTS supervision_trace_material (
    id               uuid NOT NULL,
    workspace_id     uuid NOT NULL,
    issue_id         uuid NOT NULL,
    round_id         uuid NOT NULL,
    run_id           uuid NOT NULL,
    runtime          text NOT NULL DEFAULT '',
    manual           boolean NOT NULL DEFAULT false, -- 1. Define ownership at creation while this migration remains unapplied.
    source           text NOT NULL,
    status           text NOT NULL
        CHECK (status IN ('pending','processing','complete','partial','failed','not_applicable','disabled')),
    attempts         integer NOT NULL DEFAULT 0,
    material_version bigint NOT NULL DEFAULT 1,
    cursor           jsonb NOT NULL DEFAULT '{}'::jsonb,
    lease_until      timestamptz,
    report           text NOT NULL DEFAULT '',
    coverage         jsonb NOT NULL DEFAULT '{}'::jsonb,
    gaps             jsonb NOT NULL DEFAULT '[]'::jsonb,
    queried_at       timestamptz,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    error            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
