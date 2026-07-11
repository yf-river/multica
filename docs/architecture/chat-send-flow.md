# Chat send: current durable flow

This document describes the only supported Web/Desktop chat-send path. It is
an implementation map for maintainers, not a compatibility specification.

## Ownership and data flow

| Concern | Current owner | Source |
| --- | --- | --- |
| Server sessions, messages and tasks | React Query | `packages/core/chat/queries.ts` |
| Active session, selected agent and drafts | Workspace-scoped Zustand chat store | `packages/core/chat/store.ts` |
| Unconfirmed logical sends | Account-scoped persisted pending-operation store | `packages/core/chat/pending-operation-store.ts` |
| HTTP contract and transport outcome | Core API client | `packages/core/api/client.ts` |
| UI orchestration and optimistic projection | Chat window | `packages/views/chat/components/chat-window.tsx` |
| Durable mutation and replay | Go handler transaction | `server/internal/handler/chat.go` |
| Task creation and post-commit publication | Task service | `server/internal/service/task_enqueue.go` |

```text
ChatInput
  -> persist PendingChatOperation(UUIDv4, account, workspace, exact payload)
  -> POST /api/chat/sessions              [same Idempotency-Key, if needed]
  -> persist resolved session id
  -> optimistic message + pending task
  -> POST /api/chat/sessions/{id}/messages [same Idempotency-Key]
  -> replace optimistic ids from 201
  -> remove PendingChatOperation

reload / lost response
  -> rehydrate PendingChatOperation
  -> replay its current step with the same UUID
  -> server returns the original 201 without duplicate writes or events
```

The server scopes one key by workspace, authenticated actor and operation.
`create_session` and `send_message` are separate operation namespaces, so one
logical client UUID intentionally spans both requests. Reusing that key with a
different payload in either namespace returns `409 idempotency_conflict`.

## Transaction boundary

For a new message, one PostgreSQL transaction performs:

1. reserve the idempotency record;
2. lock agent, session and eligible attachments in stable order;
3. create the user message and task;
4. bind attachments and link the message to the task;
5. touch the session and persist the exact 201 response;
6. commit.

Task wake-up, queue notification, analytics and WebSocket publication happen
only after commit. A rollback therefore leaves no message, task, attachment
link, completed idempotency record or externally visible queue event.

A committed replay skips mutable business preconditions and returns the stored
response for the same authenticated scope. This remains deterministic if the
agent or session changes after the original response was lost. Authorization
and workspace identity are still established before the replay scope is used.

## Failure and recovery semantics

| Failure | Client behavior | Server guarantee |
| --- | --- | --- |
| Explicit 4xx response | Remove pending intent and restore the draft | This payload was rejected before commit |
| Gateway/server 5xx | Keep pending intent and retry the same key | Retry either executes a rolled-back request or replays its stored 201 |
| Network/abort without a response | Keep pending intent; show outcome unknown | Same-key retry converges |
| Malformed successful response | Keep pending intent and reconcile | Stored 201 can be replayed |
| Reload or workspace return | Retry the persisted current step | No duplicate session/message/task/event |
| Workspace switch during send | Continue fire-and-forget for the captured workspace | Draft clearing never follows the new route |
| Logout during send | Account cleanup removes intent; late continuations cannot repopulate cache | Any already committed server fact remains queryable on a later login |
| Stop before the real task id arrives | Persist cancel intent and cancel after replay resolves the task id | Cancellation targets the single created task |

The idempotency table intentionally has no TTL. Its rows are durable tombstones
with the same order of growth as chat mutations; deleting them would make an
old offline retry capable of creating a second business operation.

## Verification anchors

- PostgreSQL concurrency, rollback, replay and event-count tests:
  `server/internal/handler/chat_idempotency_integration_test.go`.
- Transport classification and required headers:
  `packages/core/api/client.test.ts`.
- Persistent exact-intent replay and logout race:
  `packages/core/chat/pending-operation*.test.ts`.
- Captured-workspace draft clearing:
  `packages/core/chat/store.test.ts` and
  `packages/views/chat/components/chat-input.test.tsx`.
