# Durable resource-create recovery

Current first-party creates use a UUIDv4 request identity and the shared
`resource_create_request` state machine. The key is scoped by workspace, actor
and resource type; a SHA-256 fingerprint rejects changed input. A completed row
stores the resource ID and exact response.

## Ownership

- HTTP reservation, replay and conflict mapping:
  `server/internal/handler/resource_create_idempotency.go`.
- Queries and retention scan:
  `server/pkg/db/queries/resource_create_request.sql`.
- Current schema and indexes: `server/migrations/001_current_schema.up.sql`.
- Client operations persist only the exact non-secret request and UUID needed
  to recover an unknown outcome. Secret-bearing member and credential creates
  persist a UUID/fingerprint, never the raw password or token.

## Resource families

| Family | Atomic or deterministic result |
| --- | --- |
| Workspace and Member | Workspace/owner or User/password hash/Member commit with the response; the request UUID is the created identity |
| Project, Squad, Agent and Skill | Resource, initial children/status/files and exact response commit together |
| Issue and Comment | Primary row, attachment claims, task/event projection and exact response commit together |
| Attachment | Deterministic object identity plus database reconciliation and compensation around external storage |
| Quick Create | Request UUID is the Task identity or TAPD-backed Issue origin |
| External credential | Account-scoped UUID/fingerprint witness commits with encrypted or server-side secret ownership |

Every repository-owned caller reuses the same key after network failure,
invalid successful JSON or reload. A definitive rejection releases the pending
client operation. A replay returns the stored response and does not republish a
create event.

## Retention

Client pending operations expire after 30 days. The in-process scheduler runs
`prune_resource_create_requests` hourly and deletes completed witnesses older
than 31 days in `FOR UPDATE SKIP LOCKED` batches of at most 20,000 rows. The
one-day margin prevents a still-recoverable client request from losing its
server witness.

Incomplete rows are not deleted automatically because they may be the only
evidence of an interrupted operation. The job reports old incomplete rows and
batch-limit hits through `sys_cron_executions`.

Implementation: `server/internal/scheduler/jobs_resource_create_retention.go`.
Cross-domain behavior is verified by the handler idempotency/concurrency tests
and the current-system map gate.
