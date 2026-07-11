-- Current schema baseline generated from the old migration chain.
-- Development-phase squash: no historical database upgrade compatibility is preserved.

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

CREATE FUNCTION public.enqueue_task_usage_hourly_dirty_for_atq() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.runtime_id IS DISTINCT FROM NEW.runtime_id
           OR OLD.issue_id IS DISTINCT FROM NEW.issue_id THEN
            -- OLD side. NULL runtime_id rows are not aggregated (no
            -- runtime → no bucket); skip those.
            IF OLD.runtime_id IS NOT NULL THEN
                INSERT INTO task_usage_hourly_dirty (
                    bucket_hour, workspace_id, runtime_id, agent_id,
                    project_id, provider, model
                )
                SELECT DISTINCT
                    task_usage_hour_bucket(tu.created_at),
                    a.workspace_id,
                    OLD.runtime_id,
                    OLD.agent_id,
                    i_old.project_id,
                    tu.provider,
                    tu.model
                  FROM task_usage tu
                  JOIN agent a ON a.id = OLD.agent_id
                  LEFT JOIN issue i_old ON i_old.id = OLD.issue_id
                 WHERE tu.task_id = OLD.id
                ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
                    SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
            END IF;

            IF NEW.runtime_id IS NOT NULL THEN
                INSERT INTO task_usage_hourly_dirty (
                    bucket_hour, workspace_id, runtime_id, agent_id,
                    project_id, provider, model
                )
                SELECT DISTINCT
                    task_usage_hour_bucket(tu.created_at),
                    a.workspace_id,
                    NEW.runtime_id,
                    NEW.agent_id,
                    i_new.project_id,
                    tu.provider,
                    tu.model
                  FROM task_usage tu
                  JOIN agent a ON a.id = NEW.agent_id
                  LEFT JOIN issue i_new ON i_new.id = NEW.issue_id
                 WHERE tu.task_id = NEW.id
                ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
                    SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
            END IF;
        END IF;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        IF OLD.runtime_id IS NOT NULL THEN
            INSERT INTO task_usage_hourly_dirty (
                bucket_hour, workspace_id, runtime_id, agent_id,
                project_id, provider, model
            )
            SELECT DISTINCT
                task_usage_hour_bucket(tu.created_at),
                a.workspace_id,
                OLD.runtime_id,
                OLD.agent_id,
                i.project_id,
                tu.provider,
                tu.model
              FROM task_usage tu
              JOIN agent a ON a.id = OLD.agent_id
              LEFT JOIN issue i ON i.id = OLD.issue_id
             WHERE tu.task_id = OLD.id
            ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
                SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
        END IF;
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION public.enqueue_task_usage_hourly_dirty_for_issue_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO task_usage_hourly_dirty (
        bucket_hour, workspace_id, runtime_id, agent_id,
        project_id, provider, model
    )
    SELECT DISTINCT
        task_usage_hour_bucket(tu.created_at),
        OLD.workspace_id,
        atq.runtime_id,
        atq.agent_id,
        OLD.project_id,
        tu.provider,
        tu.model
      FROM agent_task_queue atq
      JOIN task_usage tu ON tu.task_id = atq.id
     WHERE atq.issue_id = OLD.id
       AND atq.runtime_id IS NOT NULL
    ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
        SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
    RETURN OLD;
END;
$$;

