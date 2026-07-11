# Squad create flow

## Supported path

The Web modal calls `useCreateSquad`, which persists the exact
`CreateSquadRequest` and its UUIDv4 operation key in the current workspace.
The Core API client posts that payload once to `POST /api/squads` and retries
an unknown transport or response-contract outcome once with the same key. The
CLI uses the same route and supplies a caller-owned key.

The create payload contains the leader and all initial members. Creating a
Squad and then adding its members through separate requests is not a supported
first-party path.

## Transaction and replay boundary

`CreateSquad` validates workspace membership, scope, leader access and every
initial member before writing. One database transaction then reserves the
`squad_create_request` identity, creates the `squad`, inserts the mandatory
leader membership and every initial membership, and stores the exact response.
Any failure rolls back all of those rows.

The replay scope is `(workspace_id, actor_id, idempotency_key)`. Reusing a key
with the same normalized request returns the stored `201` response; reusing it
with a different request returns `409 idempotency_conflict`. Deleting the Squad
cascades its completed replay row.

## Outcome recovery

On a successful or definitive rejected response, the Web clears the pending
operation. If the outcome is unknown, it keeps both the exact request and key
across reloads. The next submit resumes that stored intent rather than adopting
new form data under the old identity. Workspace deletion and logout remove the
workspace-scoped pending operation.

`squad:created` and analytics are emitted only after commit and are never the
system of record. Replays do not emit them again.

## Verification anchors

- Route and transaction: `server/internal/handler/squad.go`
- Replay table and queries: `server/migrations/012_squad_create_idempotency.up.sql`
  and `server/pkg/db/queries/squad_create_request.sql`
- Concurrency, rollback and exact replay: `server/internal/handler/squad_idempotency_test.go`
- Reload recovery: `packages/core/squads/mutations.ts` and
  `packages/core/squads/mutations.test.tsx`
- First-party modal: `packages/views/modals/create-squad.tsx`
