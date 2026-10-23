-- 质量监督的完成轮次：任务每次完成产生一轮，自动监督只审查该轮截止时间之前的材料。
-- 轮次身份用于去重（扫描、重启、重复状态请求都只对应同一行），也用于重开后停止
-- 旧轮次派发。不建外键，关联与清理由应用层在同一事务内完成。
-- IF NOT EXISTS：迁移编号在 620–626 之后重排为 632–638，int 台账已按旧 stem 记录
-- 并建好同表，按完整 stem 重跑时必须可重复执行，不能靠清理 schema_migrations。
CREATE TABLE IF NOT EXISTS supervision_round (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    sequence integer NOT NULL CHECK (sequence > 0),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
    material_cutoff timestamptz NOT NULL,
    close_reason text NOT NULL DEFAULT '' CHECK (close_reason IN ('','reopened','recompleted')),
    opened_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);
