-- 质量监督的执行轨迹与用量材料：每次执行（agent_task_queue 的一行）对应一份准备产物。
--
-- 平台在后台把材料准备好再派发监督，不在事务或任务完成接口里等待外部 HTTP。
-- 同一 (workspace_id, run_id, source) 只保留一行，重复准备按该唯一索引幂等；
-- 材料版本变化时覆盖同一行并提升版本号，迟到数据因此成为新版本而不是另起一份。
--
-- 刻意不建外键、不建级联：run/issue/round 归属由准备路径按工作区校验后写入，
-- 工作区拆除与任务删除按既有清理入口显式处理。
CREATE TABLE IF NOT EXISTS supervision_trace_material (
    id               uuid PRIMARY KEY,
    workspace_id     uuid NOT NULL,
    issue_id         uuid NOT NULL,
    round_id         uuid,
    run_id           uuid NOT NULL,
    runtime          text NOT NULL DEFAULT '',
    source           text NOT NULL,
    status           text NOT NULL
        CHECK (status IN ('pending','processing','complete','partial','failed','not_applicable','disabled')),
    attempts         integer NOT NULL DEFAULT 0,
    material_version bigint NOT NULL DEFAULT 0,
    report           text NOT NULL DEFAULT '',
    coverage         jsonb NOT NULL DEFAULT '{}'::jsonb,
    gaps             jsonb NOT NULL DEFAULT '[]'::jsonb,
    queried_at       timestamptz,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    error            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