CREATE FUNCTION public.enqueue_task_usage_hourly_dirty_for_issue_project() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id THEN
        -- OLD project buckets.
        INSERT INTO task_usage_hourly_dirty (
            bucket_hour, workspace_id, runtime_id, agent_id,
            project_id, provider, model
        )
        SELECT DISTINCT
            task_usage_hour_bucket(tu.created_at),
            NEW.workspace_id,
            atq.runtime_id,
            atq.agent_id,
            OLD.project_id,
            tu.provider,
            tu.model
          FROM agent_task_queue atq
          JOIN task_usage tu ON tu.task_id = atq.id
         WHERE atq.issue_id = NEW.id
           AND atq.runtime_id IS NOT NULL
        ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
            SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);

        -- NEW project buckets.
        INSERT INTO task_usage_hourly_dirty (
            bucket_hour, workspace_id, runtime_id, agent_id,
            project_id, provider, model
        )
        SELECT DISTINCT
            task_usage_hour_bucket(tu.created_at),
            NEW.workspace_id,
            atq.runtime_id,
            atq.agent_id,
            NEW.project_id,
            tu.provider,
            tu.model
          FROM agent_task_queue atq
          JOIN task_usage tu ON tu.task_id = atq.id
         WHERE atq.issue_id = NEW.id
           AND atq.runtime_id IS NOT NULL
        ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
            SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION public.enqueue_task_usage_hourly_dirty_for_tu() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO task_usage_hourly_dirty (
        bucket_hour, workspace_id, runtime_id, agent_id,
        project_id, provider, model
    )
    SELECT
        task_usage_hour_bucket(OLD.created_at),
        a.workspace_id,
        atq.runtime_id,
        atq.agent_id,
        i.project_id,
        OLD.provider,
        OLD.model
      FROM agent_task_queue atq
      JOIN agent a ON a.id = atq.agent_id
      LEFT JOIN issue i ON i.id = atq.issue_id
     WHERE atq.id = OLD.task_id
       AND atq.runtime_id IS NOT NULL
    ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_dirty_key DO UPDATE
        SET enqueued_at = GREATEST(task_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
    RETURN OLD;
END;
$$;

CREATE FUNCTION public.prune_task_usage_hourly_dirty(p_retention interval DEFAULT '7 days'::interval) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    DELETE FROM task_usage_hourly_dirty
     WHERE enqueued_at < now() - p_retention;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    RETURN v_rows;
END;
$$;

CREATE FUNCTION public.rollup_task_usage_hourly() RETURNS bigint
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

        -- Fresh databases used to start at 1970-01-01 and advance one
        -- empty day every tick. Fast-forward over empty history: if no
        -- raw usage or dirty rollup key exists before the safe upper
        -- bound, mark the empty interval complete; otherwise jump to the
        -- first real pending timestamp and keep the one-day cap below.
        IF v_from < v_upper THEN
            SELECT MIN(candidate_at)
              INTO v_first_pending
              FROM (
                    SELECT tu.updated_at AS candidate_at
                      FROM task_usage tu
                      JOIN agent_task_queue atq ON atq.id = tu.task_id
                     WHERE atq.runtime_id IS NOT NULL
                       AND tu.updated_at >= v_from
                       AND tu.updated_at <  v_upper
                    UNION ALL
                    SELECT tu.created_at AS candidate_at
                      FROM task_usage tu
                      JOIN agent_task_queue atq ON atq.id = tu.task_id
                     WHERE atq.runtime_id IS NOT NULL
                       AND tu.updated_at IS NULL
                       AND tu.created_at >= v_from
                       AND tu.created_at <  v_upper
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
                -- Cap each tick at a one-day window. In steady state v_from is
                -- recent, so LEAST picks `now() - 5 min` and nothing changes. But
                -- if the worker was paused (incident, migration freeze) the
                -- watermark can fall far behind; without the cap a single catch-up
                -- tick would recompute a multi-week window in one statement while
                -- holding lock 4246, blocking every other tick. Capped, catch-up
                -- advances in bounded one-day steps over successive ticks.
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

    -- TTL prune. Runs AFTER the advisory lock is released: on a large
    -- stale backlog the prune can be slow, and holding lock 4246 through
    -- it would serialise every concurrent cron tick. It is a plain
    -- bounded DELETE — idempotent and safe to run unlocked.
    PERFORM prune_task_usage_hourly_dirty();
    RETURN v_rows;
END;
$$;

CREATE FUNCTION public.rollup_task_usage_hourly_window(p_from timestamp with time zone, p_to timestamp with time zone) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    IF p_from >= p_to THEN
        RETURN 0;
    END IF;

    WITH
    dirty_from_updates AS (
        SELECT DISTINCT
            task_usage_hour_bucket(tu.created_at) AS bucket_hour,
            a.workspace_id                        AS workspace_id,
            atq.runtime_id                        AS runtime_id,
            atq.agent_id                          AS agent_id,
            i.project_id                          AS project_id,
            tu.provider                           AS provider,
            tu.model                              AS model
          FROM task_usage tu
          JOIN agent_task_queue atq ON atq.id      = tu.task_id
          JOIN agent            a   ON a.id        = atq.agent_id
          LEFT JOIN issue       i   ON i.id        = atq.issue_id
         WHERE atq.runtime_id IS NOT NULL
           AND (
                (tu.updated_at >= p_from AND tu.updated_at < p_to)
                -- Legacy updated_at-NULL rows; partial index from 078.
                OR (tu.updated_at IS NULL
                    AND tu.created_at >= p_from
                    AND tu.created_at <  p_to)
           )
    ),
    dirty_from_queue AS (
        SELECT bucket_hour, workspace_id, runtime_id, agent_id,
               project_id, provider, model
          FROM task_usage_hourly_dirty
         WHERE enqueued_at < p_to
    ),
    dirty_keys AS (
        SELECT * FROM dirty_from_updates
        UNION
        SELECT * FROM dirty_from_queue
    ),
    recomputed AS (
        SELECT
            dk.bucket_hour,
            dk.workspace_id,
            dk.runtime_id,
            dk.agent_id,
            dk.project_id,
            dk.provider,
            dk.model,
            SUM(tu.input_tokens)::bigint       AS input_tokens,
            SUM(tu.output_tokens)::bigint      AS output_tokens,
            SUM(tu.cache_read_tokens)::bigint  AS cache_read_tokens,
            SUM(tu.cache_write_tokens)::bigint AS cache_write_tokens,
            COUNT(DISTINCT tu.task_id)::bigint AS task_count,
            COUNT(*)::bigint                   AS event_count
          FROM dirty_keys dk
          JOIN agent_task_queue atq ON atq.runtime_id  = dk.runtime_id
                                    AND atq.agent_id    = dk.agent_id
          JOIN agent            a   ON a.id            = atq.agent_id
                                    AND a.workspace_id = dk.workspace_id
          LEFT JOIN issue       i   ON i.id            = atq.issue_id
          JOIN task_usage       tu  ON tu.task_id      = atq.id
                                    AND tu.provider    = dk.provider
                                    AND tu.model       = dk.model
                                    AND task_usage_hour_bucket(tu.created_at) = dk.bucket_hour
         WHERE (i.project_id IS NOT DISTINCT FROM dk.project_id)
         GROUP BY 1, 2, 3, 4, 5, 6, 7
    ),
    upserted AS (
        INSERT INTO task_usage_hourly AS d (
            bucket_hour, workspace_id, runtime_id, agent_id,
            project_id, provider, model,
            input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
            task_count, event_count
        )
        SELECT
            bucket_hour, workspace_id, runtime_id, agent_id,
            project_id, provider, model,
            input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
            task_count, event_count
          FROM recomputed
        ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_key DO UPDATE
            SET input_tokens       = EXCLUDED.input_tokens,
                output_tokens      = EXCLUDED.output_tokens,
                cache_read_tokens  = EXCLUDED.cache_read_tokens,
                cache_write_tokens = EXCLUDED.cache_write_tokens,
                task_count         = EXCLUDED.task_count,
                event_count        = EXCLUDED.event_count,
                updated_at         = now()
        RETURNING 1
    ),
    deleted_empty AS (
        DELETE FROM task_usage_hourly d
         USING dirty_keys dk
         WHERE d.bucket_hour  = dk.bucket_hour
           AND d.workspace_id = dk.workspace_id
           AND d.runtime_id   = dk.runtime_id
           AND d.agent_id     = dk.agent_id
           AND d.project_id IS NOT DISTINCT FROM dk.project_id
           AND d.provider     = dk.provider
           AND d.model        = dk.model
           AND NOT EXISTS (
               SELECT 1 FROM recomputed r
                WHERE r.bucket_hour  = dk.bucket_hour
                  AND r.workspace_id = dk.workspace_id
                  AND r.runtime_id   = dk.runtime_id
                  AND r.agent_id     = dk.agent_id
                  AND r.project_id IS NOT DISTINCT FROM dk.project_id
                  AND r.provider     = dk.provider
                  AND r.model        = dk.model
           )
        RETURNING 1
    )
    SELECT (SELECT COUNT(*) FROM upserted) + (SELECT COUNT(*) FROM deleted_empty)
      INTO v_rows;

    DELETE FROM task_usage_hourly_dirty WHERE enqueued_at < p_to;

    RETURN v_rows;
END;
$$;

CREATE FUNCTION public.task_usage_hour_bucket(ts timestamp with time zone) RETURNS timestamp with time zone
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT (date_trunc('hour', ts AT TIME ZONE 'UTC')) AT TIME ZONE 'UTC';
$$;

CREATE FUNCTION public.task_usage_hourly_rollup_lag_seconds() RETURNS double precision
    LANGUAGE sql STABLE
    AS $$
    SELECT EXTRACT(EPOCH FROM (now() - last_run_finished_at))
      FROM task_usage_hourly_rollup_state
     WHERE id = 1;
$$;

SET default_tablespace = '';

SET default_table_access_method = heap;

CREATE TABLE public.activity_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid,
    actor_type text,
    actor_id uuid,
    action text NOT NULL,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT activity_log_actor_type_check CHECK ((actor_type = ANY (ARRAY['member'::text, 'agent'::text, 'system'::text])))
);

CREATE TABLE public.agent (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    avatar_url text,
    runtime_mode text NOT NULL,
    runtime_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    scope text DEFAULT 'personal'::text NOT NULL,
    status text DEFAULT 'offline'::text NOT NULL,
    max_concurrent_tasks integer DEFAULT 20 NOT NULL,
    owner_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    runtime_id uuid NOT NULL,
    instructions text DEFAULT ''::text NOT NULL,
    archived_at timestamp with time zone,
    archived_by uuid,
    custom_env jsonb DEFAULT '{}'::jsonb NOT NULL,
    custom_args jsonb DEFAULT '[]'::jsonb NOT NULL,
    mcp_config jsonb,
    model text,
    thinking_level text,
    CONSTRAINT agent_description_length CHECK ((char_length(description) <= 255)),
    CONSTRAINT agent_runtime_mode_check CHECK ((runtime_mode = ANY (ARRAY['local'::text, 'cloud'::text]))),
    CONSTRAINT agent_scope_check CHECK ((scope = ANY (ARRAY['personal'::text, 'workspace'::text]))),
    CONSTRAINT agent_status_check CHECK ((status = ANY (ARRAY['idle'::text, 'working'::text, 'blocked'::text, 'error'::text, 'offline'::text])))
);

CREATE TABLE public.agent_runtime (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    daemon_id text,
    name text NOT NULL,
    runtime_mode text NOT NULL,
    provider text NOT NULL,
    status text DEFAULT 'offline'::text NOT NULL,
    device_info text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_seen_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    owner_id uuid,
    scope text DEFAULT 'workspace'::text NOT NULL,
    profile_id uuid,
    CONSTRAINT agent_runtime_runtime_mode_check CHECK ((runtime_mode = ANY (ARRAY['local'::text, 'cloud'::text]))),
    CONSTRAINT agent_runtime_scope_check CHECK ((scope = ANY (ARRAY['personal'::text, 'workspace'::text]))),
    CONSTRAINT agent_runtime_status_check CHECK ((status = ANY (ARRAY['online'::text, 'offline'::text])))
);

CREATE TABLE public.agent_skill (
    agent_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.agent_task_queue (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    issue_id uuid,
    status text DEFAULT 'queued'::text NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    dispatched_at timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    result jsonb,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    context jsonb,
    runtime_id uuid NOT NULL,
    session_id text,
    work_dir text,
    trigger_comment_id uuid,
    chat_session_id uuid,
    autopilot_run_id uuid,
    attempt integer DEFAULT 1 NOT NULL,
    max_attempts integer DEFAULT 2 NOT NULL,
    parent_task_id uuid,
    failure_reason text,
    trigger_summary text,
    force_fresh_session boolean DEFAULT false NOT NULL,
    is_leader_task boolean DEFAULT false NOT NULL,
    wait_reason text,
    initiator_user_id uuid,
    CONSTRAINT agent_task_queue_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'dispatched'::text, 'running'::text, 'waiting_local_directory'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

CREATE TABLE public.attachment (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid,
    comment_id uuid,
    uploader_type text NOT NULL,
    uploader_id uuid NOT NULL,
    filename text NOT NULL,
    url text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    chat_session_id uuid,
    chat_message_id uuid,
    CONSTRAINT attachment_uploader_type_check CHECK ((uploader_type = ANY (ARRAY['member'::text, 'agent'::text])))
);

CREATE TABLE public.autopilot (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    title text NOT NULL,
    description text,
    assignee_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    execution_mode text DEFAULT 'create_issue'::text NOT NULL,
    issue_title_template text,
    created_by_type text NOT NULL,
    created_by_id uuid NOT NULL,
    last_run_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    assignee_type text DEFAULT 'agent'::text NOT NULL,
    project_id uuid,
    CONSTRAINT autopilot_assignee_type_check CHECK ((assignee_type = ANY (ARRAY['agent'::text, 'squad'::text]))),
    CONSTRAINT autopilot_created_by_type_check CHECK ((created_by_type = ANY (ARRAY['member'::text, 'agent'::text]))),
    CONSTRAINT autopilot_execution_mode_check CHECK ((execution_mode = ANY (ARRAY['create_issue'::text, 'run_only'::text]))),
    CONSTRAINT autopilot_status_check CHECK ((status = ANY (ARRAY['active'::text, 'paused'::text, 'archived'::text])))
);

CREATE TABLE public.autopilot_run (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    autopilot_id uuid NOT NULL,
    trigger_id uuid,
    source text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    issue_id uuid,
    task_id uuid,
    triggered_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    failure_reason text,
    trigger_payload jsonb,
    result jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    squad_id uuid,
    CONSTRAINT autopilot_run_source_check CHECK ((source = ANY (ARRAY['schedule'::text, 'manual'::text, 'webhook'::text]))),
    CONSTRAINT autopilot_run_status_check CHECK ((status = ANY (ARRAY['issue_created'::text, 'running'::text, 'completed'::text, 'failed'::text, 'skipped'::text])))
);

CREATE TABLE public.autopilot_subscriber (
    autopilot_id uuid NOT NULL,
    user_type text NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT autopilot_subscriber_user_type_check CHECK ((user_type = 'member'::text))
);

CREATE TABLE public.autopilot_trigger (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    autopilot_id uuid NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    cron_expression text,
    timezone text DEFAULT 'UTC'::text,
    next_run_at timestamp with time zone,
    webhook_token text,
    label text,
    last_fired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    provider text DEFAULT 'generic'::text NOT NULL,
    signing_secret text,
    event_filters jsonb,
    CONSTRAINT autopilot_trigger_kind_check CHECK ((kind = ANY (ARRAY['schedule'::text, 'webhook'::text]))),
    CONSTRAINT autopilot_trigger_provider_check CHECK ((provider = ANY (ARRAY['generic'::text, 'github'::text])))
);

CREATE TABLE public.chat_message (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    chat_session_id uuid NOT NULL,
    role text NOT NULL,
    content text NOT NULL,
    task_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    failure_reason text,
    elapsed_ms bigint,
    CONSTRAINT chat_message_role_check CHECK ((role = ANY (ARRAY['user'::text, 'assistant'::text])))
);

CREATE TABLE public.chat_session (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    creator_id uuid NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    session_id text,
    work_dir text,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    unread_since timestamp with time zone,
    runtime_id uuid,
    CONSTRAINT chat_session_status_check CHECK ((status = ANY (ARRAY['active'::text, 'archived'::text])))
);

CREATE TABLE public.comment (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    issue_id uuid NOT NULL,
    author_type text NOT NULL,
    author_id uuid NOT NULL,
    content text NOT NULL,
    type text DEFAULT 'comment'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    parent_id uuid,
    workspace_id uuid NOT NULL,
    resolved_at timestamp with time zone,
    resolved_by_type text,
    resolved_by_id uuid,
    source_task_id uuid,
    CONSTRAINT comment_author_type_check CHECK ((author_type = ANY (ARRAY['member'::text, 'agent'::text, 'system'::text]))),
    CONSTRAINT comment_resolved_consistency CHECK ((((resolved_at IS NULL) AND (resolved_by_type IS NULL) AND (resolved_by_id IS NULL)) OR ((resolved_at IS NOT NULL) AND (resolved_by_type IS NOT NULL) AND (resolved_by_id IS NOT NULL)))),
    CONSTRAINT comment_type_check CHECK ((type = ANY (ARRAY['comment'::text, 'status_change'::text, 'progress_update'::text, 'system'::text])))
);

CREATE TABLE public.comment_reaction (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    comment_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    emoji text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT comment_reaction_actor_type_check CHECK ((actor_type = ANY (ARRAY['member'::text, 'agent'::text])))
);

CREATE TABLE public.daemon_connection (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    daemon_id text NOT NULL,
    status text DEFAULT 'disconnected'::text NOT NULL,
    last_heartbeat_at timestamp with time zone,
    runtime_info jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT daemon_connection_status_check CHECK ((status = ANY (ARRAY['connected'::text, 'disconnected'::text])))
);

CREATE TABLE public.daemon_token (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    token_hash text NOT NULL,
    workspace_id uuid NOT NULL,
    daemon_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.external_credential_profile (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    name text NOT NULL,
    secret_ref text DEFAULT ''::text NOT NULL,
    encrypted_secret bytea,
    secret_hint text DEFAULT ''::text NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'unverified'::text NOT NULL,
    last_verified_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT external_credential_profile_check CHECK (((secret_ref <> ''::text) OR (encrypted_secret IS NOT NULL))),
    CONSTRAINT external_credential_profile_provider_check CHECK ((provider = ANY (ARRAY['tapd'::text, 'gongfeng'::text]))),
    CONSTRAINT external_credential_profile_status_check CHECK ((status = ANY (ARRAY['unverified'::text, 'verified'::text, 'failed'::text, 'disabled'::text])))
);

CREATE TABLE public.feedback (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    workspace_id uuid,
    message text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.github_installation (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    installation_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text DEFAULT 'User'::text NOT NULL,
    account_avatar_url text,
    connected_by_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_installation_account_type_check CHECK ((account_type = ANY (ARRAY['User'::text, 'Organization'::text])))
);

CREATE TABLE public.github_pending_check_suite (
    workspace_id uuid NOT NULL,
    installation_id bigint NOT NULL,
    repo_owner text NOT NULL,
    repo_name text NOT NULL,
    pr_number integer NOT NULL,
    suite_id bigint NOT NULL,
    head_sha text NOT NULL,
    app_id bigint NOT NULL,
    conclusion text,
    status text NOT NULL,
    suite_updated_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.github_pending_installation (
    installation_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text DEFAULT 'User'::text NOT NULL,
    account_avatar_url text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT github_pending_installation_account_login_check CHECK ((account_login <> ''::text)),
    CONSTRAINT github_pending_installation_account_type_check CHECK ((account_type = ANY (ARRAY['User'::text, 'Organization'::text])))
);

CREATE TABLE public.github_pull_request (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    installation_id bigint NOT NULL,
    repo_owner text NOT NULL,
    repo_name text NOT NULL,
    pr_number integer NOT NULL,
    title text NOT NULL,
    state text NOT NULL,
    html_url text NOT NULL,
    branch text,
    author_login text,
    author_avatar_url text,
    merged_at timestamp with time zone,
    closed_at timestamp with time zone,
    pr_created_at timestamp with time zone NOT NULL,
    pr_updated_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    head_sha text DEFAULT ''::text NOT NULL,
    mergeable_state text,
    additions integer DEFAULT 0 NOT NULL,
    deletions integer DEFAULT 0 NOT NULL,
    changed_files integer DEFAULT 0 NOT NULL,
    CONSTRAINT github_pull_request_state_check CHECK ((state = ANY (ARRAY['open'::text, 'closed'::text, 'merged'::text, 'draft'::text])))
);

CREATE TABLE public.github_pull_request_check_suite (
    pr_id uuid NOT NULL,
    suite_id bigint NOT NULL,
    head_sha text NOT NULL,
    app_id bigint NOT NULL,
    conclusion text,
    status text NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE TABLE public.inbox_item (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    recipient_type text NOT NULL,
    recipient_id uuid NOT NULL,
    type text NOT NULL,
    severity text DEFAULT 'info'::text NOT NULL,
    issue_id uuid,
    title text NOT NULL,
    body text,
    read boolean DEFAULT false NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_type text,
    actor_id uuid,
    details jsonb DEFAULT '{}'::jsonb,
    CONSTRAINT inbox_item_recipient_type_check CHECK ((recipient_type = ANY (ARRAY['member'::text, 'agent'::text]))),
    CONSTRAINT inbox_item_severity_check CHECK ((severity = ANY (ARRAY['action_required'::text, 'attention'::text, 'info'::text])))
);

CREATE TABLE public.issue (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    title text NOT NULL,
    description text,
    status text DEFAULT 'backlog'::text NOT NULL,
    priority text DEFAULT 'none'::text NOT NULL,
    assignee_type text,
    assignee_id uuid,
    creator_type text NOT NULL,
    creator_id uuid NOT NULL,
    parent_issue_id uuid,
    acceptance_criteria jsonb DEFAULT '[]'::jsonb NOT NULL,
    context_refs jsonb DEFAULT '[]'::jsonb NOT NULL,
    "position" double precision DEFAULT 0 NOT NULL,
    due_date date,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    number integer DEFAULT 0 NOT NULL,
    project_id uuid,
    origin_type text,
    origin_id uuid,
    first_executed_at timestamp with time zone,
    start_date date,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    scope text DEFAULT 'workspace'::text NOT NULL,
    owner_id uuid,
    work_started_at timestamp with time zone,
    work_completed_at timestamp with time zone,
    CONSTRAINT issue_assignee_type_check CHECK ((assignee_type = ANY (ARRAY['member'::text, 'agent'::text, 'squad'::text]))),
    CONSTRAINT issue_creator_type_check CHECK ((creator_type = ANY (ARRAY['member'::text, 'agent'::text]))),
    CONSTRAINT issue_metadata_is_object CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT issue_metadata_size_limit CHECK ((pg_column_size(metadata) <= 8192)),
    CONSTRAINT issue_origin_type_check CHECK ((origin_type = ANY (ARRAY['autopilot'::text, 'quick_create'::text, 'lark_chat'::text]))),
    CONSTRAINT issue_priority_check CHECK ((priority = ANY (ARRAY['urgent'::text, 'high'::text, 'medium'::text, 'low'::text, 'none'::text]))),
    CONSTRAINT issue_scope_check CHECK ((scope = ANY (ARRAY['personal'::text, 'workspace'::text]))),
    CONSTRAINT issue_status_check CHECK ((status = ANY (ARRAY['backlog'::text, 'todo'::text, 'in_progress'::text, 'in_review'::text, 'done'::text, 'blocked'::text, 'cancelled'::text])))
);

CREATE TABLE public.issue_dependency (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    issue_id uuid NOT NULL,
    depends_on_issue_id uuid NOT NULL,
    type text NOT NULL,
    CONSTRAINT issue_dependency_type_check CHECK ((type = ANY (ARRAY['blocks'::text, 'blocked_by'::text, 'related'::text])))
);

CREATE TABLE public.issue_label (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    color text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.issue_pull_request (
    issue_id uuid NOT NULL,
    pull_request_id uuid NOT NULL,
    linked_by_type text,
    linked_by_id uuid,
    linked_at timestamp with time zone DEFAULT now() NOT NULL,
    close_intent boolean DEFAULT false NOT NULL
);

CREATE TABLE public.issue_reaction (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    issue_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    emoji text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT issue_reaction_actor_type_check CHECK ((actor_type = ANY (ARRAY['member'::text, 'agent'::text])))
);

CREATE TABLE public.issue_subscriber (
    issue_id uuid NOT NULL,
    user_type text NOT NULL,
    user_id uuid NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT issue_subscriber_reason_check CHECK ((reason = ANY (ARRAY['creator'::text, 'assignee'::text, 'commenter'::text, 'mentioned'::text, 'manual'::text, 'autopilot'::text]))),
    CONSTRAINT issue_subscriber_user_type_check CHECK ((user_type = ANY (ARRAY['member'::text, 'agent'::text])))
);

CREATE TABLE public.issue_to_label (
    issue_id uuid NOT NULL,
    label_id uuid NOT NULL
);

CREATE TABLE public.lark_binding_token (
    token_hash text NOT NULL,
    workspace_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_open_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lark_binding_token_ttl_cap CHECK ((expires_at <= (created_at + '00:15:00'::interval)))
);

CREATE TABLE public.lark_chat_session_binding (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    chat_session_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_chat_id text NOT NULL,
    lark_chat_type text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lark_chat_session_binding_lark_chat_type_check CHECK ((lark_chat_type = ANY (ARRAY['p2p'::text, 'group'::text])))
);

CREATE TABLE public.lark_inbound_audit (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    installation_id uuid,
    lark_chat_id text,
    event_type text NOT NULL,
    lark_event_id text,
    lark_message_id text,
    drop_reason text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.lark_inbound_message_dedup (
    installation_id uuid NOT NULL,
    message_id text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    claim_token uuid DEFAULT gen_random_uuid() NOT NULL
);

CREATE TABLE public.lark_installation (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    app_id text NOT NULL,
    app_secret_encrypted bytea NOT NULL,
    tenant_key text,
    bot_open_id text NOT NULL,
    installer_user_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    ws_lease_token text,
    ws_lease_expires_at timestamp with time zone,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    bot_union_id text,
    region text DEFAULT 'feishu'::text NOT NULL,
    CONSTRAINT lark_installation_region_check CHECK ((region = ANY (ARRAY['feishu'::text, 'lark'::text]))),
    CONSTRAINT lark_installation_status_check CHECK ((status = ANY (ARRAY['active'::text, 'revoked'::text])))
);

CREATE TABLE public.lark_outbound_card_message (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    chat_session_id uuid NOT NULL,
    task_id uuid,
    lark_chat_id text NOT NULL,
    lark_card_message_id text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    last_patched_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lark_outbound_card_message_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'streaming'::text, 'final'::text, 'error'::text])))
);

CREATE TABLE public.lark_user_binding (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    multica_user_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_open_id text NOT NULL,
    union_id text,
    bound_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.member (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT member_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'admin'::text, 'member'::text])))
);

CREATE TABLE public.notification_preference (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    preferences jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.personal_access_token (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    token_prefix text NOT NULL,
    expires_at timestamp with time zone,
    last_used_at timestamp with time zone,
    revoked boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.pinned_item (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    item_type text NOT NULL,
    item_id uuid NOT NULL,
    "position" double precision DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pinned_item_item_type_check CHECK ((item_type = ANY (ARRAY['issue'::text, 'project'::text])))
);

CREATE TABLE public.project (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    title text NOT NULL,
    description text,
    icon text,
    status text DEFAULT 'planned'::text NOT NULL,
    lead_type text,
    lead_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    priority text DEFAULT 'none'::text NOT NULL,
    scope text DEFAULT 'workspace'::text NOT NULL,
    owner_id uuid,
    CONSTRAINT project_lead_type_check CHECK ((lead_type = ANY (ARRAY['member'::text, 'agent'::text]))),
    CONSTRAINT project_priority_check CHECK ((priority = ANY (ARRAY['urgent'::text, 'high'::text, 'medium'::text, 'low'::text, 'none'::text]))),
    CONSTRAINT project_scope_check CHECK ((scope = ANY (ARRAY['personal'::text, 'workspace'::text]))),
    CONSTRAINT project_status_check CHECK ((status = ANY (ARRAY['planned'::text, 'in_progress'::text, 'paused'::text, 'completed'::text, 'cancelled'::text])))
);

CREATE TABLE public.project_resource (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    resource_type text NOT NULL,
    resource_ref jsonb NOT NULL,
    label text,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid
);

CREATE TABLE public.prompt_evaluation_asset (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    prompt_id uuid,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    asset_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    structure_schema text DEFAULT 'multica.training_evaluation.asset_profile.v1'::text NOT NULL,
    structured_case_count integer DEFAULT 0 NOT NULL,
    structured_variable_count integer DEFAULT 0 NOT NULL,
    structured_assertion_count integer DEFAULT 0 NOT NULL,
    linked_dataset_count integer DEFAULT 0 NOT NULL,
    linked_prompt_count integer DEFAULT 0 NOT NULL,
    evaluation_dimension_count integer DEFAULT 0 NOT NULL,
    dataset_row_count integer DEFAULT 0 NOT NULL,
    test_suite_case_count integer DEFAULT 0 NOT NULL,
    experiment_dimension_count integer DEFAULT 0 NOT NULL,
    CONSTRAINT prompt_evaluation_asset_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text]))),
    CONSTRAINT prompt_evaluation_asset_type_check CHECK ((asset_type = ANY (ARRAY['数据集'::text, '测试套件'::text])))
);

