-- Restore the rollup function as it was before 634 (one window per tick, no self-heal).
CREATE OR REPLACE FUNCTION rollup_task_usage_hourly()
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_lock_ok BOOLEAN;
    v_from    TIMESTAMPTZ;
    v_to      TIMESTAMPTZ;
    v_rows    BIGINT := 0;
BEGIN
    SELECT pg_try_advisory_lock(4246) INTO v_lock_ok;
    IF NOT v_lock_ok THEN
        RETURN 0;
    END IF;

    BEGIN
        UPDATE task_usage_hourly_rollup_state
           SET last_run_started_at = now(),
               last_error          = NULL
         WHERE id = 1
        RETURNING watermark_at INTO v_from;

        -- Cap each tick at a one-day window. In steady state v_from is
        -- recent, so LEAST picks `now() - 5 min` and nothing changes. But
        -- if the worker was paused (incident, migration freeze) the
        -- watermark can fall far behind; without the cap a single catch-up
        -- tick would recompute a multi-week window in one statement while
        -- holding lock 4246, blocking every other tick. Capped, catch-up
        -- advances in bounded one-day steps over successive ticks.
        v_to := LEAST(now() - INTERVAL '5 minutes', v_from + INTERVAL '1 day');

        IF v_from < v_to THEN
            v_rows := rollup_task_usage_hourly_window(v_from, v_to);

            UPDATE task_usage_hourly_rollup_state
               SET watermark_at         = v_to,
                   last_run_finished_at = now(),
                   last_run_rows        = v_rows
             WHERE id = 1;
        ELSE
            UPDATE task_usage_hourly_rollup_state
               SET last_run_finished_at = now(),
                   last_run_rows        = 0
             WHERE id = 1;
        END IF;

        PERFORM pg_advisory_unlock(4246);
    EXCEPTION WHEN OTHERS THEN
        UPDATE task_usage_hourly_rollup_state
           SET last_error           = SQLERRM,
               last_run_finished_at = now()
         WHERE id = 1;
        PERFORM pg_advisory_unlock(4246);
        RAISE;
    END;

    -- TTL prune. Runs AFTER the advisory lock is released: on a large
    -- stale backlog the prune can be slow, and holding lock 4246 through
    -- it would serialise every concurrent cron tick. It is a plain
    -- bounded DELETE — idempotent and safe to run unlocked.
    PERFORM prune_task_usage_hourly_dirty();
    RETURN v_rows;
END;
$$;
