# Current domain flows

This index is the maintained entry point for Multica's high-risk business
flows. The generated [current system map](./current-system-map.md) inventories
surfaces; these documents explain ownership, transaction boundaries, recovery,
and the one supported path through those surfaces.

| Flow | Why it is maintained | Document |
| --- | --- | --- |
| Chat create and send | Two-step persisted intent, task enqueue, attachments and unknown outcomes | [Chat send](./chat-send-flow.md) |
| Project create | Optional resources, exact response replay, Web reload and CLI retry | [Project create](./project-create-flow.md) |
| Autopilot create and dispatch | Atomic initial trigger, three ingress identities and Issue/task materialization | [Autopilot lifecycle](./autopilot-flow.md) |
| Squad create | Atomic leader and initial membership, exact replay, Web reload and CLI retry | [Squad create](./squad-create-flow.md) |
| Lark inbound and outbound | Verified inbound dedup and durable at-least-once final delivery | [Lark boundary](./lark-boundary-flow.md) |
| Issue, comment and task lifecycle | Atomic primary writes, terminal outbox and truthful batch semantics | [Issue/task lifecycle](./issue-task-lifecycle-flow.md) |
| Prompt Evaluation | Versioned datasets, task-bound Runs, atomic terminal projection and review artifacts | [Prompt Evaluation](./prompt-evaluation-flow.md) |
| Gongfeng repository and MR | Inventory/resource ownership, credential boundary and recoverable remote create | [Gongfeng repository](./gongfeng-repository-flow.md) |

## Shared rules

- React Query owns server state; workspace/account-scoped Zustand stores own
  client drafts and unresolved logical operations.
- A mutation response marked outcome-unknown is retried only when the same
  logical-operation identity is durable at both client and server.
- Database state and durable replay identity commit together. WebSocket,
  analytics and process wake-ups happen after commit and must not be mistaken
  for the system of record.
- Current first-party callers use one route and payload. Optional behavior at a
  documented external boundary is not permission to add an internal second
  implementation.

When a flow changes, update its document and the verification anchors in the
same commit. `pnpm test:current-system-map` checks that the routes and tables
named by this index still exist in the generated inventory.
