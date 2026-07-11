# Autopilot lifecycle: current flow

Autopilot has one atomic creation path and three current dispatch identities:
schedule trigger, webhook delivery and caller-keyed manual trigger. The removed
reserved `api` trigger kind is not part of the current model.

## Creation ownership

| Concern | Current owner | Source |
| --- | --- | --- |
| Form orchestration | Shared Autopilot dialog | `packages/views/autopilots/components/autopilot-dialog.tsx` |
| Server state and unresolved operations | Core queries, mutations and workspace storage | `packages/core/autopilots/queries.ts`, `packages/core/autopilots/mutations.ts`, `packages/core/autopilots/pending-operation-store.ts` |
| Runtime response boundary | Automation schemas/API client | `packages/core/api/schemas-automation.ts`, `packages/core/api/client.ts` |
| Validation and atomic create | Autopilot handler | `server/internal/handler/autopilot.go` |
| Shared trigger preparation | Trigger handler module | `server/internal/handler/autopilot_triggers.go` |
| Dispatch materialization | Autopilot service | `server/internal/service/autopilot.go` |

```text
AutopilotDialog
  -> persist exact create request + UUIDv4 in workspace storage
  -> POST /api/autopilots [exact payload + Idempotency-Key]
  -> validate assignee, project, subscribers and prepared trigger
  -> replay committed workspace + actor + key + hash, if present
  -> one transaction:
       create Autopilot
       create subscriber template
       create first schedule/webhook Trigger
       bind initial_trigger_id
       commit
  -> publish autopilot:created + analytics
  -> return Autopilot + initial_trigger

lost response / reload
  -> rehydrate the exact create request or per-Autopilot manual trigger key
  -> retry with the original key
  -> return the single committed Autopilot/Run
```

There is no frontend `create Autopilot -> create Trigger` double write. A forced
trigger failure leaves no Autopilot. Concurrent same-key requests converge on
the row identified by `(workspace, creator, request_key)`; a different payload
with that key returns 409.

## Dispatch identities

| Ingress | Durable identity | Path |
| --- | --- | --- |
| Schedule | Trigger and due schedule state | Scheduler → `DispatchAutopilot` |
| Webhook | Provider delivery/dedupe key | `POST /api/webhooks/autopilots/{token}` → delivery → dispatch |
| Manual | Canonical UUIDv4 `Idempotency-Key` | `POST /api/autopilots/{id}/trigger` → `DispatchAutopilotOnce` |

Manual dispatch stores `autopilot_run.request_key` under the Autopilot/source.
A concurrent retry waits for the winning Run to materialize and returns it; it
does not create a second Issue or task. Schedule and webhook paths already have
their own domain identities and do not manufacture caller keys.

After a unique Run is established, the service chooses exactly one execution
mode:

- `create_issue`: atomically creates the Issue, task, Run linkage, audience,
  outbox event and applicable Squad SOP state;
- `run_only`: atomically creates and links the direct task and Run state.

Admission skips and dispatch failures remain visible Run outcomes. Terminal
Issue/task events update Run terminal state through the durable projection;
they are not hidden by a default success value.

## State and recovery

- React Query owns Autopilot, Trigger, Run and delivery server state.
- Zustand owns view filters plus the exact unresolved create/manual operation;
  it never owns Autopilot, Trigger or Run server records. Pending operations
  are workspace-scoped, survive reload, and are removed on success, definitive
  rejection, workspace deletion or logout.
- WebSocket lifecycle events invalidate the Autopilot query family; they do not
  copy Runs into a Zustand server-state cache.
- The detail response may omit an absolute webhook URL when no public URL is
  configured. Web/Desktop compose the current path from the API base/origin;
  this is the explicit response-drift boundary, not an old trigger path.

## Verification anchors

- Atomic create, concurrent replay and rollback:
  `server/internal/handler/autopilot_create_atomicity_test.go`.
- Manual dispatch replay and concurrency:
  `server/internal/handler/autopilot_idempotency_test.go` and
  `server/internal/handler/autopilot_private_leader_test.go`.
- Webhook delivery identity: `server/internal/handler/webhook_delivery_test.go`.
- Client key retention and same-key retry:
  `packages/core/autopilots/mutations.test.tsx` and
  `packages/core/api/client.test.ts`.
- Current schema transitions: `server/migrations/008_autopilot_manual_trigger_idempotency.up.sql`,
  `server/migrations/009_autopilot_create_idempotency.up.sql` and
  `server/migrations/010_remove_inert_autopilot_api_kind.up.sql`.
