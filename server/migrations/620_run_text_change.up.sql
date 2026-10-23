-- Platform text an agent edited during a run (skill body, agent instructions,
-- issue or project copy). The diff of a code file comes from the work tree; a
-- prompt lives in the database, so it needs its own record to be reviewable.
-- 1. Only the table here: the release gate accepts a new table but not a plain
--    CREATE INDEX, and a multi-statement file runs as one implicit transaction,
--    where CREATE INDEX CONCURRENTLY is not allowed. The index therefore lives
--    in its own single-statement migration (621) so the old release keeps
--    serving while the new table is added.
CREATE TABLE IF NOT EXISTS public.run_text_change (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    target_kind text NOT NULL,
    target_id text NOT NULL,
    target_name text DEFAULT ''::text NOT NULL,
    field text NOT NULL,
    before_text text DEFAULT ''::text NOT NULL,
    after_text text DEFAULT ''::text NOT NULL,
    added integer DEFAULT 0 NOT NULL,
    deleted integer DEFAULT 0 NOT NULL,
    redacted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
