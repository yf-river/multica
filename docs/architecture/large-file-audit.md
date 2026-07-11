# Maintained large-file audit

This audit covers human-maintained production source files above 1,500 lines
and separately reviews test files above that threshold. Generated code,
migrations and vendored source are excluded. The threshold is a review trigger,
not a line-count target: files are split only when the move creates a real
responsibility boundary.

## Current result

| File | Before | Current | Decision |
|---|---:|---:|---|
| `packages/views/issues/components/issue-detail.tsx` | 2,296 | 1,424 | Split presentation from page orchestration |
| `packages/views/chat/components/chat-window.tsx` | 1,788 | 1,477 | Split agent picker, empty state and cache projection |
| `packages/views/issues/components/swimlane-view.tsx` | 1,536 | 1,441 | Split drag identity, collision and position projection |
| `packages/core/api/client.ts` | 3,239 | 3,239 | Retain as the single HTTP transport facade |
| `server/internal/handler/issue.go` | 1,557 | 1,557 | Retain as the mapped Issue HTTP lifecycle boundary |

No newly created production file exceeds 1,500 lines. Several extracted domain
modules intentionally exceed 300 lines because they contain cohesive current
behavior rather than forwarding wrappers; the largest is
`server/pkg/agent/hermes_client.go` at 1,435 lines.

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

## Large test files

The following test files exceed 1,500 lines. They are retained as test-only
scenario suites, not production boundaries. Their size is mostly fixture and
protocol-matrix cost; splitting them without first extracting a stable fixture
vocabulary would duplicate setup and make cross-scenario invariants harder to
review.

| Suite group | Files and current lines | Decision |
|---|---|---|
| Handler integration fixtures | `handler/daemon_test.go` 4,979; `handler/handler_test.go` 4,279; `handler/prompt_evaluation_asset_test.go` 3,619; `handler/squad_briefing_test.go` 1,878; `handler/github_test.go` 1,772; `handler/squad_comment_trigger_test.go` 1,672; `handler/project_resource_test.go` 1,588; `handler/dashboard_test.go` 1,510 | Retain package-local database fixtures and domain scenario matrices together; new domains should use a dedicated test file rather than extending the generic fixture file |
| Daemon and execution environment | `daemon/execenv/execenv_test.go` 4,375; `daemon/daemon_test.go` 2,437; `daemon/execenv/runtime_config_test.go` 1,723; `daemon/repocache/cache_test.go` 1,572 | Retain OS/runtime matrices with their shared sandbox fixtures; split only when a fixture becomes independently reusable |
| CLI and agent protocols | `cmd/multica/cmd_issue_test.go` 2,422; `pkg/agent/codex_test.go` 2,415; `pkg/agent/hermes_test.go` 2,081; `pkg/agent/openclaw_test.go` 1,569 | Retain protocol golden cases by provider/command so request and event variants remain reviewable in one place |
| Frontend behavior matrices | `views/run-reviews/components/run-reviews-page.test.ts` 1,839; `views/issues/components/swimlane-view.test.tsx` 1,567 | Retain pure projection and interaction matrices beside their shared builders; production logic has already moved into bounded modules |
| Lark delivery | `integrations/lark/dispatcher_test.go` 1,708 | Retain retry, receipt and dead-letter scenarios around one provider fixture |

This is not a blanket exemption. A large test file must not accumulate unrelated
production domains, and any new scenario should go into the smallest existing
domain suite. The next split trigger is duplicated fixture construction or a
test helper that can be named without importing another domain's internals.

## Verification

- Views typecheck and lint pass.
- Views: 153 test files, 1,267 tests pass.
- Chat focused tests: 27 pass; Swimlane: 42 pass; Issue Detail: 34 pass.
- Knip reports no unused or duplicate exports after the split.
- `scripts/current-system-map.test.mjs` guards the retained Issue route/source
  ownership.
