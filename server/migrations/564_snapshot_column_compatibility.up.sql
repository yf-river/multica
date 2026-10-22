-- 快照 d91979d4c 的应用字段在历史路径中缺少对应增量定义。
ALTER TABLE public.issue ADD COLUMN IF NOT EXISTS work_started_at timestamptz;
ALTER TABLE public.issue ADD COLUMN IF NOT EXISTS work_completed_at timestamptz;
ALTER TABLE public.autopilot_trigger ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1;
ALTER TABLE public.project_resource ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1;
ALTER TABLE public."user" ADD COLUMN IF NOT EXISTS account text NOT NULL DEFAULT '';
ALTER TABLE public."user" ADD COLUMN IF NOT EXISTS password_hash text;
