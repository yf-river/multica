DROP SCHEMA public CASCADE;
CREATE SCHEMA public;

-- 迁移执行器在回滚后仍需删除本次版本记录；保留空的账本表使基线
-- `down` 可以安全完成，下一次 `up` 会重新建立完整结构。
CREATE TABLE schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