CREATE TABLE public.prompt_evaluation_case (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    prompt_id uuid,
    case_index integer DEFAULT 0 NOT NULL,
    case_name text DEFAULT ''::text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected_contains jsonb DEFAULT '[]'::jsonb NOT NULL,
    input jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected jsonb DEFAULT '{}'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    source text DEFAULT 'payload'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_case_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text, 'draft'::text, 'approved'::text, 'active'::text])))
);

CREATE TABLE public.prompt_evaluation_case_assertion (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    case_id uuid NOT NULL,
    assertion_index integer DEFAULT 0 NOT NULL,
    assertion_type text DEFAULT '包含文本'::text NOT NULL,
    expected_text text NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    source text DEFAULT 'expected_contains'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_case_assertion_assertion_type_check CHECK ((assertion_type = '包含文本'::text)),
    CONSTRAINT prompt_evaluation_case_assertion_source_check CHECK ((source = 'expected_contains'::text)),
    CONSTRAINT prompt_evaluation_case_assertion_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text, 'draft'::text, 'approved'::text, 'active'::text])))
);

CREATE TABLE public.prompt_evaluation_case_operation (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    operation_type text DEFAULT ''::text NOT NULL,
    filter jsonb DEFAULT '{}'::jsonb NOT NULL,
    input jsonb DEFAULT '{}'::jsonb NOT NULL,
    changed_count integer DEFAULT 0 NOT NULL,
    skipped_count integer DEFAULT 0 NOT NULL,
    sample_case_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT '已完成'::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.prompt_evaluation_dataset_row (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    dataset_asset_id uuid NOT NULL,
    case_id uuid NOT NULL,
    row_index integer DEFAULT 0 NOT NULL,
    row_name text DEFAULT ''::text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected_contains jsonb DEFAULT '[]'::jsonb NOT NULL,
    expected jsonb DEFAULT '{}'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    source text DEFAULT 'payload'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_dataset_row_source_check CHECK ((source = ANY (ARRAY['payload'::text, 'manual'::text, 'trace'::text]))),
    CONSTRAINT prompt_evaluation_dataset_row_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text, 'draft'::text, 'approved'::text, 'active'::text])))
);

