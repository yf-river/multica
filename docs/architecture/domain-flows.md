# Current domain ownership

This is the maintained entry point for Multica's high-risk business flows. It
names the current route, state owner, transaction or external boundary, and the
smallest useful verification anchor. The drift-checked surface inventory lives
in [current-system-summary.md](./current-system-summary.md); shared create
identity and retention live in
[resource-create-recovery.md](./resource-create-recovery.md).

## Shared rules

- React Query owns server state. Workspace/account-scoped Zustand stores own
  drafts and unresolved client operations, never copies of server records.
- A mutation with an unknown outcome is retried only with the same durable
  request identity and exact request.
- Database state and its replay witness commit together. WebSocket, analytics,
  daemon wake-ups and trace metrics are post-commit hints.
- Handlers own HTTP validation and response projection; services own reusable
  state transitions; sqlc queries own persistence statements.
- Provider, object-storage and filesystem calls are explicit external
  boundaries. They are not converted into false success.

## Create and upload flows

### Chat

- Entry: `POST /api/chat/sessions` then
  `POST /api/chat/sessions/{sessionId}/messages`.
- Owners: `packages/core/chat/pending-operation-store.ts`,
  `server/internal/handler/chat.go`, and
  `server/internal/service/task_enqueue.go`.
- Boundary: the message, task, attachment links and exact response commit in
  one transaction; post-commit events never create a second message.
- Verification: `server/internal/handler/chat_idempotency_integration_test.go`
  and `packages/core/chat/pending-operation-store.test.ts`.

### Attachment

- Entry: `POST /api/upload-file` through `packages/core/api/client.ts`.
- Owners: `server/internal/handler/file.go` and
  `server/internal/handler/resource_create_idempotency.go`.
- Boundary: the content fingerprint and request identity serialize external
  object upload; rollback compensates the object, while an unknown commit is
  reconciled before deletion.
- Verification: `server/internal/handler/file_test.go` and
  `server/migrations/001_current_schema.up.sql`.

### Quick Create

- Entry: `POST /api/issues/quick-create`.
- Owners: `packages/core/issues/quick-create.ts`,
  `packages/core/issues/stores/quick-create-store.ts`,
  `server/internal/handler/issue_quick_create.go`, and
  `server/internal/handler/quick_create_idempotency.go`.
- Boundary: Agent/Squad requests use the request UUID as the Task identity;
  TAPD requests use it as the Issue origin. `server/internal/service/task_enqueue.go`
  and `server/pkg/db/queries/agent.sql` materialize the single result.
- Verification: `server/internal/handler/quick_create_idempotency_test.go` and
  `server/migrations/001_current_schema.up.sql`.

### Project

- Entry: `POST /api/projects`.
- Owners: `packages/core/projects/mutations.ts`,
  `packages/views/modals/create-project.tsx`, and
  `server/internal/handler/project.go`.
- Boundary: Project, bundled resources and exact response commit with
  `server/pkg/db/queries/resource_create_request.sql`; Web and CLI use
  this route.
- Verification: `server/internal/handler/project_idempotency_test.go`.

### Autopilot

- Entries: `POST /api/autopilots`, `POST /api/autopilots/{id}/trigger`, and
  `POST /api/webhooks/autopilots/{token}`.
- Owners: `packages/core/autopilots/mutations.ts`,
  `packages/core/autopilots/pending-operation-store.ts`,
  `server/internal/handler/autopilot.go`,
  `server/internal/handler/autopilot_triggers.go`, and
  `server/internal/service/autopilot.go`.
- Boundary: create commits its first trigger atomically. Schedule, webhook and
  manual dispatch keep their own durable identities and materialize exactly one
  Issue or direct Task.
- Verification: `server/internal/handler/autopilot_create_atomicity_test.go`
  and `server/internal/handler/autopilot_idempotency_test.go`.

### Squad, Agent and Skill

- Entries: `POST /api/squads`, `POST /api/agents`, and `POST /api/skills`.
- Squad owner: `server/internal/handler/squad.go`, with client recovery in
  `packages/core/squads/mutations.ts` and
  `packages/core/squads/mutations.test.tsx`.
