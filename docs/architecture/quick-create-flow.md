# Quick Create flow

The Agent Create panel is the current Issue creation UI. It calls
`quickCreateIssueWithRecovery`, which persists the exact request and UUIDv4
identity in the workspace/account-scoped Quick Create store before calling
`POST /api/issues/quick-create`. Core retries one unknown transport or response
outcome with the same identity; if the outcome remains unknown, a reload and
later submit recover the original intent before accepting a new one.

The handler validates actor visibility, runtime readiness, project, parent,
assignee, dates and attachments before reserving
`resource_create_request[type=quick_create]`. The request identity then spans
both current execution branches:

- Agent/Squad prompts use the UUID as `agent_task_queue.id`; the task context
  stores the request hash, requester and workspace. A crash after task commit
  but before response completion recovers that exact queued task.
- TAPD source URLs use the UUID as Issue `origin_id` with
  `origin_type=quick_create`; the request hash is stored in validated metadata.
  A workspace-scoped unique origin index prevents concurrent duplicate Issues.
  Recovery returns the existing Issue without fetching TAPD again.

The exact 202 Task response or 201 Issue response is completed in the durable
request record. Same-key different requests return 409. Concurrent same-key
calls serialize on the request reservation and emit only one Task or Issue.

Verification anchors:

- Persisted client operation: `packages/core/issues/quick-create.ts` and
  `packages/core/issues/stores/quick-create-store.ts`.
- HTTP orchestration and recovery: `server/internal/handler/issue_quick_create.go`
  and `server/internal/handler/quick_create_idempotency.go`.
- Deterministic task: `server/internal/service/task_enqueue.go` and
  `server/pkg/db/queries/agent.sql`.
- Durable schema: `server/migrations/001_current_schema.up.sql`.
- PostgreSQL/concurrency regressions:
  `server/internal/handler/quick_create_idempotency_test.go` and
  `server/internal/handler/quick_create_parent_test.go`.
