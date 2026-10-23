-- 上游同步设置：提示词改写与每日同步整点。
-- 升级期间旧程序继续服务，且程序恢复不能撤销数据库变更，因此这里只新增一张表：
-- 不改 upstream_sync_config 与 agent 的结构或数据，旧程序不读这张表也能照常跑完
-- 同步、按原键找到执行体；回退旧版本同样不需要 down 任何语句。
CREATE TABLE upstream_sync_settings (
    singleton smallint PRIMARY KEY DEFAULT 1 CHECK (singleton = 1),
    -- 用户改写后的提示词，空串表示继续跟随随代码发布的内置默认。
    instructions text NOT NULL DEFAULT '',
    -- 每天自动同步的北京时间整点。
    schedule_hour integer NOT NULL DEFAULT 9 CHECK (schedule_hour BETWEEN 0 AND 23),
    updated_at timestamptz NOT NULL DEFAULT now()
);