- Agent owner: `packages/core/agents/create.ts`,
  `packages/core/api/transport.ts`,
  `packages/core/platform/recoverable-operation-store.ts`, and
  `server/internal/handler/agent.go`.
- Skill owner: `packages/core/skills/create.ts`,
  `packages/core/api/transport.ts`,
  `packages/core/platform/recoverable-operation-store.ts`, and
  `server/internal/handler/skill.go`.
- Boundary: initial members, initial Agent status, or Skill files commit with
  the resource and `server/internal/handler/resource_create_idempotency.go`;
  `server/pkg/db/queries/resource_create_request.sql` stores exact replay.
- Verification: `server/internal/handler/squad_idempotency_test.go`,
  `server/internal/handler/agent_idempotency_test.go`, and
  `server/internal/handler/skill_idempotency_test.go`.

## Lifecycle and integration flows

### Issue, Comment and Task

- Entries: `POST /api/issues`, `PUT /api/issues/{id}`,
  `POST /api/issues/{id}/comments`, and daemon Task start/complete/fail routes.
- HTTP owners: `server/internal/handler/issue.go` and
  `server/internal/handler/comment.go`.
- Task owners: `server/internal/service/task_claim.go`,
  `server/internal/service/task_complete.go`, and
  `server/internal/service/task_fail.go`.
- Boundary: primary writes and terminal outbox events commit atomically;
  receipt-tracked projections run through `server/cmd/server/task_projection.go`.
  Batch Issue updates remain explicitly per-item.
- Verification: `server/internal/handler/issue_delete_atomicity_test.go` and
  `server/internal/handler/issue_batch_test.go`.

### Prompt Evaluation

- Entries: Asset run, Run sync/review and Skill-candidate apply routes.
- HTTP owners: `server/internal/handler/prompt_evaluation_asset.go` and
  `server/internal/handler/prompt_evaluation_dataset_versions.go`.
- Projection owners: `server/internal/service/prompt_evaluation_sync.go` and
  `server/cmd/server/prompt_evaluation_projection.go`.
- Frontend mutation owners:
  `packages/views/prompt-library/components/use-prompt-library-mutations.ts`
  and
  `packages/views/prompt-library/components/use-skill-candidate-workflow-actions.ts`.
- Boundary: Runs bind one authoritative Task; terminal projection updates
  Trials, scores and evidence atomically. Missing machine verdicts require
  review and never become fabricated pass results.

### Gongfeng repository and merge request

- Entries: workspace repository probe/resolve, Project resource create, and
  Issue merge-request create/link.
- Owners: `server/internal/handler/workspace.go`,
  `server/internal/handler/project_resource.go`,
  `server/internal/handler/github.go`,
  `server/internal/service/task_complete.go`, and
  `server/internal/handler/issue.go`.
- UI/CLI owners:
  `packages/views/settings/components/project-gongfeng-repositories.tsx` and
  `server/cmd/multica/cmd_issue_pull_request.go`.
- Boundary: credentials stay account-scoped; Project resources reference the
  workspace inventory. Remote create is reconciled by branch pair before the
  local mirror/link transaction claims success.

### Lark

- Entry: verified long-connection events and `POST /api/lark/binding/redeem`.
- Owners: `server/internal/integrations/lark/dispatcher.go`,
  `server/internal/integrations/lark/outbound.go`,
  `server/cmd/server/chat_projection.go`, and
  `server/internal/eventoutbox/outbox.go`.
- Boundary: inbound provider identity is deduplicated before Chat/task writes.
  Final replies use receipt-tracked at-least-once delivery; typing indicators
  remain best-effort. Lark has no caller idempotency key for sends, so a crash
  after remote acceptance can duplicate a final reply rather than lose it.
- Verification: `server/internal/integrations/lark/dispatcher_test.go` and
  `server/internal/integrations/lark/outbound_test.go`.

## API client boundary

`packages/core/api/client.ts` is the typed endpoint registry;
`packages/core/api/transport.ts` owns mutable connection state, auth/CSRF and
workspace headers, structured errors, invalid JSON and outcome-unknown retry.
Domain `schemas-*.ts` files own response parsing. Business state, persistence
and retry policy do not belong in endpoint methods.

When a route, table, state owner or boundary above changes, update this file and
run `pnpm test:current-system-map`.
