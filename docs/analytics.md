# Product Analytics

This document is the source of truth for the analytics events Multica ships
to PostHog. Events feed the acquisition → activation → expansion funnel that
drives our weekly Active Workspaces (WAW) north-star metric.

> **PostHog is reserved for user/product-behaviour events.** High-volume
> operational / execution-lifecycle telemetry — runtime lifecycle
> (`runtime_registered` / `runtime_ready` / `runtime_failed` /
> `runtime_offline`), the agent task lifecycle (`agent_task_*`), and autopilot
> run lifecycle (`autopilot_run_started` / `autopilot_run_completed` /
> `autopilot_run_failed`) — is **Prometheus-only** and is **not** shipped to
> PostHog. Grafana already covers it and the per-event PostHog ingestion cost
> (these events dominate volume and bill at the identified-event rate) is not
> justified. The runtime/autopilot events are flagged by
> `analytics.IsMetricsOnly`, which `metrics.RecordEvent` consults to skip the
> PostHog `Capture` while still incrementing the Prometheus counter; the
> `agent_task_*` lifecycle is recorded straight to Prometheus via the typed
> `BusinessMetrics.RecordTask*` methods and has no `analytics.Event` at all.

## Configuration

All analytics shipping is toggled by environment variables (see `.env.example`):

| Variable | Meaning | Default |
|---|---|---|
| `POSTHOG_API_KEY` | PostHog project API key. Empty = no events are shipped. | `""` |
| `POSTHOG_HOST` | PostHog host (US or EU cloud, or self-hosted URL). | `https://us.i.posthog.com` |
| `ANALYTICS_ENVIRONMENT` | Optional override for the standard `environment` event property. Normalized to `production`, `staging`, or `dev`; defaults from `APP_ENV`. | `APP_ENV` / `dev` |
| `ANALYTICS_DISABLED` | Set to `true`/`1` to force the no-op client even when `POSTHOG_API_KEY` is set. | `""` |

Local dev and self-hosted instances run with `POSTHOG_API_KEY=""`, so **no
events leave the process unless the operator explicitly opts in**.

### Self-hosted instances

Self-hosters should **never inherit a Multica-issued `POSTHOG_API_KEY`** —
that would route their users' behavior to our analytics project. The
defaults guarantee this:

- `.env.example` ships `POSTHOG_API_KEY=` empty. The Docker self-host
  compose does not set a default either.
- With the key unset, `NewFromEnv` returns `NoopClient` and logs
  `analytics: POSTHOG_API_KEY not set, using noop client` at startup — a
  visible confirmation that nothing is shipped.
- Operators who want their own analytics can set `POSTHOG_API_KEY` and
  `POSTHOG_HOST` to point at their own PostHog project (Cloud or
  self-hosted PostHog).
- The frontend receives the key via `/api/config`, so self-hosts' blank server
  config also disables frontend event shipping automatically.

## Architecture

```
handler/service → metrics.RecordEvent
                         │
             ┌───────────┴───────────┐
             ▼                       ▼
       Prometheus counter     analytics.Client.Capture
                                     │ product events only
                                     ▼
                           bounded queue → POST /batch/
```

- `analytics.Capture` is **never allowed to block a request handler**. A
  broken backend must not degrade the product — when the queue is full,
  events are dropped and counted (visible via `slog` + the `dropped` counter
  on shutdown).
- Batches flush either when `BatchSize` is reached or every `FlushEvery`
  (default 10 s), whichever comes first.
- `Close()` drains remaining events during graceful shutdown. Called from
  `server/cmd/server/main.go` via `defer`.

## Identity model

- **`distinct_id`** — always the user's UUID for logged-in events. The
  frontend's `posthog.identify(user.id)` keeps pre-login pageviews and later
  authenticated events on one person.
- **`workspace_id`** — added to every workspace-scoped event. Dashboards use
  the event property directly for workspace-level metrics.
- **`user_id`** — added to event properties for authenticated human users.
  It is available for individual debugging but may be absent from agent or
  system events.
- **Initial person properties (`$set_once`)** — use for immutable acquisition
  attribution so later events cannot overwrite the user's origin.

## Taxonomy

