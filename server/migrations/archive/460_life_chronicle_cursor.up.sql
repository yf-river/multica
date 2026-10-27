CREATE TABLE life_chronicle_cursor (
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    period_kind text NOT NULL,
    next_period_start timestamptz NOT NULL,
    last_processed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id, period_kind),
    CONSTRAINT life_chronicle_cursor_kind_check CHECK (period_kind IN ('day', 'week', 'month', 'year'))
);
