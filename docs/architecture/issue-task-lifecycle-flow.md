# Issue, comment and task lifecycle

## Ownership and supported writes

HTTP validation and response assembly live in the Issue and Comment handlers.
The database is the source of truth; React Query owns the corresponding client
state and realtime events only invalidate or reconcile that cache.

- `POST /api/issues` carries one UUIDv4 request identity from the Core pending
  operation through the HTTP boundary. The Issue row, attachment claims,
  assignment/approval projection, outbox event and exact `201` response are
  prepared and committed with the shared `resource_create_request` row. A lost
  response is replayed byte-for-byte; changed input under the same key is 409.
  `PUT /api/issues/{id}` retains its resource-identity idempotence. Both paths
  validate workspace-scoped assignees, dates and parent/project references.
- `POST /api/issues/{id}/comments` commits the comment, attachment claims,
  thread reopening and `comment:created` outbox event together.
- Batch update is intentionally per-item, not all-or-nothing. Every item is
  accounted for as updated, child-blocked or failed with a stable code.
- Single and batch delete share `deleteIssueAtomically`: active Task
  cancellation plus its terminal outbox events, linked Autopilot failure and
  the Issue delete commit together. Object-storage deletion and realtime
  notification happen after commit.

## Task lifecycle

Daemon lifecycle routes delegate to `TaskService`; handlers do not reproduce
state transitions.

- Start commits Task `running`, automatic Issue `in_progress`, Squad SOP start
  event and current Run stage in one transaction. The open SOP Run is locked.
- Complete/fail/cancel commit the terminal Task row, revoked task credential,
  retry decision, terminal domain event and applicable Issue/Squad projections
  through the service transaction paths.
- Terminal domain events feed receipt-tracked consumers for activity, audience,
  notifications, Chat, quick-create, Autopilot and Prompt Evaluation. A failed
  consumer rolls back only its projection, retains the business commit and is
  retried by the dispatcher.

Post-commit trace metrics, daemon wakeups and WebSocket publication are hints,
not authoritative state. Consumers validate workspace and row identity before
projecting an event; deletion after the primary commit is an explicit no-op
rather than an endless foreign-key retry.

## Verification anchors

- Issue writes and shared atomic delete: `server/internal/handler/issue.go`
- Issue request/result recovery and transaction boundary:
  `server/internal/handler/issue_idempotency.go` and
  `server/internal/service/issue.go`
- Per-item batch results: `server/internal/handler/issue_batch.go`
- Comment transaction: `server/internal/handler/comment.go`
- Task start/terminal services: `server/internal/service/task_claim.go`,
  `server/internal/service/task_complete.go` and
  `server/internal/service/task_fail.go`
- Durable consumer routing: `server/cmd/server/activity_listeners.go`,
  `server/cmd/server/subscriber_listeners.go` and
  `server/cmd/server/task_projection.go`
- Rollback and truthful-partial regressions:
  `server/internal/handler/issue_delete_atomicity_test.go` and
  `server/internal/handler/issue_batch_test.go`