CREATE TABLE public.prompt_evaluation_dataset_version (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    dataset_asset_id uuid NOT NULL,
    version integer NOT NULL,
    version_label text DEFAULT ''::text NOT NULL,
    row_count integer DEFAULT 0 NOT NULL,
    row_fingerprint text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.prompt_evaluation_dataset_version_row (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    dataset_version_id uuid NOT NULL,
    dataset_asset_id uuid NOT NULL,
    source_row_id uuid,
    case_id uuid,
    row_index integer DEFAULT 0 NOT NULL,
    row_name text DEFAULT ''::text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected_contains jsonb DEFAULT '[]'::jsonb NOT NULL,
    expected jsonb DEFAULT '{}'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    source text DEFAULT 'payload'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.prompt_evaluation_dimension_score (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    run_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    prompt_id uuid,
    dimension_index integer DEFAULT 0 NOT NULL,
    dimension_name text DEFAULT ''::text NOT NULL,
    score double precision DEFAULT 0 NOT NULL,
    passed_cases integer DEFAULT 0 NOT NULL,
    total_cases integer DEFAULT 0 NOT NULL,
    status text DEFAULT '待执行'::text NOT NULL,
    rule text DEFAULT ''::text NOT NULL,
    evidence text DEFAULT ''::text NOT NULL,
    source text DEFAULT 'run_metrics'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_dimension_score_source_check CHECK ((source = ANY (ARRAY['run_metrics'::text, 'agent_sync'::text, 'local_run'::text]))),
    CONSTRAINT prompt_evaluation_dimension_score_status_check CHECK ((status = ANY (ARRAY['待执行'::text, '已评分'::text, '无用例'::text])))
);

CREATE TABLE public.prompt_evaluation_evidence_snapshot (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    run_id uuid NOT NULL,
    snapshot_type text DEFAULT '手动归档'::text NOT NULL,
    schema_version text DEFAULT 'multica.prompt_evaluation.evidence_snapshot.v1'::text NOT NULL,
    summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_evidence_snapshot_type_check CHECK ((snapshot_type = ANY (ARRAY['手动归档'::text, '验收归档'::text, '自动归档'::text])))
);

CREATE TABLE public.prompt_evaluation_optimization_candidate (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    run_id uuid NOT NULL,
    prompt_id uuid NOT NULL,
    candidate_name text NOT NULL,
    candidate_content text NOT NULL,
    rationale text DEFAULT ''::text NOT NULL,
    failed_case_count integer DEFAULT 0 NOT NULL,
    source_failure_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    source_prompt_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    metrics jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT '待确认'::text NOT NULL,
    published_prompt_id uuid,
    published_at timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_optimization_candidate_status_check CHECK ((status = ANY (ARRAY['待确认'::text, '已发布'::text, '已拒绝'::text])))
);

CREATE TABLE public.prompt_evaluation_run (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    prompt_id uuid,
    run_kind text NOT NULL,
    status text DEFAULT '已入队'::text NOT NULL,
    trigger_source text DEFAULT '手动'::text NOT NULL,
    agent_id uuid,
    runtime_id uuid,
    task_id uuid,
    chat_session_id uuid,
    model text DEFAULT ''::text NOT NULL,
    runtime_provider text DEFAULT ''::text NOT NULL,
    total_cases integer DEFAULT 0 NOT NULL,
    passed_cases integer DEFAULT 0 NOT NULL,
    failed_cases integer DEFAULT 0 NOT NULL,
    pass_rate double precision DEFAULT 0 NOT NULL,
    total_duration_ms bigint DEFAULT 0 NOT NULL,
    average_duration_ms bigint DEFAULT 0 NOT NULL,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    estimated_cost double precision DEFAULT 0 NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    conclusion text DEFAULT ''::text NOT NULL,
    metrics jsonb DEFAULT '{}'::jsonb NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    review_decision text DEFAULT ''::text NOT NULL,
    review_note text DEFAULT ''::text NOT NULL,
    reviewed_by uuid,
    reviewed_at timestamp with time zone,
    CONSTRAINT prompt_evaluation_run_review_decision_check CHECK ((review_decision = ANY (ARRAY[''::text, '通过'::text, '未通过'::text]))),
    CONSTRAINT prompt_evaluation_run_run_kind_check CHECK ((run_kind = ANY (ARRAY['本地渲染'::text, 'Agent执行'::text]))),
    CONSTRAINT prompt_evaluation_run_status_check CHECK ((status = ANY (ARRAY['已入队'::text, '运行中'::text, '通过'::text, '未通过'::text, '失败'::text, '已取消'::text, '需人工复核'::text])))
);

CREATE TABLE public.prompt_evaluation_test_suite_case (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    test_suite_asset_id uuid NOT NULL,
    case_id uuid NOT NULL,
    case_index integer DEFAULT 0 NOT NULL,
    case_name text DEFAULT ''::text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected_contains jsonb DEFAULT '[]'::jsonb NOT NULL,
    expected jsonb DEFAULT '{}'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    source text DEFAULT 'payload'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_test_suite_case_source_check CHECK ((source = ANY (ARRAY['payload'::text, 'manual'::text]))),
    CONSTRAINT prompt_evaluation_test_suite_case_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text, 'draft'::text, 'approved'::text, 'active'::text])))
);

CREATE TABLE public.prompt_evaluation_trial (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    case_index integer DEFAULT 0 NOT NULL,
    case_name text DEFAULT ''::text NOT NULL,
    status text DEFAULT '待执行'::text NOT NULL,
    input jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected jsonb DEFAULT '{}'::jsonb NOT NULL,
    output jsonb DEFAULT '{}'::jsonb NOT NULL,
    rendered_prompt text DEFAULT ''::text NOT NULL,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    duration_ms bigint DEFAULT 0 NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_evaluation_trial_status_check CHECK ((status = ANY (ARRAY['待执行'::text, '通过'::text, '未通过'::text, '失败'::text, '已跳过'::text, '需人工复核'::text])))
);

CREATE TABLE public.prompt_library_item (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    project_id uuid,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    prompt_type text DEFAULT '通用'::text NOT NULL,
    content text NOT NULL,
    variables jsonb DEFAULT '[]'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT '启用'::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_library_item_status_check CHECK ((status = ANY (ARRAY['启用'::text, '归档'::text]))),
    CONSTRAINT prompt_library_item_version_check CHECK ((version > 0))
);

CREATE TABLE public.prompt_library_version (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    prompt_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    project_id uuid,
    version integer NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    prompt_type text DEFAULT '通用'::text NOT NULL,
    content text NOT NULL,
    variables jsonb DEFAULT '[]'::jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    source text DEFAULT '手动创建'::text NOT NULL,
    source_candidate_id uuid,
    change_note text DEFAULT ''::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT prompt_library_version_source_check CHECK ((source = ANY (ARRAY['手动创建'::text, '手动更新'::text, '优化候选发布'::text, '历史回填'::text]))),
    CONSTRAINT prompt_library_version_version_check CHECK ((version > 0))
);