Every event is assigned to one dashboard category:

| Category | Events |
|---|---|
| `core_loop` | `workspace_created`, `agent_created`, `issue_created`, `chat_message_sent`, `issue_executed`, `autopilot_created`, `squad_created` |
| `acquisition` | `signup` |
| `ops_feedback` | `feedback_opened`, `feedback_submitted` |
| `system/noise` | `$pageview`, `$set`, `$identify`, `$autocapture`, `$rageclick` |
| `operational (Prometheus-only — NOT in PostHog)` | Runtime and Autopilot lifecycle events listed below; Agent task lifecycle uses typed `multica_agent_task_*` metrics rather than event names. |

The core product dashboard uses only `core_loop`. Acquisition, feedback, and
system/noise events stay in separate dashboards. The
`operational` row is **not shipped to PostHog** — those signals live in
Grafana via `multica_*` business counters (see `server/internal/metrics`).

## Standard core properties

Canonical core events should carry these properties whenever the entity exists:

| Property | Type | Notes |
|---|---|---|
| `environment` | string | `production` / `staging` / `dev`; stamped by backend and frontend analytics clients. |
| `event_schema_version` | int | Current version: `2`. |
| `user_id` | string UUID | Human user ID when known. Agent/system events may omit it. |
| `workspace_id` | string UUID | Required for workspace-scoped events. |
| `agent_id` | string UUID | Required for agent/task events. |
| `task_id` | string UUID | Required for `agent_task_*` events. |
| `issue_id` / `chat_session_id` / `autopilot_run_id` | string UUID | Relevant source entity for the task/entry event. |
| `source` | string | Canonical values: `manual`, `chat`, `autopilot`, `api`, `ops_feedback`. UI surface details use `surface` or `trigger_source`. |
| `runtime_mode` | string | `cloud` / `local` when a runtime/agent task is involved. |
| `provider` | string | `claude`, `codex`, `cursor`, etc. when a runtime/agent task is involved. |
| `is_demo` | bool | Currently always `false`; reserved for future demo/test workspace filtering. |

Prometheus-only runtime and autopilot events carry only the properties read by
their metric dispatcher. They are not part of the PostHog property contract.

## Event contract

### `signup`

Fires when a new user is created through the internal account/password model.
The internal-team build intentionally does not collect marketing attribution
or external-source fields.

Person properties set with `$set_once`:

| Property | Type | Description |
|---|---|---|
| `account` | string | Internal account name used for login. |

### `workspace_created`

Fires after a `CreateWorkspace` transaction commits successfully.

| Property | Type | Description |
|---|---|---|
| `workspace_id` | string (UUID) | Added globally; present here for clarity. |

First-workspace segmentation is derived from event order in PostHog; the event
does not carry a race-prone `is_first_workspace` property.

### Runtime lifecycle (Prometheus-only)

These events pass through `metrics.RecordEvent` but are not shipped to PostHog.

| Event | When recorded | Labels / measurement |
|---|---|---|
| `runtime_registered` | First insert of a `(workspace_id, daemon_id, provider)` runtime; reconnect updates do not emit. | `runtime_mode`, `provider` |
| `runtime_ready` | A new runtime is registered online. | `runtime_mode`, `provider`; positive `ready_duration_ms` feeds the readiness histogram |
| `runtime_failed` | Registration persistence fails before readiness. | `runtime_mode`, `provider`, `failure_reason`, `recoverable` |
| `runtime_offline` | Explicit deregistration or stale-runtime sweep. | `runtime_mode`, `provider` |

### `issue_created`

Fires after an issue row is created, including manual UI/API issue creation,
quick-create issue creation by an agent, and autopilot `create_issue` runs.

| Property | Type | Description |
|---|---|---|
| `issue_id` | string (UUID) | Created issue. |
| `agent_id` | string (UUID) | Agent assignee or creating agent when applicable. |
| `task_id` | string (UUID) | Present for quick-create issue creation. |
| `autopilot_run_id` | string (UUID) | Present for autopilot-created issues. |
| `source` | string | `manual`, `api`, or `autopilot`. |

### `chat_message_sent`

Fires after a user chat message is persisted and the corresponding agent task
is queued.

