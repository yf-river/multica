-- 批次归属：completion 指向完成轮次，manual 是用户显式对未完成任务发起的检查。
-- 每条派发写入一行，用于提交时判断轮次是否仍然开放。
CREATE TABLE supervision_batch_round (
    workspace_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    round_id uuid,
    scope text NOT NULL CHECK (scope IN ('completion','manual')),
    created_at timestamptz NOT NULL DEFAULT now()
);
