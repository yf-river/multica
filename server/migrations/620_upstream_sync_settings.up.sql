-- 上游同步的执行体与提示词改为设置页维护：
-- 1. instructions 保存用户改写后的提示词，空值表示继续跟随内置默认；
-- 2. schedule_hour 保存每天自动同步的北京时间整点；
-- 3. 执行体改用固定 system_key，换运行时只改绑定，不再新建第二条 Agent 和自动化。
--    旧 Agent 带上 id 后缀保留，历史任务与运行记录仍指向它们。
ALTER TABLE upstream_sync_config
    ADD COLUMN IF NOT EXISTS instructions text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS schedule_hour integer NOT NULL DEFAULT 9;

ALTER TABLE upstream_sync_config
    ADD CONSTRAINT upstream_sync_config_schedule_hour_range
    CHECK (schedule_hour BETWEEN 0 AND 23);

UPDATE agent a
SET kind = 'user', system_key = 'upstream_sync', updated_at = now()
FROM upstream_sync_config c
WHERE c.agent_id = a.id;

UPDATE agent
SET system_key = 'upstream_sync_orphan:' || id::text, updated_at = now()
WHERE system_key LIKE 'upstream_sync:%';
