-- Canonicalize the provider dimension once so every usage query can group by
-- the stored value directly. Summing case variants preserves the result the
-- previous LOWER(provider) projections exposed.
DO $$
BEGIN
    CREATE TEMP TABLE normalized_task_usage ON COMMIT DROP AS
    SELECT (array_agg(id ORDER BY created_at, id))[1] AS id,
           task_id, lower(btrim(provider)) AS provider, model,
           sum(input_tokens)::bigint AS input_tokens,
           sum(output_tokens)::bigint AS output_tokens,
           sum(cache_read_tokens)::bigint AS cache_read_tokens,
           sum(cache_write_tokens)::bigint AS cache_write_tokens,
           min(created_at) AS created_at, max(updated_at) AS updated_at
    FROM task_usage
    GROUP BY task_id, lower(btrim(provider)), model;

    CREATE TEMP TABLE normalized_task_usage_hourly ON COMMIT DROP AS
    SELECT bucket_hour, workspace_id, runtime_id, agent_id, project_id,
           lower(btrim(provider)) AS provider, model,
           sum(input_tokens)::bigint AS input_tokens,
           sum(output_tokens)::bigint AS output_tokens,
           sum(cache_read_tokens)::bigint AS cache_read_tokens,
           sum(cache_write_tokens)::bigint AS cache_write_tokens,
           sum(task_count)::bigint AS task_count,
           sum(event_count)::bigint AS event_count,
           max(updated_at) AS updated_at
    FROM task_usage_hourly
    GROUP BY bucket_hour, workspace_id, runtime_id, agent_id, project_id,
             lower(btrim(provider)), model;

    CREATE TEMP TABLE normalized_task_usage_hourly_dirty ON COMMIT DROP AS
    SELECT bucket_hour, workspace_id, runtime_id, agent_id, project_id,
           lower(btrim(provider)) AS provider, model,
           max(enqueued_at) AS enqueued_at
    FROM task_usage_hourly_dirty
    GROUP BY bucket_hour, workspace_id, runtime_id, agent_id, project_id,
             lower(btrim(provider)), model;

    DELETE FROM task_usage;
    INSERT INTO task_usage (
        id, task_id, provider, model, input_tokens, output_tokens,
        cache_read_tokens, cache_write_tokens, created_at, updated_at
    )
    SELECT id, task_id, provider, model, input_tokens, output_tokens,
           cache_read_tokens, cache_write_tokens, created_at, updated_at
    FROM normalized_task_usage;

    DELETE FROM task_usage_hourly;
    INSERT INTO task_usage_hourly (
        bucket_hour, workspace_id, runtime_id, agent_id, project_id, provider,
        model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
        task_count, event_count, updated_at
    )
    SELECT bucket_hour, workspace_id, runtime_id, agent_id, project_id, provider,
           model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
           task_count, event_count, updated_at
    FROM normalized_task_usage_hourly;

    DELETE FROM task_usage_hourly_dirty;
    INSERT INTO task_usage_hourly_dirty (
        bucket_hour, workspace_id, runtime_id, agent_id, project_id,
        provider, model, enqueued_at
    )
    SELECT bucket_hour, workspace_id, runtime_id, agent_id, project_id,
           provider, model, enqueued_at
    FROM normalized_task_usage_hourly_dirty;
END
$$;

ALTER TABLE task_usage
    ADD CONSTRAINT task_usage_provider_canonical
    CHECK (provider = lower(btrim(provider)));
ALTER TABLE task_usage_hourly
    ADD CONSTRAINT task_usage_hourly_provider_canonical
    CHECK (provider = lower(btrim(provider)));
ALTER TABLE task_usage_hourly_dirty
    ADD CONSTRAINT task_usage_hourly_dirty_provider_canonical
    CHECK (provider = lower(btrim(provider)));
