ALTER TABLE task_usage_hourly_rollup_state
    ADD COLUMN last_run_started_at timestamp with time zone,
    ADD COLUMN last_run_rows bigint DEFAULT 0 NOT NULL;

CREATE OR REPLACE FUNCTION public.rollup_task_usage_hourly() RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_lock_ok BOOLEAN;
    v_from    TIMESTAMPTZ;
    v_to      TIMESTAMPTZ;
    v_upper   TIMESTAMPTZ;
    v_first_pending TIMESTAMPTZ;
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
        v_upper := now() - INTERVAL '5 minutes';
        IF v_from < v_upper THEN
            SELECT MIN(candidate_at)
              INTO v_first_pending
              FROM (
                    SELECT tu.updated_at AS candidate_at
                      FROM task_usage tu
                      JOIN agent_task_queue atq ON atq.id = tu.task_id
                     WHERE tu.updated_at >= v_from
                       AND tu.updated_at <  v_upper
                    UNION ALL
                    SELECT GREATEST(enqueued_at, v_from) AS candidate_at
                      FROM task_usage_hourly_dirty
                     WHERE enqueued_at < v_upper
              ) pending;
            IF v_first_pending IS NULL THEN
                v_to := v_upper;
            ELSE
                IF v_first_pending > v_from + INTERVAL '1 day' THEN
                    v_from := v_first_pending;
                END IF;
                v_to := LEAST(v_upper, v_from + INTERVAL '1 day');
            END IF;
        ELSE
            v_to := v_from;
        END IF;
        IF v_from < v_to THEN
            v_rows := rollup_task_usage_hourly_window(v_from, v_to);
            UPDATE task_usage_hourly_rollup_state
               SET watermark_at         = v_to,
                   last_run_finished_at = now(),
                   last_run_rows        = v_rows
             WHERE id = 1;
        ELSE
            UPDATE task_usage_hourly_rollup_state
               SET watermark_at         = GREATEST(watermark_at, v_to),
                   last_run_finished_at = now(),
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
    PERFORM prune_task_usage_hourly_dirty();
    RETURN v_rows;
END;
$$;
