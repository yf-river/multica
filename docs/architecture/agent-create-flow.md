# Agent create flow

Agents page and Squad member creation both call the same Core operation in
`packages/core/agents/create.ts`. It persists the exact `CreateAgentRequest`
and UUIDv4 request key in workspace-scoped storage before calling
`POST /api/agents`; the durable client owner is
`packages/core/agents/pending-operation-store.ts`.

The handler validates runtime ownership, scope, model and reasoning settings
before writing. One transaction then reserves
`resource_create_request[type=agent]`, creates the Agent, derives its initial
status from active tasks, converts and redacts the actor-specific response,
stores that exact `201`, and commits. Replays return the stored response and do
not emit another `agent:created` event or analytics record.

The former post-commit status reconcile was removed from this path. An online
runtime now produces the initial `idle` state inside the create transaction;
failure cannot leave a committed Agent followed by a false 500 or a response
that differs from later replay.

Core automatically retries one unknown transport/response outcome with the same
key. If it remains unknown, reload keeps the original request and key; a later
submit recovers that operation rather than combining a stale identity with new
form fields. Definitive failures release the pending operation.

Verification anchors:

- HTTP orchestration: `server/internal/handler/agent.go`.
- Transaction, concurrent replay, conflict and rollback:
  `server/internal/handler/agent_idempotency_test.go`.
- Shared replay state machine:
  `server/internal/handler/resource_create_idempotency.go`.
- Transport and reload recovery:
  `packages/core/api/client.test.ts`, `packages/core/agents/create.test.ts` and
  `packages/core/agents/pending-operation-store.test.ts`.
- First-party callers: `packages/views/agents/components/agents-page.tsx` and
  `packages/views/squads/components/squad-detail-page.tsx`.