| Property | Type | Description |
|---|---|---|
| `chat_session_id` | string (UUID) | Chat session. |
| `task_id` | string (UUID) | Queued agent task. |
| `agent_id` | string (UUID) | Chat agent. |
| `source` | string | Always `chat`. |

### Agent task lifecycle (Prometheus-only)

> **Not shipped to PostHog and has no `analytics.Event`.** The agent task
> lifecycle is recorded directly to Prometheus by the typed
> `BusinessMetrics.RecordTask*` methods. The lifecycle is intentionally absent
> from the PostHog event contract; high-cardinality task and entity IDs are not
> Prometheus labels and must not be used in Grafana reconciliation.

The actual metrics (defined in `server/internal/metrics/business.go`; label
sets in `server/internal/metrics/labels.go`):

| Metric | Type | Labels |
|---|---|---|
| `multica_agent_task_enqueued_total` | counter | `source`, `runtime_mode` |
| `multica_agent_task_dispatched_total` | counter | `source`, `runtime_mode` |
| `multica_agent_task_started_total` | counter | `source`, `runtime_mode`, `provider` |
| `multica_agent_task_terminal_total` | counter | `source`, `runtime_mode`, `terminal_status` |
| `multica_agent_task_failed_total` | counter | `source`, `runtime_mode`, `failure_reason` |
| `multica_agent_task_queue_wait_seconds` | histogram | `source`, `runtime_mode` |
| `multica_agent_task_run_seconds` | histogram | `source`, `runtime_mode`, `terminal_status` |
| `multica_agent_task_total_seconds` | histogram | `source`, `runtime_mode`, `terminal_status` |

- `terminal_status` is the task's final `agent_task_queue.status` —
  `completed` / `failed` / `cancelled`. There is **no** separate
  completed/cancelled metric: all three land on
  `multica_agent_task_terminal_total{terminal_status=…}`. Failures
  additionally increment `multica_agent_task_failed_total` carrying the coarse
  `failure_reason` (`agent_task_queue.failure_reason`, default `agent_error`).
- Task wall-clock lives in the `*_seconds` histograms (queue wait / run /
  total), replacing the old `duration_ms` event property.
- `source` / `runtime_mode` / `provider` are the normalized label values
  (`NormalizeTaskSource` / `NormalizeRuntimeMode` / `NormalizeRuntimeProvider`).

### `autopilot_run_started` / `autopilot_run_completed` / `autopilot_run_failed`

> **Prometheus-only — not shipped to PostHog.** The `analytics.*` constructors
> exist only so `metrics.IncForEvent` can derive the Prometheus counter;
> `analytics.IsMetricsOnly` keeps them out of PostHog.

Fires from `autopilot_run` lifecycle changes. The run source supplies the
`cadence` and `trigger_kind` labels; terminal events additionally use the fixed
`completed` or `failed` label selected by the event name.

### `issue_executed`

Fires **at most once per issue** — when the first task on that issue
reaches terminal `done` state. Backed by an atomic
`UPDATE issue SET first_executed_at = now() WHERE id = $1 AND first_executed_at IS NULL RETURNING *`;
retries, re-assignments, and comment-triggered follow-up tasks all hit the
WHERE clause and no-op, so the `≥1 / ≥2 / ≥5 / ≥10` funnel buckets count
distinct issues, not tasks.

| Property | Type | Description |
|---|---|---|
| `issue_id` | string (UUID) | |
| `task_id` | string (UUID) | Completing task. |
| `agent_id` | string (UUID) | Completing agent. |
| `source` | string | `manual`, `chat`, or `autopilot`. |
| `runtime_mode` | string | `local` / `cloud`. |
| `provider` | string | Runtime provider. |
| `task_duration_ms` | int64 | Wall-clock time between `task.started_at` and `task.completed_at`. Zero when the task was created in a completed state (rare). |

`distinct_id` prefers the issue's human creator so agent-executed events
flow into the issue-author's person profile (same place `signup` and
`workspace_created` land). Agent-created issues prefix with `agent:` to
keep PostHog from merging the agent into a user record.

