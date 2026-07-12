# Durable resource-create recovery

Current first-party create operations use a UUIDv4 request identity and the
shared `resource_create_request` state machine. The key is scoped by workspace,
actor and resource type; the SHA-256 request fingerprint rejects key reuse with
different input. A completed row stores the resource ID and exact response.

The current resource types are Workspace, Workspace Member, Project, Squad,
Agent, Skill, Attachment, Quick Create, Issue, Comment, Prompt Library Item,
Prompt Library Version and Prompt Library Trial. Each handler either commits
the request row in the same transaction as the resource or uses a deterministic
resource identity and an explicit recovery path when an external/object
operation prevents one DB transaction. Core retries unknown outcomes with the
same key and persists the exact pending request in workspace/account-scoped
storage.

Workspace creation happens before a workspace scope exists. Its request UUID is
therefore also the new Workspace UUID; Workspace, owner membership, request row
and exact `201` response commit together. The account-scoped Core intent
survives reload without leaking into workspace-scoped storage. Deleting the
Workspace cascades its recovery witness, matching the resource lifecycle.

Workspace Member creation is restricted to owners and admins. For a new
account, User creation, password hash, Member row and exact `201` response share
one transaction; a failed membership insert cannot leave a login-capable
orphan User. The request UUID is also the Member UUID, so Core can recover it
from the authorized member list after a reload. Because the request may contain
an initial password, browser storage keeps only the UUID, a fingerprint of the
non-secret intent and whether a password was supplied; it never stores the
password or a password-derived value.

External credential profiles use an account-scoped variant because they are
not workspace resources and their request may contain a secret. The request
UUID is also the profile UUID, while the profile row stores only the UUID and a
SHA-256 request fingerprint alongside the encrypted secret or server-side
secret reference. Core persists only that UUID, fingerprint and timestamp;
raw tokens are never written to browser storage. After a reload, Core recovers
the user-scoped profile by UUID before accepting another create. Exact retries
return the same redacted `201` response, changed input with the same key is
rejected, and concurrent retries are constrained by `(user_id,
idempotency_key)`.

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
