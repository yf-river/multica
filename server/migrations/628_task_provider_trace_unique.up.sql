-- 重复上报（daemon 重试、重启重放）必须落到同一行，不能产生重复关联。
-- 单语句、并发建索引：新增索引一律走这条形式，避免在建索引期间锁表。
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS task_provider_trace_task_trace_unique
    ON task_provider_trace (task_id, provider, provider_trace_id);
