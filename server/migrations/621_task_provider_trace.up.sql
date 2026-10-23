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
-- 唯一约束内联在建表语句里：新表没有并发读写，建表时即可完成索引构建；该唯一约束
-- (task_id, provider, provider_trace_id) 同时覆盖"按任务查链路"的前缀查询，因此不再
-- 单独建二级索引。发布器的自动迁移闸门只接受新增表与并发索引，这样本迁移保持单条
-- 建表语句即可通过。
CREATE TABLE IF NOT EXISTS task_provider_trace (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL,
    task_id            uuid NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
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
    CONSTRAINT task_provider_trace_correlation_valid CHECK (correlation IN ('exact', 'inferred_window')),
    -- 重复上报（daemon 重试、重启后重放）必须落到同一行，不能产生重复关联。
    CONSTRAINT task_provider_trace_task_trace_unique UNIQUE (task_id, provider, provider_trace_id)
);
