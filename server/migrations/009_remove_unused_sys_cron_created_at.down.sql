ALTER TABLE sys_cron_executions
    ADD COLUMN created_at timestamp with time zone DEFAULT now() NOT NULL;
