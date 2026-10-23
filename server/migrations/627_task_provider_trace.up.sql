-- 外部链路关联：一次执行（agent_task_queue 的一行）在观测系统里对应的根请求。
--
-- 一次执行可以是 0 条（没发出任何模型请求、或该 backend 不暴露标识），也可以是多条
-- （换新会话重试起了第二个进程、或一次执行内多次根请求），所以这里是子表而不是
-- task_usage / agent_task_queue 上的单列。provider_trace_id 由被启动的子进程自己声明
-- （CodeBuddy 为 stream-json 事件上的 `_requestId`），不是父进程环境变量。
--
-- correlation 区分数据来源的性质：
--   exact            —— 子进程直接给出的标识，可与观测系统一对一核对
--   inferred_window  —— 仅凭"会话 + 时间窗"事后检索得到，属于候选关联，不代表精确归属
-- source 记录取值通道（stream_json / agentlens_query / manual …）。
--
-- 刻意不建外键、不建级联：
--   * task_id 的归属由应用层校验——上报路径先按主键取任务并解析工作区
--     （requireDaemonTaskAccessWithWorkspace），取不到或工作区不匹配直接拒绝，不写库。
--   * 任务行删除时由 DeleteTaskBatch 在同一事务里显式删除本表行，与 task_usage、
--     task_message 等既有清理并列，不依赖数据库级联。
--   * workspace_id 只作查询维度，同样不做外键；工作区拆除按任务批次清理即可覆盖。
--
-- 唯一约束不写在这里：新增索引一律用并发建索引的独立单语句迁移（628），
-- 重复上报的幂等由该唯一索引 + ON CONFLICT 保证。
CREATE TABLE IF NOT EXISTS task_provider_trace (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL,
    task_id            uuid NOT NULL,
    provider           text NOT NULL,
    observation_space  text NOT NULL DEFAULT '',
    session_id         text NOT NULL DEFAULT '',
    provider_trace_id  text NOT NULL,
    correlation        text NOT NULL DEFAULT 'exact',
    source             text NOT NULL DEFAULT 'stream_json',
    started_at         timestamptz,
    finished_at        timestamptz,
    metadata           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz,
    CONSTRAINT task_provider_trace_trace_id_not_empty CHECK (provider_trace_id <> ''),
    CONSTRAINT task_provider_trace_correlation_valid CHECK (correlation IN ('exact', 'inferred_window'))
);
