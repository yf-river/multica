# Skill create flow

Manual Skill creation has one recoverable path. The shared dialog delegates the
operation state machine to `packages/core/skills/create.ts`; Core persists the
exact request and UUIDv4 key in workspace-scoped storage before calling
`POST /api/skills` through `packages/core/skills/pending-operation-store.ts`.

The API validates all supporting-file paths before opening a transaction. That
transaction reserves `resource_create_request[type=skill]`, creates the Skill
and every supporting file, serializes the exact `201` response, completes the
request record and commits. A failure at any point rolls back all four parts.

An unknown transport or response-contract outcome is retried once with the same
key. If it remains unknown, the pending operation survives reload; the next
submit replays the stored request rather than combining the old key with newly
typed fields. A definitive rejection releases the key. Replays return the exact
stored Skill and file list and do not publish a second `skill:created` event.

Verification anchors:

- HTTP orchestration: `server/internal/handler/skill.go`.
- Shared replay validation: `server/internal/handler/resource_create_idempotency.go`.
- Transaction, concurrency, conflict and rollback:
  `server/internal/handler/skill_idempotency_test.go`.
- Cross-resource migration:
  `server/cmd/migrate/resource_create_request_migration_test.go`.
- Transport retry and persisted recovery:
  `packages/core/api/client.test.ts`, `packages/core/skills/create.test.ts` and
  `packages/core/skills/pending-operation-store.test.ts`.