Workspace ordinals are derived from event order in PostHog rather than emitted
with a race-prone counter. Per-task completion counts live in Grafana via
`BusinessMetrics.RecordTaskTerminal`; `issue_executed` is the PostHog
activation signal.

### `agent_created`

Fires on every successful `POST /api/agents`. The
`is_first_agent_in_workspace` property distinguishes the first agent from
later additions.

| Property | Type | Description |
|---|---|---|
| `agent_id` | string (UUID) | |
| `provider` | string | Runtime provider the agent is bound to (`claude`, `codex`, etc). |
| `runtime_mode` | string | Runtime mode copied from the bound runtime. |
| `template` | string | Template slug used to seed the agent (`coding` / `planning` / `writing` / `assistant`). Empty when the caller didn't come from a template picker. |
| `is_first_agent_in_workspace` | bool | `true` when the workspace had zero agents before this insert. |

`distinct_id` is the authenticated owner's user id.

### `squad_created`

Fires after a Squad is created.

| Property | Type | Description |
|---|---|---|
| `squad_id` | string (UUID) | Created Squad. |
| `member_count` | int64 | Members seeded at creation time. |
| `source` | string | Always `manual`. |

### `autopilot_created`

Fires after an Autopilot is created.

| Property | Type | Description |
|---|---|---|
| `autopilot_id` | string (UUID) | Created Autopilot. |
| `cadence` | string | Persisted cadence. |
| `trigger_kind` | string | Initial `schedule`, `webhook`, or `manual` trigger; schedule wins when both schedule and webhook are seeded. |
| `source` | string | Always `manual`. |

### `feedback_submitted`

Fires from `CreateFeedback` after the `feedback` row is inserted and the
hourly per-user rate-limit check has passed. Retries within the same hour
that were rate-limited (429) don't emit. The free-text message is stored
in the DB and never broadcast.

| Property | Type | Description |
|---|---|---|
| `message_length_bucket` | string | `0-100` / `100-500` / `500-2000` / `2000+` — coarse bucket of `len(message)` so we can tell "quick note" from "bug report with repro steps" without leaking content. |
| `has_images` | bool | `true` when the markdown contains at least one `![...](url)` image reference — signals bug reports with visual evidence. |
| `kind` | string | Current feedback category when the client supplies one. |
| `platform` | string | Client platform from `X-Client-Platform` header (`web` / `desktop`). Omitted when the header is absent. |
| `app_version` | string | Client version from `X-Client-Version` header. Omitted when absent. |

`distinct_id` is the submitter's user id; `workspace_id` is attached from
the modal's current-workspace context and may be empty when feedback is
sent from a pre-workspace surface.

### Frontend-only events

- `$pageview` is emitted by the web and Desktop root trackers. Automatic
  PostHog pageview capture is disabled. `capturePageview` strips query/hash,
  collapses resource IDs to section paths and deduplicates consecutive views
  of the same section.
- `feedback_opened` fires when the Feedback modal mounts. It carries
  `source=help_menu` and the current `workspace_id` when available.
- Marketing attribution, anonymous-source cookies and acquisition prompts are
  not part of the current internal-team product.

## Reconciliation

Per-task completion is no longer shipped to PostHog. Task success now
reconciles **DB ↔ Prometheus** instead of DB ↔ PostHog: the
`BusinessMetrics.RecordTaskTerminal` counter (exported as a `multica_*` task
metric) should track the operational source of truth:

```sql
SELECT date_trunc('day', completed_at AT TIME ZONE 'UTC') AS day,
       count(*) AS db_completed_tasks
FROM agent_task_queue
WHERE status = 'completed'
  AND completed_at >= now() - interval '30 days'
GROUP BY 1
ORDER BY 1;
```

Compare against the equivalent Prometheus counter in Grafana. The expected
difference should be near zero; sustained drift means either an emission site
is missing or the metrics pipeline is unhealthy.

On the PostHog side, `issue_executed` remains the product-level success signal
(at most one per issue) and can be reconciled against
`issue.first_executed_at` if needed.

## Governance

Before adding, renaming, or removing any event:

1. Update this document first.
2. Update `server/internal/analytics/events.go` constants and helpers to
   match.
3. PR description must state which existing funnel / insight is affected.
