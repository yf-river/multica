# Project create: current durable flow

This is the only first-party Project creation path for Web, Desktop, CLI and
E2E. It covers both a metadata-only Project and a Project created together with
repository resources.

## Ownership and data flow

| Concern | Current owner | Source |
| --- | --- | --- |
| Form fields and unresolved create intent | Workspace-scoped Project draft store | `packages/core/projects/draft-store.ts` |
| Server list/detail state | React Query Project queries | `packages/core/projects/queries.ts` |
| Request identity and cache reconciliation | Core Project mutation | `packages/core/projects/mutations.ts` |
| Transport retry and response validation | Core API client | `packages/core/api/client.ts` |
| Web/Desktop form orchestration | Shared create modal | `packages/views/modals/create-project.tsx` |
| CLI request identity | CLI Project command and HTTP client | `server/cmd/multica/cmd_project.go`, `server/internal/cli/client.go` |
| Validation, transaction and replay | Project handler | `server/internal/handler/project.go` |
| Durable request/result | PostgreSQL query module | `server/pkg/db/queries/project_create_request.sql` |

```text
CreateProjectModal
  -> persist PendingCreate(UUIDv4, exact CreateProjectRequest)
  -> POST /api/projects [Idempotency-Key]
  -> handler validates and normalizes current fields/resources
  -> replay same workspace + actor + key + request hash, if committed
  -> otherwise one transaction:
       reserve project_create_request
       create Project
       create every bundled ProjectResource
       persist exact 201 response + Project link
       commit
  -> publish Project/resource realtime events
  -> cache insert + authoritative invalidation
  -> clear pending intent and form draft

lost response / reload
  -> rehydrate exact request and key from workspace storage
  -> retry that snapshot, not fields reconstructed from current UI/server data
  -> receive the stored 201 without a duplicate Project or resource
```

The current public HTTP boundary accepts a missing key so existing external
integrations remain valid. Every repository-owned caller sends a canonical
UUIDv4. A supplied key is scoped by workspace and authenticated actor; using it
with a different normalized request returns `409 idempotency_conflict`.

## Transaction and publication boundary

The former metadata-only direct insert and resource-bearing transaction have
been replaced by one path. The transaction reserves the request identity,
creates the Project and resources, and completes the exact replay response.
Failure at any point, including response completion, leaves none of those rows.

Realtime publication occurs only after commit. It accelerates cache refresh but
is not durable Project state; clients also invalidate the authoritative list
after mutation settlement. A replay returns the stored response and does not
publish a second create event.

`project_create_request.project_id` references the created Project with
`ON DELETE CASCADE`. Deleting a Project therefore removes its replay record and
prevents an unbounded orphan tombstone table. While the Project exists, deleting
the record would be unsafe because a delayed offline retry could create a
duplicate.

## Failure and recovery semantics

| Failure | Client behavior | Server guarantee |
| --- | --- | --- |
| Explicit 4xx | Release pending intent; keep editable form draft | No commit for that request |
| Transport or malformed success | Retry once with exact request/key; retain intent if still unknown | Rolled-back execution or exact committed 201 replay |
| Browser reload after unknown | Rehydrate exact pending request/key | Dynamic repository metadata cannot change the replay hash |
| Concurrent same-key calls | All callers receive one Project id | Primary-key conflict waits for the winning transaction |
| Same key, changed payload | Surface 409 | Original Project remains authoritative |
| Resource or response-record failure | Surface 5xx | Project, resources and request record all roll back |
| Project deletion | Normal target-addressed delete | Replay record is removed by foreign-key cascade |

## Verification anchors

- PostgreSQL replay, conflict, concurrency, bundled-resource, rollback and
  retention tests: `server/internal/handler/project_idempotency_test.go`.
- Resource normalization regression:
  `server/internal/handler/project_resource_normalization_test.go`.
- Browser storage rehydrate and exact-intent replay:
  `packages/core/projects/mutations.test.tsx`.
- Core and CLI same-key transport retry: `packages/core/api/client.test.ts` and
  `server/internal/cli/client_test.go`.
- Schema and typed queries: `server/migrations/011_project_create_idempotency.up.sql`
  and `server/pkg/db/queries/project_create_request.sql`.
