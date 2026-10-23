-- 选择性组合发布：一个批次是一份候选、一次构建、一次生产切换，成员各自保留授权与证据。
-- 成员与批次的关联由应用维护：删除交付时同事务清理成员行，不依赖级联。
CREATE TABLE IF NOT EXISTS delivery_batch (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('collecting','frozen','preparing','validating','publishing','published','needs_changes','cancelled')),
    window_seconds integer NOT NULL,
    collect_until timestamptz NOT NULL,
    base_commit text NOT NULL DEFAULT '',
    candidate_id text NOT NULL DEFAULT '',
    candidate_commit text NOT NULL DEFAULT '',
    worker_token uuid,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS delivery_batch_member (
    batch_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    source_commit text NOT NULL,
    result text NOT NULL DEFAULT 'pending' CHECK (result IN ('pending','published','failed','withdrawn')),
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (batch_id, delivery_id)
);
