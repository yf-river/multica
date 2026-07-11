# Maintained large-file audit

This audit covers human-maintained production source files above 1,500 lines.
Generated code, migrations, vendored source and tests are excluded. It is a
review trigger, not a line-count target: files are split only when the move
creates a real responsibility boundary.

## Current result

| File | Before | Current | Decision |
|---|---:|---:|---|
| `packages/views/issues/components/issue-detail.tsx` | 2,296 | 1,424 | Split presentation from page orchestration |
| `packages/views/chat/components/chat-window.tsx` | 1,788 | 1,468 | Split agent picker, empty state and cache projection |
| `packages/views/issues/components/swimlane-view.tsx` | 1,536 | 1,441 | Split drag identity, collision and position projection |
| `packages/core/api/client.ts` | 3,239 | 3,239 | Retain as the single HTTP transport facade |
| `server/internal/handler/issue.go` | 1,557 | 1,557 | Retain as the mapped Issue HTTP lifecycle boundary |

No newly created production file exceeds 300 lines.

## Extracted boundaries

`IssueDetail` continues to own React Query, mutations, workspace context,
scroll restoration and page-level effects. The following are now independent:

- TAPD source metadata parsing and source-summary presentation;
- activity formatting and activity-block presentation;
- timeline projection and optional-property rules;
- subscriber selection, sub-issue rows and the right-hand sidebar.

`ChatWindow` continues to own send/cancel/recovery orchestration. Agent choice,
the first/returning-user empty state, and optimistic message-cache projection
are separate modules. `SwimLaneView` continues to own loaded lane data and drag
orchestration; collision filtering, identifier parsing and position projection
are pure helpers.

## Retained exceptions

### Core API client

`ApiClient` has one responsibility: turn current product operations into HTTP
requests and validate their responses. Its 246 methods deliberately share one
transport implementation, authentication/CSRF headers, workspace resolution,
error semantics and response-validation policy. Splitting by domain through
mixins, prototype mutation or one-implementation interfaces would lengthen the
call path and create multiple transport facts. Domain runtime schemas already
live in separate files; the class is retained as the sole facade and Knip must
remain empty.

### Issue HTTP handler

`issue.go` contains the current Issue HTTP lifecycle: response contracts,
workspace-scoped reads, create/update/delete boundaries and their shared
validation. List queries, batch operations, quick-create internals, response
conversion and execution-tree projection already live in dedicated sibling
files. Its route ownership is checked by the generated system map and the
maintained Issue/Task lifecycle flow. Moving the remaining 57 lines merely to
cross the threshold would fragment the lifecycle without forming a new domain.

Revisit either exception only when a real second responsibility appears, a
domain can own an end-to-end transport boundary without duplicate policy, or
the maintained route/source map changes. Do not reduce these files by deleting
useful comments or introducing forwarding wrappers.

## Verification

- Views typecheck and lint pass.
- Views: 153 test files, 1,267 tests pass.
- Chat focused tests: 27 pass; Swimlane: 42 pass; Issue Detail: 34 pass.
- Knip reports no unused or duplicate exports after the split.
- `scripts/current-system-map.test.mjs` guards the retained Issue route/source
  ownership.