CREATE TABLE public.prompt_library_trial (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    prompt_id uuid NOT NULL,
    version_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    chat_session_id uuid,
    task_id uuid,
    input text DEFAULT ''::text NOT NULL,
    rendered_message text DEFAULT ''::text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    output_preview text DEFAULT ''::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.agent_playground_experiment (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    dataset_asset_id uuid,
    dataset_version_id uuid,
    judge_agent_id uuid,
    status text DEFAULT 'draft'::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_experiment_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'ready'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);

CREATE TABLE public.agent_playground_input (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    experiment_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    dataset_row_id uuid,
    row_index integer DEFAULT 0 NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    input text NOT NULL,
    variables jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.agent_playground_agent (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    experiment_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    display_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.agent_playground_result (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    experiment_id uuid NOT NULL,
    input_id uuid NOT NULL,
    experiment_agent_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    chat_session_id uuid,
    task_id uuid,
    rendered_input text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    output text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_result_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'queued'::text, 'dispatched'::text, 'running'::text, 'waiting_local_directory'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

CREATE TABLE public.agent_playground_judgement (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    experiment_id uuid NOT NULL,
    input_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    judge_agent_id uuid NOT NULL,
    chat_session_id uuid,
    task_id uuid,
    status text DEFAULT 'pending'::text NOT NULL,
    output text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agent_playground_judgement_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'queued'::text, 'dispatched'::text, 'running'::text, 'waiting_local_directory'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

CREATE TABLE public.runtime_profile (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    display_name text NOT NULL,
    protocol_family text NOT NULL,
    command_name text NOT NULL,
    description text,
    fixed_args jsonb DEFAULT '[]'::jsonb NOT NULL,
    visibility text DEFAULT 'workspace'::text NOT NULL,
    created_by uuid,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT runtime_profile_protocol_family_check CHECK ((protocol_family = ANY (ARRAY['claude'::text, 'codebuddy'::text, 'codex'::text, 'copilot'::text, 'opencode'::text, 'openclaw'::text, 'hermes'::text, 'gemini'::text, 'pi'::text, 'cursor'::text, 'kimi'::text, 'kiro'::text, 'antigravity'::text]))),
    CONSTRAINT runtime_profile_visibility_check CHECK ((visibility = ANY (ARRAY['workspace'::text, 'private'::text])))
);

CREATE TABLE public.skill (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.skill_file (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    skill_id uuid NOT NULL,
    path text NOT NULL,
    content text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.squad (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    leader_id uuid NOT NULL,
    creator_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    archived_by uuid,
    avatar_url text,
    instructions text DEFAULT ''::text NOT NULL,
    sop_profile jsonb DEFAULT '{}'::jsonb NOT NULL,
    scope text DEFAULT 'workspace'::text NOT NULL,
    CONSTRAINT squad_scope_check CHECK ((scope = ANY (ARRAY['personal'::text, 'workspace'::text])))
);

CREATE TABLE public.squad_member (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    squad_id uuid NOT NULL,
    member_type text NOT NULL,
    member_id uuid NOT NULL,
    role text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT squad_member_member_type_check CHECK ((member_type = ANY (ARRAY['agent'::text, 'member'::text])))
);

CREATE TABLE public.squad_sop_run (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    squad_id uuid NOT NULL,
    leader_task_id uuid,
    profile_key text DEFAULT ''::text NOT NULL,
    profile jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT '进行中'::text NOT NULL,
    current_step_key text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    total_duration_ms bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.squad_sop_step_event (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    squad_id uuid NOT NULL,
    step_key text NOT NULL,
    step_name text DEFAULT ''::text NOT NULL,
    role_key text DEFAULT ''::text NOT NULL,
    event_type text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    duration_ms bigint,
    created_by_type text DEFAULT ''::text NOT NULL,
    created_by_id uuid,
    task_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.sys_cron_executions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_name text NOT NULL,
    scope_kind text DEFAULT 'global'::text NOT NULL,
    scope_id text DEFAULT 'global'::text NOT NULL,
    plan_time timestamp with time zone NOT NULL,
    status text NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    next_retry_at timestamp with time zone,
    runner_id text,
    lease_token uuid DEFAULT gen_random_uuid() NOT NULL,
    heartbeat_at timestamp with time zone,
    stale_after timestamp with time zone,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    duration_ms integer,
    rows_affected bigint,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_code text,
    error_msg text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_sys_cron_attempt CHECK (((attempt >= 1) AND (max_attempts >= attempt))),
    CONSTRAINT chk_sys_cron_duration CHECK (((duration_ms IS NULL) OR (duration_ms >= 0))),
    CONSTRAINT chk_sys_cron_status CHECK ((status = ANY (ARRAY['RUNNING'::text, 'SUCCESS'::text, 'FAILED'::text])))
);

CREATE TABLE public.task_message (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    task_id uuid NOT NULL,
    seq integer NOT NULL,
    type text NOT NULL,
    tool text,
    content text,
    input jsonb,
    output text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_token (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    token_hash text NOT NULL,
    task_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_trace_event (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    issue_id uuid,
    agent_id uuid NOT NULL,
    runtime_id uuid,
    squad_id uuid,
    project_id uuid,
    source text DEFAULT ''::text NOT NULL,
    event_type text NOT NULL,
    event_name text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    duration_ms bigint,
    queue_wait_ms bigint,
    run_ms bigint,
    total_ms bigint,
    provider text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    error_type text DEFAULT ''::text NOT NULL,
    trigger_comment_id uuid,
    autopilot_run_id uuid,
    chat_session_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_usage (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    task_id uuid NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    model text NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now()
);

CREATE TABLE public.task_usage_hourly (
    bucket_hour timestamp with time zone NOT NULL,
    workspace_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    project_id uuid,
    provider text NOT NULL,
    model text NOT NULL,
    input_tokens bigint DEFAULT 0 NOT NULL,
    output_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_tokens bigint DEFAULT 0 NOT NULL,
    task_count bigint DEFAULT 0 NOT NULL,
    event_count bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_usage_hourly_dirty (
    bucket_hour timestamp with time zone NOT NULL,
    workspace_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    project_id uuid,
    provider text NOT NULL,
    model text NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_usage_hourly_rollup_state (
    id smallint DEFAULT 1 NOT NULL,
    watermark_at timestamp with time zone DEFAULT '1970-01-01 00:00:00+00'::timestamp with time zone NOT NULL,
    last_run_started_at timestamp with time zone,
    last_run_finished_at timestamp with time zone,
    last_run_rows bigint DEFAULT 0 NOT NULL,
    last_error text,
    CONSTRAINT task_usage_hourly_rollup_state_id_check CHECK ((id = 1))
);

CREATE TABLE public."user" (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    account text NOT NULL,
    avatar_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    onboarded_at timestamp with time zone,
    onboarding_questionnaire jsonb DEFAULT '{}'::jsonb NOT NULL,
    starter_content_state text,
    profile_description text DEFAULT ''::text NOT NULL,
    timezone text,
    password_hash text
);

CREATE TABLE public.webhook_delivery (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    autopilot_id uuid NOT NULL,
    trigger_id uuid NOT NULL,
    provider text NOT NULL,
    event text DEFAULT 'webhook.received'::text NOT NULL,
    dedupe_key text,
    dedupe_source text,
    signature_status text DEFAULT 'not_required'::text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempt_count integer DEFAULT 1 NOT NULL,
    selected_headers jsonb DEFAULT '{}'::jsonb NOT NULL,
    content_type text,
    raw_body bytea,
    response_status integer,
    response_body text,
    autopilot_run_id uuid,
    replayed_from_delivery_id uuid,
    error text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    last_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_delivery_provider_check CHECK ((provider = ANY (ARRAY['generic'::text, 'github'::text]))),
    CONSTRAINT webhook_delivery_signature_status_check CHECK ((signature_status = ANY (ARRAY['not_required'::text, 'valid'::text, 'invalid'::text, 'missing'::text]))),
    CONSTRAINT webhook_delivery_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'dispatched'::text, 'rejected'::text, 'ignored'::text, 'failed'::text])))
);

CREATE TABLE public.workspace (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    context text,
    repos jsonb DEFAULT '[]'::jsonb NOT NULL,
    issue_prefix text DEFAULT ''::text NOT NULL,
    issue_counter integer DEFAULT 0 NOT NULL,
    avatar_url text
);

ALTER TABLE ONLY public.activity_log
    ADD CONSTRAINT activity_log_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent
    ADD CONSTRAINT agent_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_runtime
    ADD CONSTRAINT agent_runtime_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_skill
    ADD CONSTRAINT agent_skill_pkey PRIMARY KEY (agent_id, skill_id);

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.autopilot
    ADD CONSTRAINT autopilot_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.autopilot_subscriber
    ADD CONSTRAINT autopilot_subscriber_pkey PRIMARY KEY (autopilot_id, user_type, user_id);

ALTER TABLE ONLY public.autopilot_trigger
    ADD CONSTRAINT autopilot_trigger_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.chat_message
    ADD CONSTRAINT chat_message_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.chat_session
    ADD CONSTRAINT chat_session_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.comment
    ADD CONSTRAINT comment_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.comment_reaction
    ADD CONSTRAINT comment_reaction_comment_id_actor_type_actor_id_emoji_key UNIQUE (comment_id, actor_type, actor_id, emoji);

ALTER TABLE ONLY public.comment_reaction
    ADD CONSTRAINT comment_reaction_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.daemon_connection
    ADD CONSTRAINT daemon_connection_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.daemon_token
    ADD CONSTRAINT daemon_token_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.external_credential_profile
    ADD CONSTRAINT external_credential_profile_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.external_credential_profile
    ADD CONSTRAINT external_credential_profile_user_id_provider_name_key UNIQUE (user_id, provider, name);

ALTER TABLE ONLY public.feedback
    ADD CONSTRAINT feedback_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.github_installation
    ADD CONSTRAINT github_installation_installation_id_key UNIQUE (installation_id);

ALTER TABLE ONLY public.github_installation
    ADD CONSTRAINT github_installation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.github_pending_check_suite
    ADD CONSTRAINT github_pending_check_suite_pkey PRIMARY KEY (workspace_id, repo_owner, repo_name, pr_number, suite_id);

ALTER TABLE ONLY public.github_pending_installation
    ADD CONSTRAINT github_pending_installation_pkey PRIMARY KEY (installation_id);

ALTER TABLE ONLY public.github_pull_request_check_suite
    ADD CONSTRAINT github_pull_request_check_suite_pkey PRIMARY KEY (pr_id, suite_id);

ALTER TABLE ONLY public.github_pull_request
    ADD CONSTRAINT github_pull_request_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.github_pull_request
    ADD CONSTRAINT github_pull_request_workspace_id_repo_owner_repo_name_pr_nu_key UNIQUE (workspace_id, repo_owner, repo_name, pr_number);

ALTER TABLE ONLY public.inbox_item
    ADD CONSTRAINT inbox_item_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.issue_dependency
    ADD CONSTRAINT issue_dependency_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.issue_label
    ADD CONSTRAINT issue_label_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT issue_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.issue_pull_request
    ADD CONSTRAINT issue_pull_request_pkey PRIMARY KEY (issue_id, pull_request_id);

ALTER TABLE ONLY public.issue_reaction
    ADD CONSTRAINT issue_reaction_issue_id_actor_type_actor_id_emoji_key UNIQUE (issue_id, actor_type, actor_id, emoji);

ALTER TABLE ONLY public.issue_reaction
    ADD CONSTRAINT issue_reaction_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.issue_subscriber
    ADD CONSTRAINT issue_subscriber_pkey PRIMARY KEY (issue_id, user_type, user_id);

ALTER TABLE ONLY public.issue_to_label
    ADD CONSTRAINT issue_to_label_pkey PRIMARY KEY (issue_id, label_id);

ALTER TABLE ONLY public.lark_binding_token
    ADD CONSTRAINT lark_binding_token_pkey PRIMARY KEY (token_hash);

ALTER TABLE ONLY public.lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_chat_session_id_key UNIQUE (chat_session_id);

ALTER TABLE ONLY public.lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_installation_id_lark_chat_id_key UNIQUE (installation_id, lark_chat_id);

ALTER TABLE ONLY public.lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lark_inbound_audit
    ADD CONSTRAINT lark_inbound_audit_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lark_inbound_message_dedup
    ADD CONSTRAINT lark_inbound_message_dedup_pkey PRIMARY KEY (installation_id, message_id);

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_app_id_key UNIQUE (app_id);

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_workspace_id_agent_id_key UNIQUE (workspace_id, agent_id);

ALTER TABLE ONLY public.lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lark_user_binding
    ADD CONSTRAINT lark_user_binding_installation_id_lark_open_id_key UNIQUE (installation_id, lark_open_id);

ALTER TABLE ONLY public.lark_user_binding
    ADD CONSTRAINT lark_user_binding_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.member
    ADD CONSTRAINT member_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.member
    ADD CONSTRAINT member_workspace_id_user_id_key UNIQUE (workspace_id, user_id);

ALTER TABLE ONLY public.notification_preference
    ADD CONSTRAINT notification_preference_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notification_preference
    ADD CONSTRAINT notification_preference_workspace_id_user_id_key UNIQUE (workspace_id, user_id);

ALTER TABLE ONLY public.personal_access_token
    ADD CONSTRAINT personal_access_token_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.pinned_item
    ADD CONSTRAINT pinned_item_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.pinned_item
    ADD CONSTRAINT pinned_item_workspace_id_user_id_item_type_item_id_key UNIQUE (workspace_id, user_id, item_type, item_id);

ALTER TABLE ONLY public.project
    ADD CONSTRAINT project_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.project_resource
    ADD CONSTRAINT project_resource_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.project_resource
    ADD CONSTRAINT project_resource_project_id_resource_type_resource_ref_key UNIQUE (project_id, resource_type, resource_ref);

ALTER TABLE ONLY public.prompt_evaluation_asset
    ADD CONSTRAINT prompt_evaluation_asset_name_unique UNIQUE (workspace_id, asset_type, name);

ALTER TABLE ONLY public.prompt_evaluation_asset
    ADD CONSTRAINT prompt_evaluation_asset_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_case_assertion
    ADD CONSTRAINT prompt_evaluation_case_assertion_case_index_unique UNIQUE (case_id, assertion_index);

ALTER TABLE ONLY public.prompt_evaluation_case_assertion
    ADD CONSTRAINT prompt_evaluation_case_assertion_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_asset_index_unique UNIQUE (asset_id, case_index);

ALTER TABLE ONLY public.prompt_evaluation_case_operation
    ADD CONSTRAINT prompt_evaluation_case_operation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_asset_index_unique UNIQUE (dataset_asset_id, row_index);

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_case_unique UNIQUE (case_id);

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_dataset_version
    ADD CONSTRAINT prompt_evaluation_dataset_version_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_unique UNIQUE (dataset_version_id, row_index);

ALTER TABLE ONLY public.prompt_evaluation_dataset_version
    ADD CONSTRAINT prompt_evaluation_dataset_version_unique UNIQUE (dataset_asset_id, version);

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_run_dimension_unique UNIQUE (run_id, dimension_index, dimension_name);

ALTER TABLE ONLY public.prompt_evaluation_evidence_snapshot
    ADD CONSTRAINT prompt_evaluation_evidence_snapshot_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_asset_index_unique UNIQUE (test_suite_asset_id, case_index);

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_case_unique UNIQUE (case_id);

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_evaluation_trial
    ADD CONSTRAINT prompt_evaluation_trial_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_library_item
    ADD CONSTRAINT prompt_library_item_name_unique UNIQUE (workspace_id, name);

ALTER TABLE ONLY public.prompt_library_item
    ADD CONSTRAINT prompt_library_item_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_input
    ADD CONSTRAINT agent_playground_input_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_input
    ADD CONSTRAINT agent_playground_input_experiment_index_key UNIQUE (experiment_id, row_index);

ALTER TABLE ONLY public.agent_playground_agent
    ADD CONSTRAINT agent_playground_agent_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_agent
    ADD CONSTRAINT agent_playground_agent_unique UNIQUE (experiment_id, agent_id);

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_unique UNIQUE (input_id, experiment_agent_id);

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_unique UNIQUE (input_id);

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_unique UNIQUE (prompt_id, version);

ALTER TABLE ONLY public.runtime_profile
    ADD CONSTRAINT runtime_profile_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.runtime_profile
    ADD CONSTRAINT runtime_profile_workspace_id_display_name_key UNIQUE (workspace_id, display_name);

ALTER TABLE ONLY public.skill_file
    ADD CONSTRAINT skill_file_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.skill_file
    ADD CONSTRAINT skill_file_skill_id_path_key UNIQUE (skill_id, path);

ALTER TABLE ONLY public.skill
    ADD CONSTRAINT skill_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.skill
    ADD CONSTRAINT skill_workspace_id_name_key UNIQUE (workspace_id, name);

ALTER TABLE ONLY public.squad_member
    ADD CONSTRAINT squad_member_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.squad_member
    ADD CONSTRAINT squad_member_squad_id_member_type_member_id_key UNIQUE (squad_id, member_type, member_id);

ALTER TABLE ONLY public.squad
    ADD CONSTRAINT squad_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.squad_sop_run
    ADD CONSTRAINT squad_sop_run_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.sys_cron_executions
    ADD CONSTRAINT sys_cron_executions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_message
    ADD CONSTRAINT task_message_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_token
    ADD CONSTRAINT task_token_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_usage_hourly_rollup_state
    ADD CONSTRAINT task_usage_hourly_rollup_state_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_usage
    ADD CONSTRAINT task_usage_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.task_usage
    ADD CONSTRAINT task_usage_task_id_provider_model_key UNIQUE (task_id, provider, model);

ALTER TABLE ONLY public.daemon_connection
    ADD CONSTRAINT uq_daemon_agent UNIQUE (agent_id, daemon_id);

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT uq_issue_workspace_number UNIQUE (workspace_id, number);

ALTER TABLE ONLY public.sys_cron_executions
    ADD CONSTRAINT uq_sys_cron_execution UNIQUE (job_name, scope_kind, scope_id, plan_time);

ALTER TABLE ONLY public.task_usage_hourly_dirty
    ADD CONSTRAINT uq_task_usage_hourly_dirty_key UNIQUE NULLS NOT DISTINCT (bucket_hour, workspace_id, runtime_id, agent_id, project_id, provider, model);

ALTER TABLE ONLY public.task_usage_hourly
    ADD CONSTRAINT uq_task_usage_hourly_key UNIQUE NULLS NOT DISTINCT (bucket_hour, workspace_id, runtime_id, agent_id, project_id, provider, model);

ALTER TABLE ONLY public."user"
    ADD CONSTRAINT user_account_key UNIQUE (account);

ALTER TABLE ONLY public."user"
    ADD CONSTRAINT user_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.workspace
    ADD CONSTRAINT workspace_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.workspace
    ADD CONSTRAINT workspace_slug_key UNIQUE (slug);

CREATE UNIQUE INDEX agent_personal_no_owner_name_active_unique ON public.agent USING btree (workspace_id, name) WHERE ((archived_at IS NULL) AND (scope = 'personal'::text) AND (owner_id IS NULL));

CREATE UNIQUE INDEX agent_personal_owner_name_active_unique ON public.agent USING btree (workspace_id, owner_id, name) WHERE ((archived_at IS NULL) AND (scope = 'personal'::text) AND (owner_id IS NOT NULL));

CREATE UNIQUE INDEX agent_runtime_workspace_daemon_profile_key ON public.agent_runtime USING btree (workspace_id, daemon_id, profile_id) WHERE (profile_id IS NOT NULL);

CREATE UNIQUE INDEX agent_runtime_workspace_daemon_provider_key ON public.agent_runtime USING btree (workspace_id, daemon_id, provider) WHERE (profile_id IS NULL);

CREATE UNIQUE INDEX agent_workspace_name_active_unique ON public.agent USING btree (workspace_id, name) WHERE ((archived_at IS NULL) AND (scope = 'workspace'::text));

CREATE INDEX comment_issue_resolved_at_idx ON public.comment USING btree (issue_id, resolved_at);

CREATE INDEX idx_activity_log_issue_keyset ON public.activity_log USING btree (issue_id, created_at DESC, id DESC);

CREATE INDEX idx_activity_log_squad_no_action_task ON public.activity_log USING btree (issue_id, actor_id, ((details ->> 'task_id'::text))) WHERE ((actor_type = 'agent'::text) AND (action = 'squad_leader_evaluated'::text) AND ((details ->> 'outcome'::text) = 'no_action'::text));

CREATE INDEX idx_agent_runtime_last_seen_at ON public.agent_runtime USING btree (last_seen_at);

CREATE INDEX idx_agent_runtime_status ON public.agent_runtime USING btree (workspace_id, status);

CREATE INDEX idx_agent_runtime_workspace ON public.agent_runtime USING btree (workspace_id);

CREATE INDEX idx_agent_runtime_workspace_scope ON public.agent_runtime USING btree (workspace_id, scope);

CREATE INDEX idx_agent_skill_agent ON public.agent_skill USING btree (agent_id);

CREATE INDEX idx_agent_skill_skill ON public.agent_skill USING btree (skill_id);

CREATE INDEX idx_agent_task_queue_agent ON public.agent_task_queue USING btree (agent_id, status);

CREATE INDEX idx_agent_task_queue_chat_pending ON public.agent_task_queue USING btree (chat_session_id, created_at DESC) WHERE ((chat_session_id IS NOT NULL) AND (status = ANY (ARRAY['queued'::text, 'dispatched'::text, 'running'::text])));

CREATE INDEX idx_agent_task_queue_claim_candidates ON public.agent_task_queue USING btree (runtime_id, priority DESC, created_at) WHERE (status = 'queued'::text);

CREATE INDEX idx_agent_task_queue_issue_id ON public.agent_task_queue USING btree (issue_id);

CREATE INDEX idx_agent_task_queue_parent ON public.agent_task_queue USING btree (parent_task_id);

CREATE INDEX idx_agent_task_queue_pending ON public.agent_task_queue USING btree (agent_id, priority DESC, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'dispatched'::text]));

CREATE INDEX idx_agent_task_queue_queued_created_at ON public.agent_task_queue USING btree (created_at) WHERE (status = 'queued'::text);

CREATE INDEX idx_agent_task_queue_running_started_at ON public.agent_task_queue USING btree (started_at) WHERE (status = 'running'::text);

CREATE INDEX idx_agent_task_queue_runtime_pending ON public.agent_task_queue USING btree (runtime_id, priority DESC, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'dispatched'::text]));

CREATE INDEX idx_agent_workspace ON public.agent USING btree (workspace_id);

CREATE INDEX idx_attachment_chat_message ON public.attachment USING btree (chat_message_id) WHERE (chat_message_id IS NOT NULL);

CREATE INDEX idx_attachment_chat_session ON public.attachment USING btree (chat_session_id) WHERE (chat_session_id IS NOT NULL);

CREATE INDEX idx_attachment_comment ON public.attachment USING btree (comment_id) WHERE (comment_id IS NOT NULL);

CREATE INDEX idx_attachment_issue ON public.attachment USING btree (issue_id) WHERE (issue_id IS NOT NULL);

CREATE INDEX idx_attachment_workspace ON public.attachment USING btree (workspace_id);

CREATE INDEX idx_autopilot_assignee ON public.autopilot USING btree (assignee_id);

CREATE INDEX idx_autopilot_assignee_type_id ON public.autopilot USING btree (assignee_type, assignee_id);

CREATE INDEX idx_autopilot_project ON public.autopilot USING btree (project_id);

CREATE INDEX idx_autopilot_run_autopilot ON public.autopilot_run USING btree (autopilot_id, created_at DESC);

CREATE INDEX idx_autopilot_run_issue ON public.autopilot_run USING btree (issue_id) WHERE (issue_id IS NOT NULL);

CREATE INDEX idx_autopilot_run_squad_id ON public.autopilot_run USING btree (squad_id) WHERE (squad_id IS NOT NULL);

CREATE INDEX idx_autopilot_run_status ON public.autopilot_run USING btree (autopilot_id, status) WHERE (status = ANY (ARRAY['issue_created'::text, 'running'::text]));

CREATE INDEX idx_autopilot_subscriber_user ON public.autopilot_subscriber USING btree (user_type, user_id);

CREATE INDEX idx_autopilot_trigger_autopilot ON public.autopilot_trigger USING btree (autopilot_id);

CREATE INDEX idx_autopilot_trigger_next_run ON public.autopilot_trigger USING btree (next_run_at) WHERE ((enabled = true) AND (kind = 'schedule'::text));

CREATE UNIQUE INDEX idx_autopilot_trigger_webhook_token ON public.autopilot_trigger USING btree (webhook_token) WHERE ((kind = 'webhook'::text) AND (webhook_token IS NOT NULL));

CREATE INDEX idx_autopilot_workspace ON public.autopilot USING btree (workspace_id);

CREATE INDEX idx_chat_message_session ON public.chat_message USING btree (chat_session_id, created_at);

CREATE INDEX idx_chat_session_creator ON public.chat_session USING btree (creator_id, workspace_id);

CREATE INDEX idx_chat_session_workspace ON public.chat_session USING btree (workspace_id);

CREATE INDEX idx_comment_issue_keyset ON public.comment USING btree (issue_id, created_at DESC, id DESC);

CREATE INDEX idx_comment_reaction_comment_id ON public.comment_reaction USING btree (comment_id);

CREATE UNIQUE INDEX idx_daemon_token_hash ON public.daemon_token USING btree (token_hash);

CREATE INDEX idx_daemon_token_workspace_daemon ON public.daemon_token USING btree (workspace_id, daemon_id);

CREATE INDEX idx_external_credential_profile_user_provider ON public.external_credential_profile USING btree (user_id, provider, created_at DESC);

CREATE INDEX idx_feedback_user_created ON public.feedback USING btree (user_id, created_at DESC);

CREATE INDEX idx_github_installation_workspace ON public.github_installation USING btree (workspace_id);

CREATE INDEX idx_github_pending_check_suite_received_at ON public.github_pending_check_suite USING btree (received_at);

CREATE INDEX idx_github_pr_check_suite_aggregate ON public.github_pull_request_check_suite USING btree (pr_id, head_sha, app_id, updated_at DESC);

CREATE INDEX idx_github_pull_request_workspace ON public.github_pull_request USING btree (workspace_id);

CREATE INDEX idx_inbox_recipient ON public.inbox_item USING btree (recipient_type, recipient_id, read);

CREATE INDEX idx_issue_assignee ON public.issue USING btree (assignee_type, assignee_id);

CREATE INDEX idx_issue_first_executed_at ON public.issue USING btree (workspace_id, first_executed_at) WHERE (first_executed_at IS NOT NULL);

CREATE INDEX idx_issue_metadata_gin ON public.issue USING gin (metadata jsonb_path_ops);

CREATE INDEX idx_issue_origin ON public.issue USING btree (origin_type, origin_id) WHERE (origin_type IS NOT NULL);

CREATE INDEX idx_issue_parent ON public.issue USING btree (parent_issue_id);

CREATE INDEX idx_issue_project ON public.issue USING btree (project_id);

CREATE INDEX idx_issue_pull_request_pr ON public.issue_pull_request USING btree (pull_request_id);

CREATE INDEX idx_issue_reaction_issue_id ON public.issue_reaction USING btree (issue_id);

CREATE INDEX idx_issue_status ON public.issue USING btree (workspace_id, status);

CREATE INDEX idx_issue_subscriber_user ON public.issue_subscriber USING btree (user_type, user_id);

CREATE INDEX idx_issue_work_completed_at ON public.issue USING btree (workspace_id, work_completed_at) WHERE (work_completed_at IS NOT NULL);

CREATE INDEX idx_issue_work_started_at ON public.issue USING btree (workspace_id, work_started_at) WHERE (work_started_at IS NOT NULL);

CREATE INDEX idx_issue_workspace ON public.issue USING btree (workspace_id);

CREATE INDEX idx_issue_workspace_number ON public.issue USING btree (workspace_id, number);

CREATE INDEX idx_issue_workspace_owner_scope ON public.issue USING btree (workspace_id, owner_id, scope);

CREATE INDEX idx_issue_workspace_scope ON public.issue USING btree (workspace_id, scope);

CREATE INDEX idx_lark_binding_token_installation ON public.lark_binding_token USING btree (installation_id, expires_at);

CREATE INDEX idx_lark_chat_session_binding_session ON public.lark_chat_session_binding USING btree (chat_session_id);

CREATE INDEX idx_lark_inbound_audit_installation ON public.lark_inbound_audit USING btree (installation_id, received_at DESC);

CREATE INDEX idx_lark_inbound_audit_reason ON public.lark_inbound_audit USING btree (drop_reason, received_at DESC);

CREATE INDEX idx_lark_inbound_dedup_received ON public.lark_inbound_message_dedup USING btree (received_at);

CREATE INDEX idx_lark_installation_agent ON public.lark_installation USING btree (agent_id);

CREATE INDEX idx_lark_installation_lease ON public.lark_installation USING btree (ws_lease_expires_at) WHERE (status = 'active'::text);

CREATE INDEX idx_lark_installation_workspace ON public.lark_installation USING btree (workspace_id);

CREATE INDEX idx_lark_outbound_card_session ON public.lark_outbound_card_message USING btree (chat_session_id, created_at DESC);

CREATE UNIQUE INDEX idx_lark_outbound_card_task ON public.lark_outbound_card_message USING btree (task_id) WHERE (task_id IS NOT NULL);

CREATE INDEX idx_lark_user_binding_user ON public.lark_user_binding USING btree (multica_user_id, workspace_id);

CREATE INDEX idx_lark_user_binding_workspace_open ON public.lark_user_binding USING btree (workspace_id, lark_open_id);

CREATE INDEX idx_member_user_workspace ON public.member USING btree (user_id, workspace_id);

CREATE INDEX idx_member_workspace ON public.member USING btree (workspace_id);

CREATE UNIQUE INDEX idx_one_pending_task_per_issue_agent ON public.agent_task_queue USING btree (issue_id, agent_id) WHERE (status = ANY (ARRAY['queued'::text, 'dispatched'::text]));

CREATE UNIQUE INDEX idx_pat_token_hash ON public.personal_access_token USING btree (token_hash);

CREATE INDEX idx_pat_user ON public.personal_access_token USING btree (user_id, revoked);

CREATE INDEX idx_pinned_item_user_ws ON public.pinned_item USING btree (workspace_id, user_id, "position");

CREATE INDEX idx_project_resource_project ON public.project_resource USING btree (project_id, "position");

CREATE INDEX idx_project_resource_workspace ON public.project_resource USING btree (workspace_id);

CREATE INDEX idx_project_workspace ON public.project USING btree (workspace_id);

CREATE INDEX idx_project_workspace_owner_scope ON public.project USING btree (workspace_id, owner_id, scope);

CREATE INDEX idx_project_workspace_scope ON public.project USING btree (workspace_id, scope);

CREATE INDEX idx_prompt_evaluation_case_assertion_asset ON public.prompt_evaluation_case_assertion USING btree (asset_id, case_id, assertion_index);

CREATE INDEX idx_prompt_evaluation_case_assertion_case ON public.prompt_evaluation_case_assertion USING btree (case_id, assertion_index);

CREATE INDEX idx_prompt_evaluation_case_assertion_workspace ON public.prompt_evaluation_case_assertion USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_asset_index ON public.prompt_evaluation_case USING btree (asset_id, case_index);

CREATE INDEX idx_prompt_evaluation_case_operation_asset_created ON public.prompt_evaluation_case_operation USING btree (asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_operation_status_created ON public.prompt_evaluation_case_operation USING btree (workspace_id, status, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_operation_workspace_created ON public.prompt_evaluation_case_operation USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_case_stream_created ON public.prompt_evaluation_case USING btree (workspace_id, asset_id, status, source, created_at DESC, id);

CREATE INDEX idx_prompt_evaluation_case_stream_index ON public.prompt_evaluation_case USING btree (workspace_id, asset_id, status, source, case_index, id);

CREATE INDEX idx_prompt_evaluation_case_stream_updated ON public.prompt_evaluation_case USING btree (workspace_id, asset_id, status, source, updated_at DESC, id);

CREATE INDEX idx_prompt_evaluation_case_tags_gin ON public.prompt_evaluation_case USING gin (tags);

CREATE INDEX idx_prompt_evaluation_case_workspace_created ON public.prompt_evaluation_case USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dataset_row_asset_index ON public.prompt_evaluation_dataset_row USING btree (dataset_asset_id, row_index);

CREATE INDEX idx_prompt_evaluation_dataset_row_workspace_created ON public.prompt_evaluation_dataset_row USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dataset_version_asset_created ON public.prompt_evaluation_dataset_version USING btree (dataset_asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dataset_version_row_asset ON public.prompt_evaluation_dataset_version_row USING btree (dataset_asset_id, row_index);

CREATE INDEX idx_prompt_evaluation_dataset_version_row_version_index ON public.prompt_evaluation_dataset_version_row USING btree (dataset_version_id, row_index);

CREATE INDEX idx_prompt_evaluation_dataset_version_workspace_created ON public.prompt_evaluation_dataset_version USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dimension_score_asset_dimension ON public.prompt_evaluation_dimension_score USING btree (asset_id, dimension_index, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dimension_score_prompt_dimension ON public.prompt_evaluation_dimension_score USING btree (prompt_id, dimension_index, created_at DESC) WHERE (prompt_id IS NOT NULL);

CREATE INDEX idx_prompt_evaluation_dimension_score_workspace_created ON public.prompt_evaluation_dimension_score USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_evidence_snapshot_run_created ON public.prompt_evaluation_evidence_snapshot USING btree (run_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_evidence_snapshot_workspace_created ON public.prompt_evaluation_evidence_snapshot USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_prompt ON public.prompt_evaluation_optimization_candidate USING btree (prompt_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_run ON public.prompt_evaluation_optimization_candidate USING btree (run_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_optimization_candidate_workspace_created ON public.prompt_evaluation_optimization_candidate USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_run_asset_created ON public.prompt_evaluation_run USING btree (asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_run_task ON public.prompt_evaluation_run USING btree (task_id) WHERE (task_id IS NOT NULL);

CREATE INDEX idx_prompt_evaluation_run_workspace_created ON public.prompt_evaluation_run USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_run_workspace_reviewed ON public.prompt_evaluation_run USING btree (workspace_id, reviewed_at DESC) WHERE (reviewed_at IS NOT NULL);

CREATE INDEX idx_prompt_evaluation_test_suite_case_asset_index ON public.prompt_evaluation_test_suite_case USING btree (test_suite_asset_id, case_index);

CREATE INDEX idx_prompt_evaluation_test_suite_case_workspace_created ON public.prompt_evaluation_test_suite_case USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_trial_asset_created ON public.prompt_evaluation_trial USING btree (asset_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_trial_run_case ON public.prompt_evaluation_trial USING btree (run_id, case_index);

CREATE INDEX idx_prompt_library_version_prompt_version ON public.prompt_library_version USING btree (prompt_id, version DESC);

CREATE INDEX idx_prompt_library_version_workspace_created ON public.prompt_library_version USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_prompt_library_trial_prompt_created ON public.prompt_library_trial USING btree (prompt_id, created_at DESC);

CREATE INDEX idx_prompt_library_trial_workspace_created ON public.prompt_library_trial USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_agent_playground_experiment_workspace_created ON public.agent_playground_experiment USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_agent_playground_input_experiment_index ON public.agent_playground_input USING btree (experiment_id, row_index);

CREATE INDEX idx_agent_playground_agent_experiment_order ON public.agent_playground_agent USING btree (experiment_id, display_order);

CREATE INDEX idx_agent_playground_result_experiment ON public.agent_playground_result USING btree (experiment_id, input_id, experiment_agent_id);

CREATE INDEX idx_agent_playground_judgement_experiment ON public.agent_playground_judgement USING btree (experiment_id, input_id);

CREATE INDEX idx_runtime_profile_workspace ON public.runtime_profile USING btree (workspace_id);

CREATE INDEX idx_skill_file_skill ON public.skill_file USING btree (skill_id);

CREATE INDEX idx_skill_workspace ON public.skill USING btree (workspace_id);

CREATE INDEX idx_squad_member_entity ON public.squad_member USING btree (member_type, member_id);

CREATE INDEX idx_squad_member_squad ON public.squad_member USING btree (squad_id);

CREATE INDEX idx_squad_sop_run_issue_created ON public.squad_sop_run USING btree (issue_id, created_at DESC);

CREATE UNIQUE INDEX idx_squad_sop_run_issue_open ON public.squad_sop_run USING btree (issue_id) WHERE (status = ANY (ARRAY['待开始'::text, '进行中'::text, '已阻塞'::text]));

CREATE INDEX idx_squad_sop_run_squad_created ON public.squad_sop_run USING btree (squad_id, created_at DESC);

CREATE INDEX idx_squad_sop_run_workspace_created ON public.squad_sop_run USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_squad_sop_step_event_issue_created ON public.squad_sop_step_event USING btree (issue_id, created_at DESC);

CREATE INDEX idx_squad_sop_step_event_run_created ON public.squad_sop_step_event USING btree (run_id, created_at);

CREATE INDEX idx_squad_sop_step_event_squad_created ON public.squad_sop_step_event USING btree (squad_id, created_at DESC);

CREATE INDEX idx_squad_workspace ON public.squad USING btree (workspace_id);

CREATE INDEX idx_squad_workspace_scope ON public.squad USING btree (workspace_id, scope) WHERE (archived_at IS NULL);

CREATE INDEX idx_sys_cron_exec_failed_recent ON public.sys_cron_executions USING btree (job_name, plan_time DESC) WHERE (status = 'FAILED'::text);

CREATE INDEX idx_sys_cron_exec_finished ON public.sys_cron_executions USING btree (finished_at) WHERE (status = ANY (ARRAY['SUCCESS'::text, 'FAILED'::text]));

CREATE INDEX idx_sys_cron_exec_job_plan ON public.sys_cron_executions USING btree (job_name, scope_kind, scope_id, plan_time DESC);

CREATE INDEX idx_sys_cron_exec_running_stale ON public.sys_cron_executions USING btree (stale_after) WHERE (status = 'RUNNING'::text);

CREATE INDEX idx_task_message_task_id_seq ON public.task_message USING btree (task_id, seq);

CREATE UNIQUE INDEX idx_task_token_hash ON public.task_token USING btree (token_hash);

CREATE INDEX idx_task_token_task ON public.task_token USING btree (task_id);

CREATE INDEX idx_task_trace_event_agent_created ON public.task_trace_event USING btree (agent_id, created_at DESC);

CREATE INDEX idx_task_trace_event_issue_created ON public.task_trace_event USING btree (issue_id, created_at DESC) WHERE (issue_id IS NOT NULL);

CREATE INDEX idx_task_trace_event_squad_created ON public.task_trace_event USING btree (squad_id, created_at DESC) WHERE (squad_id IS NOT NULL);

CREATE INDEX idx_task_trace_event_task_created ON public.task_trace_event USING btree (task_id, created_at);

CREATE INDEX idx_task_trace_event_workspace_created ON public.task_trace_event USING btree (workspace_id, created_at DESC);

CREATE INDEX idx_task_usage_created_at ON public.task_usage USING btree (created_at);

CREATE INDEX idx_task_usage_created_at_legacy ON public.task_usage USING btree (created_at) WHERE (updated_at IS NULL);

CREATE INDEX idx_task_usage_hourly_dirty_enqueued_at ON public.task_usage_hourly_dirty USING btree (enqueued_at);

CREATE INDEX idx_task_usage_hourly_runtime_time ON public.task_usage_hourly USING btree (runtime_id, bucket_hour DESC);

CREATE INDEX idx_task_usage_hourly_workspace_agent_time ON public.task_usage_hourly USING btree (workspace_id, agent_id, bucket_hour DESC);

CREATE INDEX idx_task_usage_hourly_workspace_project_time ON public.task_usage_hourly USING btree (workspace_id, project_id, bucket_hour DESC) WHERE (project_id IS NOT NULL);

CREATE INDEX idx_task_usage_hourly_workspace_time ON public.task_usage_hourly USING btree (workspace_id, bucket_hour DESC);

CREATE INDEX idx_task_usage_task_id ON public.task_usage USING btree (task_id);

CREATE INDEX idx_task_usage_updated_at ON public.task_usage USING btree (updated_at);

CREATE INDEX idx_user_created_at ON public."user" USING btree (created_at);

CREATE INDEX idx_webhook_delivery_autopilot ON public.webhook_delivery USING btree (autopilot_id, created_at DESC);

CREATE UNIQUE INDEX idx_webhook_delivery_dedupe ON public.webhook_delivery USING btree (trigger_id, dedupe_key) WHERE ((dedupe_key IS NOT NULL) AND (status <> ALL (ARRAY['rejected'::text, 'failed'::text])));

CREATE INDEX idx_webhook_delivery_run ON public.webhook_delivery USING btree (autopilot_run_id) WHERE (autopilot_run_id IS NOT NULL);

CREATE UNIQUE INDEX issue_label_workspace_name_lower_idx ON public.issue_label USING btree (workspace_id, lower(name));

CREATE INDEX prompt_evaluation_asset_prompt_idx ON public.prompt_evaluation_asset USING btree (prompt_id);

CREATE INDEX prompt_evaluation_asset_workspace_type_idx ON public.prompt_evaluation_asset USING btree (workspace_id, asset_type);

CREATE INDEX prompt_library_item_workspace_project_idx ON public.prompt_library_item USING btree (workspace_id, project_id);

CREATE INDEX prompt_library_item_workspace_type_idx ON public.prompt_library_item USING btree (workspace_id, prompt_type);

CREATE TRIGGER trg_atq_dirty_hourly BEFORE DELETE OR UPDATE OF runtime_id, issue_id ON public.agent_task_queue FOR EACH ROW EXECUTE FUNCTION public.enqueue_task_usage_hourly_dirty_for_atq();

CREATE TRIGGER trg_issue_delete_dirty_hourly BEFORE DELETE ON public.issue FOR EACH ROW EXECUTE FUNCTION public.enqueue_task_usage_hourly_dirty_for_issue_delete();

CREATE TRIGGER trg_issue_project_dirty_hourly BEFORE UPDATE OF project_id ON public.issue FOR EACH ROW EXECUTE FUNCTION public.enqueue_task_usage_hourly_dirty_for_issue_project();

CREATE TRIGGER trg_tu_dirty_hourly BEFORE DELETE ON public.task_usage FOR EACH ROW EXECUTE FUNCTION public.enqueue_task_usage_hourly_dirty_for_tu();

ALTER TABLE ONLY public.activity_log
    ADD CONSTRAINT activity_log_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.activity_log
    ADD CONSTRAINT activity_log_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent
    ADD CONSTRAINT agent_archived_by_fkey FOREIGN KEY (archived_by) REFERENCES public."user"(id);

ALTER TABLE ONLY public.agent
    ADD CONSTRAINT agent_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public."user"(id);

ALTER TABLE ONLY public.agent
    ADD CONSTRAINT agent_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES public.agent_runtime(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.agent_runtime
    ADD CONSTRAINT agent_runtime_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public."user"(id);

ALTER TABLE ONLY public.agent_runtime
    ADD CONSTRAINT agent_runtime_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_skill
    ADD CONSTRAINT agent_skill_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_skill
    ADD CONSTRAINT agent_skill_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.skill(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_autopilot_run_id_fkey FOREIGN KEY (autopilot_run_id) REFERENCES public.autopilot_run(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_parent_task_id_fkey FOREIGN KEY (parent_task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES public.agent_runtime(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_task_queue
    ADD CONSTRAINT agent_task_queue_trigger_comment_id_fkey FOREIGN KEY (trigger_comment_id) REFERENCES public.comment(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent
    ADD CONSTRAINT agent_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_chat_message_id_fkey FOREIGN KEY (chat_message_id) REFERENCES public.chat_message(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_comment_id_fkey FOREIGN KEY (comment_id) REFERENCES public.comment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.attachment
    ADD CONSTRAINT attachment_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.autopilot
    ADD CONSTRAINT autopilot_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_autopilot_id_fkey FOREIGN KEY (autopilot_id) REFERENCES public.autopilot(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_squad_id_fkey FOREIGN KEY (squad_id) REFERENCES public.squad(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.autopilot_run
    ADD CONSTRAINT autopilot_run_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES public.autopilot_trigger(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.autopilot_trigger
    ADD CONSTRAINT autopilot_trigger_autopilot_id_fkey FOREIGN KEY (autopilot_id) REFERENCES public.autopilot(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.autopilot
    ADD CONSTRAINT autopilot_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.chat_message
    ADD CONSTRAINT chat_message_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.chat_session
    ADD CONSTRAINT chat_session_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.chat_session
    ADD CONSTRAINT chat_session_creator_id_fkey FOREIGN KEY (creator_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.chat_session
    ADD CONSTRAINT chat_session_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES public.agent_runtime(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.chat_session
    ADD CONSTRAINT chat_session_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.comment
    ADD CONSTRAINT comment_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.comment
    ADD CONSTRAINT comment_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.comment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.comment_reaction
    ADD CONSTRAINT comment_reaction_comment_id_fkey FOREIGN KEY (comment_id) REFERENCES public.comment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.comment_reaction
    ADD CONSTRAINT comment_reaction_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.comment
    ADD CONSTRAINT comment_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.daemon_connection
    ADD CONSTRAINT daemon_connection_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.daemon_token
    ADD CONSTRAINT daemon_token_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.external_credential_profile
    ADD CONSTRAINT external_credential_profile_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.feedback
    ADD CONSTRAINT feedback_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.feedback
    ADD CONSTRAINT feedback_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.github_installation
    ADD CONSTRAINT github_installation_connected_by_id_fkey FOREIGN KEY (connected_by_id) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.github_installation
    ADD CONSTRAINT github_installation_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.github_pull_request_check_suite
    ADD CONSTRAINT github_pull_request_check_suite_pr_id_fkey FOREIGN KEY (pr_id) REFERENCES public.github_pull_request(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.github_pull_request
    ADD CONSTRAINT github_pull_request_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.inbox_item
    ADD CONSTRAINT inbox_item_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.inbox_item
    ADD CONSTRAINT inbox_item_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_dependency
    ADD CONSTRAINT issue_dependency_depends_on_issue_id_fkey FOREIGN KEY (depends_on_issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_dependency
    ADD CONSTRAINT issue_dependency_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_label
    ADD CONSTRAINT issue_label_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT issue_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public."user"(id);

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT issue_parent_issue_id_fkey FOREIGN KEY (parent_issue_id) REFERENCES public.issue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT issue_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.issue_pull_request
    ADD CONSTRAINT issue_pull_request_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_pull_request
    ADD CONSTRAINT issue_pull_request_pull_request_id_fkey FOREIGN KEY (pull_request_id) REFERENCES public.github_pull_request(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_reaction
    ADD CONSTRAINT issue_reaction_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_reaction
    ADD CONSTRAINT issue_reaction_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_subscriber
    ADD CONSTRAINT issue_subscriber_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_to_label
    ADD CONSTRAINT issue_to_label_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue_to_label
    ADD CONSTRAINT issue_to_label_label_id_fkey FOREIGN KEY (label_id) REFERENCES public.issue_label(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.issue
    ADD CONSTRAINT issue_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_binding_token
    ADD CONSTRAINT lark_binding_token_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES public.lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_binding_token
    ADD CONSTRAINT lark_binding_token_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES public.lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_inbound_audit
    ADD CONSTRAINT lark_inbound_audit_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES public.lark_installation(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.lark_inbound_message_dedup
    ADD CONSTRAINT lark_inbound_message_dedup_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES public.lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_installer_user_id_fkey FOREIGN KEY (installer_user_id) REFERENCES public."user"(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.lark_installation
    ADD CONSTRAINT lark_installation_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.lark_user_binding
    ADD CONSTRAINT lark_user_binding_installation_fk FOREIGN KEY (installation_id, workspace_id) REFERENCES public.lark_installation(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE ONLY public.lark_user_binding
    ADD CONSTRAINT lark_user_binding_member_fk FOREIGN KEY (workspace_id, multica_user_id) REFERENCES public.member(workspace_id, user_id) ON DELETE CASCADE;

ALTER TABLE ONLY public.member
    ADD CONSTRAINT member_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.member
    ADD CONSTRAINT member_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.notification_preference
    ADD CONSTRAINT notification_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.notification_preference
    ADD CONSTRAINT notification_preference_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.personal_access_token
    ADD CONSTRAINT personal_access_token_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pinned_item
    ADD CONSTRAINT pinned_item_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pinned_item
    ADD CONSTRAINT pinned_item_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.project
    ADD CONSTRAINT project_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public."user"(id);

ALTER TABLE ONLY public.project_resource
    ADD CONSTRAINT project_resource_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.project_resource
    ADD CONSTRAINT project_resource_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.project
    ADD CONSTRAINT project_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_asset
    ADD CONSTRAINT prompt_evaluation_asset_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_asset
    ADD CONSTRAINT prompt_evaluation_asset_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_asset
    ADD CONSTRAINT prompt_evaluation_asset_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case_assertion
    ADD CONSTRAINT prompt_evaluation_case_assertion_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case_assertion
    ADD CONSTRAINT prompt_evaluation_case_assertion_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.prompt_evaluation_case(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case_assertion
    ADD CONSTRAINT prompt_evaluation_case_assertion_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_case_operation
    ADD CONSTRAINT prompt_evaluation_case_operation_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case_operation
    ADD CONSTRAINT prompt_evaluation_case_operation_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_case_operation
    ADD CONSTRAINT prompt_evaluation_case_operation_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_case
    ADD CONSTRAINT prompt_evaluation_case_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.prompt_evaluation_case(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_dataset_asset_id_fkey FOREIGN KEY (dataset_asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version
    ADD CONSTRAINT prompt_evaluation_dataset_version_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version
    ADD CONSTRAINT prompt_evaluation_dataset_version_dataset_asset_id_fkey FOREIGN KEY (dataset_asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.prompt_evaluation_case(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_dataset_asset_id_fkey FOREIGN KEY (dataset_asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_dataset_version_id_fkey FOREIGN KEY (dataset_version_id) REFERENCES public.prompt_evaluation_dataset_version(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_source_row_id_fkey FOREIGN KEY (source_row_id) REFERENCES public.prompt_evaluation_dataset_row(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version_row
    ADD CONSTRAINT prompt_evaluation_dataset_version_row_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dataset_version
    ADD CONSTRAINT prompt_evaluation_dataset_version_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.prompt_evaluation_run(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_dimension_score
    ADD CONSTRAINT prompt_evaluation_dimension_score_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_evidence_snapshot
    ADD CONSTRAINT prompt_evaluation_evidence_snapshot_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_evidence_snapshot
    ADD CONSTRAINT prompt_evaluation_evidence_snapshot_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.prompt_evaluation_run(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_evidence_snapshot
    ADD CONSTRAINT prompt_evaluation_evidence_snapshot_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candida_published_prompt_id_fkey FOREIGN KEY (published_prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.prompt_evaluation_run(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_optimization_candidate
    ADD CONSTRAINT prompt_evaluation_optimization_candidate_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_reviewed_by_fkey FOREIGN KEY (reviewed_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES public.agent_runtime(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_case_id_fkey FOREIGN KEY (case_id) REFERENCES public.prompt_evaluation_case(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_test_suite_asset_id_fkey FOREIGN KEY (test_suite_asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_test_suite_case
    ADD CONSTRAINT prompt_evaluation_test_suite_case_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_trial
    ADD CONSTRAINT prompt_evaluation_trial_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_trial
    ADD CONSTRAINT prompt_evaluation_trial_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.prompt_evaluation_run(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_evaluation_trial
    ADD CONSTRAINT prompt_evaluation_trial_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_item
    ADD CONSTRAINT prompt_library_item_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_item
    ADD CONSTRAINT prompt_library_item_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_item
    ADD CONSTRAINT prompt_library_item_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_version_id_fkey FOREIGN KEY (version_id) REFERENCES public.prompt_library_version(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_trial
    ADD CONSTRAINT prompt_library_trial_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_dataset_asset_id_fkey FOREIGN KEY (dataset_asset_id) REFERENCES public.prompt_evaluation_asset(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_dataset_version_id_fkey FOREIGN KEY (dataset_version_id) REFERENCES public.prompt_evaluation_dataset_version(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_experiment
    ADD CONSTRAINT agent_playground_experiment_judge_agent_id_fkey FOREIGN KEY (judge_agent_id) REFERENCES public.agent(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_input
    ADD CONSTRAINT agent_playground_input_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_input
    ADD CONSTRAINT agent_playground_input_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_input
    ADD CONSTRAINT agent_playground_input_dataset_row_id_fkey FOREIGN KEY (dataset_row_id) REFERENCES public.prompt_evaluation_dataset_version_row(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_agent
    ADD CONSTRAINT agent_playground_agent_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_agent
    ADD CONSTRAINT agent_playground_agent_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_agent
    ADD CONSTRAINT agent_playground_agent_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_input_id_fkey FOREIGN KEY (input_id) REFERENCES public.agent_playground_input(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_experiment_agent_id_fkey FOREIGN KEY (experiment_agent_id) REFERENCES public.agent_playground_agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_result
    ADD CONSTRAINT agent_playground_result_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES public.agent_playground_experiment(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_input_id_fkey FOREIGN KEY (input_id) REFERENCES public.agent_playground_input(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_judge_agent_id_fkey FOREIGN KEY (judge_agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.agent_playground_judgement
    ADD CONSTRAINT agent_playground_judgement_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_prompt_id_fkey FOREIGN KEY (prompt_id) REFERENCES public.prompt_library_item(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_source_candidate_id_fkey FOREIGN KEY (source_candidate_id) REFERENCES public.prompt_evaluation_optimization_candidate(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.prompt_library_version
    ADD CONSTRAINT prompt_library_version_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.skill
    ADD CONSTRAINT skill_created_by_fkey FOREIGN KEY (created_by) REFERENCES public."user"(id);

ALTER TABLE ONLY public.skill_file
    ADD CONSTRAINT skill_file_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.skill(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.skill
    ADD CONSTRAINT skill_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad
    ADD CONSTRAINT squad_leader_id_fkey FOREIGN KEY (leader_id) REFERENCES public.agent(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.squad_member
    ADD CONSTRAINT squad_member_squad_id_fkey FOREIGN KEY (squad_id) REFERENCES public.squad(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_run
    ADD CONSTRAINT squad_sop_run_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_run
    ADD CONSTRAINT squad_sop_run_leader_task_id_fkey FOREIGN KEY (leader_task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.squad_sop_run
    ADD CONSTRAINT squad_sop_run_squad_id_fkey FOREIGN KEY (squad_id) REFERENCES public.squad(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_run
    ADD CONSTRAINT squad_sop_run_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.squad_sop_run(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_squad_id_fkey FOREIGN KEY (squad_id) REFERENCES public.squad(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.squad_sop_step_event
    ADD CONSTRAINT squad_sop_step_event_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.squad
    ADD CONSTRAINT squad_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_message
    ADD CONSTRAINT task_message_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_token
    ADD CONSTRAINT task_token_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_token
    ADD CONSTRAINT task_token_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_token
    ADD CONSTRAINT task_token_user_id_fkey FOREIGN KEY (user_id) REFERENCES public."user"(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_token
    ADD CONSTRAINT task_token_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_autopilot_run_id_fkey FOREIGN KEY (autopilot_run_id) REFERENCES public.autopilot_run(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES public.chat_session(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_issue_id_fkey FOREIGN KEY (issue_id) REFERENCES public.issue(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.project(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_runtime_id_fkey FOREIGN KEY (runtime_id) REFERENCES public.agent_runtime(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_squad_id_fkey FOREIGN KEY (squad_id) REFERENCES public.squad(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_trigger_comment_id_fkey FOREIGN KEY (trigger_comment_id) REFERENCES public.comment(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.task_trace_event
    ADD CONSTRAINT task_trace_event_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.task_usage
    ADD CONSTRAINT task_usage_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.agent_task_queue(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_autopilot_id_fkey FOREIGN KEY (autopilot_id) REFERENCES public.autopilot(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_autopilot_run_id_fkey FOREIGN KEY (autopilot_run_id) REFERENCES public.autopilot_run(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_replayed_from_delivery_id_fkey FOREIGN KEY (replayed_from_delivery_id) REFERENCES public.webhook_delivery(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_trigger_id_fkey FOREIGN KEY (trigger_id) REFERENCES public.autopilot_trigger(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.webhook_delivery
    ADD CONSTRAINT webhook_delivery_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspace(id) ON DELETE CASCADE;

-- Required current-schema seed rows for an empty development database.
INSERT INTO public.task_usage_hourly_rollup_state (id) VALUES (1) ON CONFLICT DO NOTHING;

SELECT pg_catalog.set_config('search_path', 'public', false);
