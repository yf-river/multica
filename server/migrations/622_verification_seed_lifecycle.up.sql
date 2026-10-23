-- 验收种子身份跟着槽位走：换任务前据此判断是否需要按当前种子恢复数据。
CREATE TABLE IF NOT EXISTS verification_slot_seed (
    name text PRIMARY KEY REFERENCES verification_slot(name),
    seed_id text NOT NULL,
    seed_sha256 text NOT NULL,
    verified boolean NOT NULL DEFAULT false,
    restored_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 单槽位维护：停接单、恢复种子、恢复接单，其他槽位继续正常验收。
CREATE TABLE IF NOT EXISTS verification_slot_maintenance (
    id uuid PRIMARY KEY,
    name text NOT NULL REFERENCES verification_slot(name),
    action text NOT NULL CHECK (action IN ('maintain','resume')),
    actor text NOT NULL,
    reason text NOT NULL DEFAULT '',
    seed_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS verification_slot_maintenance_name_created ON verification_slot_maintenance(name, created_at DESC);
