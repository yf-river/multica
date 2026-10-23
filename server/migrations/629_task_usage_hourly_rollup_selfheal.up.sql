-- Self-healing rollup for the hourly usage pipeline (GONG-51).
--
-- Two defects made the workspace usage page read zero while `task_usage` kept
-- growing, and both failed silently:
--
--   1. `task_usage_hourly_rollup_state` is expected to always hold the single
--      id=1 row (101 seeds it), but databases created from an older baseline
--      never got that seed. `UPDATE ... RETURNING watermark_at INTO v_from`
--      then yielded NULL, `v_from < v_to` was NULL (never true), and every
--      tick recorded SUCCESS with zero rows — 8k ticks, no data.
--   2. Even with a state row present, catch-up advanced at most one day per
--      tick (`LEAST(now()-5min, v_from + 1 day)`). A watermark stuck in the
--      past (fresh database, restored dump) needed one tick per day of lag to
--      converge — months of waiting.
--
-- The replacement below keeps the one-day window bound (so a single statement
-- never recomputes a multi-week range while holding lock 4246) but loops over
-- consecutive windows inside one tick, capped at 60 days of catch-up per call.
-- A backlog larger than that continues on the next tick instead of stalling.
--
-- The state row is now created on demand, so a missing seed heals itself
-- instead of turning into a permanent no-op. The watermark is seeded from the
-- oldest usage row when it has never been set, so historical usage is not
-- skipped.
CREATE OR REPLACE FUNCTION rollup_task_usage_hourly()
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_lock_ok  BOOLEAN;
    v_from     TIMESTAMPTZ;
    v_to       TIMESTAMPTZ;
    v_rows     BIGINT := 0;
    v_total    BIGINT := 0;
    v_iter     INT := 0;
    v_max_iter CONSTANT INT := 60;
BEGIN
    SELECT pg_try_advisory_lock(4246) INTO v_lock_ok;
    IF NOT v_lock_ok THEN
        RETURN 0;
    END IF;

    BEGIN
        -- Heal a missing seed row: without it every update below matches zero
        -- rows and the worker silently reports success forever. When the row
        -- is missing we insert it with the oldest usage timestamp rather than
        -- the column default (1970-01-01), so history is covered immediately.
        INSERT INTO task_usage_hourly_rollup_state (id, watermark_at)
        SELECT 1, COALESCE(date_trunc('hour', MIN(created_at)), now() - INTERVAL '5 minutes')
          FROM task_usage
        ON CONFLICT (id) DO NOTHING;

        UPDATE task_usage_hourly_rollup_state
           SET last_run_started_at = now(),
               last_error          = NULL
         WHERE id = 1
        RETURNING watermark_at INTO v_from;

        -- Rows created by the 101 seed carry the 1970-01-01 default, which
        -- means "never rolled up yet" rather than a genuine watermark. Without
        -- this reset such a database needs ~20k one-day ticks to reach today.
        -- Nothing legitimate sits at the epoch, so treat it as unset.
        IF v_from IS NULL OR v_from <= TIMESTAMPTZ '1970-01-02 00:00:00+00' THEN
            SELECT COALESCE(date_trunc('hour', MIN(created_at)), now() - INTERVAL '5 minutes')
              INTO v_from
              FROM task_usage;
            UPDATE task_usage_hourly_rollup_state
               SET watermark_at = v_from
             WHERE id = 1;
        END IF;

        -- Catch up in bounded one-day windows. Steady state runs one iteration
        -- (`v_to = now() - 5 min`); a long backlog converges within a few
        -- ticks instead of one day of lag per tick.
        LOOP
            EXIT WHEN v_iter >= v_max_iter;

            v_to := LEAST(now() - INTERVAL '5 minutes', v_from + INTERVAL '1 day');
            EXIT WHEN v_from >= v_to;

            v_rows := rollup_task_usage_hourly_window(v_from, v_to);
            v_total := v_total + v_rows;
            v_iter := v_iter + 1;

            UPDATE task_usage_hourly_rollup_state
               SET watermark_at         = v_to,
                   last_run_finished_at = now(),
                   last_run_rows        = v_rows
             WHERE id = 1;

            v_from := v_to;
        END LOOP;

        IF v_iter = 0 THEN
            UPDATE task_usage_hourly_rollup_state
               SET last_run_finished_at = now(),
                   last_run_rows        = 0
             WHERE id = 1;
        END IF;

        PERFORM pg_advisory_unlock(4246);
    EXCEPTION
        -- `WHEN OTHERS` deliberately does NOT catch query_canceled (statement
        -- timeout, client cancel). A catch-up tick can now run for a while, so
        -- that path must release 4246 explicitly — otherwise a cancelled tick
        -- keeps the lock on its session and every later tick reports "no work"
        -- (MUL-5983).
        WHEN query_canceled THEN
            PERFORM pg_advisory_unlock(4246);
            RAISE;
        WHEN OTHERS THEN
            UPDATE task_usage_hourly_rollup_state
               SET last_error           = SQLERRM,
                   last_run_finished_at = now()
             WHERE id = 1;
            PERFORM pg_advisory_unlock(4246);
            RAISE;
    END;

    -- TTL prune stays outside the advisory lock (same reasoning as 102).
    PERFORM prune_task_usage_hourly_dirty();
    RETURN v_total;
END;
$$;
