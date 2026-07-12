# Durable resource-create recovery

Current first-party create operations use a UUIDv4 request identity and the
shared `resource_create_request` state machine. The key is scoped by workspace,
actor and resource type; the SHA-256 request fingerprint rejects key reuse with
different input. A completed row stores the resource ID and exact response.

The current resource types are Workspace, Project, Squad, Agent, Skill,
Attachment, Quick Create, Issue, Comment, Prompt Library Item, Prompt Library
Version and Prompt Library Trial. Each handler either commits the request row in the
same transaction as the resource or uses a deterministic resource identity and
an explicit recovery path when an external/object operation prevents one DB
transaction. Core retries unknown outcomes with the same key and persists the
exact pending request in workspace/account-scoped storage.

Workspace creation happens before a workspace scope exists. Its request UUID is
therefore also the new Workspace UUID; Workspace, owner membership, request row
and exact `201` response commit together. The account-scoped Core intent
survives reload without leaking into workspace-scoped storage. Deleting the
Workspace cascades its recovery witness, matching the resource lifecycle.

Prompt Library Item atomically commits the item, initial version and exact `201`
response. Prompt Library Version atomically updates the current item, inserts
one version and records the exact `201` response. Prompt Library Trial uses the
same contract for its compound execution create:
Chat Session, user Message, Agent Task, Trial row and exact `202` response commit
together; only the winning request publishes the post-commit task wake-up. Core
persists pending Item, Version and Trial intents in one workspace/account-scoped
Prompt Library create store and always recovers the older intent before sending
a changed one.

## Retention contract

First-party pending operations live for at most 30 days. The DB-backed global
scheduler runs `prune_resource_create_requests` hourly and retains completed
responses for 31 days, leaving a one-day clock/scheduling margin. Each run uses
`FOR UPDATE SKIP LOCKED` batches and deletes at most 20,000 completed rows, so
normal writes are not held behind a table-wide cleanup.
Migration 054 supplies partial indexes for the completed expiry scan and the
incomplete-age diagnostic; retention must not fall back to a growing full-table
scan.

Incomplete rows are never deleted automatically: they may be the only witness
of an interrupted operation. Rows older than 31 days are counted in the
scheduler result as `stale_incomplete` and emit a warning. Hitting the per-run
delete ceiling is recorded as `batch_limit_reached` and also emits a warning.
Operators can inspect the durable audit trail with:

```sql
SELECT status, rows_affected, result, error_code, error_message, finished_at
FROM sys_cron_executions
WHERE job_name = 'prune_resource_create_requests'
ORDER BY plan_time DESC
LIMIT 24;
```

The implementation lives in
`server/internal/scheduler/jobs_resource_create_retention.go`; request queries
live in `server/pkg/db/queries/resource_create_request.sql`.
