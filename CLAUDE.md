# CLAUDE.md

Guidance for Claude Code when working in this repository. Keep this file short and authoritative: rules here should be hard to infer from code or easy to get wrong.

## Conventions

The source of truth for code naming, i18n glossary, and Chinese product voice is:

- `apps/docs/content/docs/developers/conventions.mdx`
- `apps/docs/content/docs/developers/conventions.zh.mdx`

Read it before editing translations in `packages/views/locales/`, naming routes/packages/files/DB columns/types, or writing Chinese UI/docs copy.

## Project Shape

Multica is an AI-native task management platform for small teams, with agents as first-class assignees that can own issues, comment, and change status.

- `server/`: Go backend, Chi router, sqlc, gorilla/websocket.
- `apps/web/`: Next.js App Router.
- `apps/mobile/`: Expo / React Native iOS app. Read `apps/mobile/CLAUDE.md` before touching it.
- `apps/docs/`: Fumadocs documentation site.
- `packages/core/`: headless business logic, API client, React Query hooks, Zustand stores.
- `packages/ui/`: atomic UI components only.
- `packages/views/`: shared business pages/components for the web application.
- `packages/tsconfig/`: shared TypeScript config.
- `packages/eslint-config/`: shared ESLint config.

Shared packages export raw `.ts` / `.tsx` and are compiled by consuming apps. Dependency direction is `views -> core + ui`; `core` and `ui` must stay independent.

## Active Delivery Scope

The active product is Web, Server, Daemon, PostgreSQL, and Agent Runtime.

- `apps/desktop/` and `apps/mobile/` are frozen source trees. Keep their source, build, routing, and release files, but do not implement Web feature parity or run and repair their checks unless the user explicitly reactivates that client.
- Frozen-client compatibility is not a delivery constraint for Web changes. Shared code may evolve for the active Web product even when a frozen client no longer compiles.
- Root `build`, `typecheck`, `test`, and `lint` commands validate `@multica/web` and its shared dependencies. Use `verify:desktop`, `verify:mobile`, or `verify:clients` only for explicit client reactivation or release work.
- During development, verify the affected Web, Go, page, or runtime path. Ordinary feature requests include int integration delivery, feature acceptance, application of tested runtime files to prod, and browser acceptance; broader verification is proportional to risk.
- A task stops when its declared user flow passes. Unrelated historical failures are tracked separately unless they block login, companion chat, tasks, Agent execution, run reviews, memory governance, data safety, authorization, or recovery.

## Development Delivery

- 主分支为 `personal-studio-upstream`，交付远端为 `origin`；`upstream` 用于同步上游。新任务基于最新主分支创建独立任务分支，已有任务续接原分支，不覆盖主工作区或其他任务的未提交改动。
- 需求澄清后先输出编号行动摘要和完整计划，用户确认计划后执行其明确包含的开发、提交任务分支、推送和 int 验证。用户已确认的事项不重复询问；新增范围或业务取舍需要重新确认。
- 并行开发、串行联调。交付记录固定提交、方案版本和证据；从干净构建工作区执行联调，不依赖已结束运行的临时路径，不要求 Agent 手动搬走运行时注入文件。
- int 业务验收通过后进入 `in_review`，提供简短测试清单，释放联调环境，代码留在任务分支。生产发布必须有用户明确的发布授权；允许提前授权在既定范围验收通过后发布，由 Agent 复核准确候选及最新用户决定，正常发布入口校验授权来源和审核记录。
- 用户同意生产发布后，整合最新主分支及生产基线，完成必要的重新验收，再合入并推送主分支，由外部发布器应用同一份验收程序。合并冲突或实质修改返回开发与审核，不能夹带未批准任务。
- 生产发布允许短暂维护。停止服务前核对环境身份、源码、产物和数据库结构；数据库变更要兼容恢复旧程序。启动或验收失败先安全恢复旧程序与配置，保留数据并报告。
- 发布成功并完成真实浏览器验收后记录结果，直接把任务标记为 done，不再等待用户二次确认；只到 int 联调验收、构建成功或部署启动成功都还不算完成，仍进 `in_review`；发布或验收失败保留当前状态并报告，不能关闭任务；未授权生产发布、未涉及 int 或生产发布的分析、配置类任务仍按各自完成条件收尾，本条不外延。清理交付工作区和已合入临时分支；不默认创建 PR、标签或 GitHub Release。
- 读 `docs/operations/environment-releases.md` 后选择环境。使用固定 int；仅用户明确要求时才允许 `MULTICA_LOCAL_DEV=1` 的独立环境。不得使用生产账号或数据作联调测试，不编辑已安装制品、不清库或重置数据。
- 并行验收使用专属 `verify-1/2/3`，固定 `int` 负责最终集成，`prod` 只接收经过 int 和发布授权的候选。验收种子只从 int 只读导出并在导入时清理认证、外部凭据和租约；种子重置仅限专属验收库，入口和恢复边界见 `docs/operations/environment-releases.md`。
- 明确的分析、仅本地修改、禁止部署等范围优先。Commit、push、数据库删除须有对应授权；确认包含具体提交和推送动作的计划可覆盖该任务，无需重复索取。

## State Rules

Keep server state and client state separate.

- TanStack Query owns server state: issues, users, workspaces, inbox, agents, members, and anything fetched from the API.
- Zustand owns client/view state: filters, drafts, modals, tab layout, and navigation history. Current workspace identity is route-driven; platform stores/singletons may mirror slug/id only for headers, persistence namespaces, and reconnects.
- Shared Zustand stores live in `packages/core/`, never in `packages/views/` or app directories.
- React Context is for platform plumbing only, such as `WorkspaceIdProvider` and `NavigationProvider`.
- Only auth/workspace stores may call `api.*` directly. Other server interaction belongs in queries/mutations.
- Workspace-scoped query keys must include `wsId`.
- Optimistic updates only when ALL hold: outcome locally predictable, user stays on the same screen (no navigation), failure is rare, rollback is trivial. Canonical: status/assignee/toggle field patches — patch determinate caches, roll back on failure, invalidate uncertain projections on settle.
- Flows that navigate or confirm (create, delete, leave) must await the server before navigating or cleaning up; never optimistically remove an entity from cache.
- Chat/message send uses the pending-message pattern: render immediately with a visible pending state and retry on failure, not silent optimism.
- WebSocket events invalidate or patch Query cache for server data. They must never mirror server payload data into Zustand; clearing client-owned pointers (active session, selection, current workspace) is allowed only with a single responder and a self-initiated guard when this client can cause the event.
- Persist durable preferences/drafts/layout. Do not persist server data or ephemeral UI state.
- Zustand selectors must return stable references. Do not return freshly allocated objects/arrays from selectors without shallow comparison.
- Hooks that need workspace context should accept `wsId`; do not call `useWorkspaceId()` internally unless the hook is guaranteed to run under the provider.

## Package Boundaries

These are hard constraints:

- `packages/core/`: no `react-dom`, `localStorage` (use `StorageAdapter`), `process.env`, or UI libraries.
- `packages/ui/`: no `@multica/core` imports and no business logic.
- `packages/views/`: no `next/*`, no `react-router-dom`, no stores. Use `NavigationAdapter`, `useNavigation()`, and `<AppLink>`.
- `apps/web/platform/`: only place for Next.js navigation/platform APIs.
- Every workspace under `apps/` and `packages/` must declare directly imported external packages in its own `package.json`.
- Shared dependencies use `catalog:` from `pnpm-workspace.yaml`; `apps/mobile/` pins Expo/React Native related versions directly.

## Sharing Rules

The web application shares business logic, hooks, stores, components, and views through `packages/core/`, `packages/ui/`, and `packages/views/`. Mobile remains an independent client and may import only the documented core types and pure functions.

Keep logic in the lowest layer that owns it:

1. Next.js or router APIs stay in the app/platform layer.
2. Headless logic belongs in `packages/core/`.
3. Shared UI or business views belong in `packages/views/`.
4. Shared primitives belong in `packages/ui/`.

Mobile is independent. It may import types and pure functions from `@multica/core`, with `import type` for types, but owns its UI, state, hooks, providers, i18n, React version, build pipeline, and release cadence.

## Commands

Use the repo scripts as the source of truth. Common commands:

```bash
node scripts/goal-test-environments.mjs verify  # inspect isolated int and production
MULTICA_LOCAL_DEV=1 make up C=api,web  # explicitly requested independent local environment
make status           # what is running, with pid/commit proof it is yours
make list             # every development environment on this machine
make down             # stop the processes, keep the database
make destroy          # stop, then drop the database and free the slot
MULTICA_LOCAL_DEV=1 make dev    # prepare and start independent local services
MULTICA_LOCAL_DEV=1 make start  # start independent local backend + frontend
make stop             # stop app processes for this checkout
make db-drop          # permanently drop this checkout's local database
make remove-worktree WORKTREE=../path  # drop a linked worktree DB, then remove it
MULTICA_LOCAL_DEV=1 make server # run independent local Go server
MULTICA_LOCAL_DEV=1 make daemon # run local daemon
make test             # Go tests
make sqlc             # regenerate sqlc code after SQL changes
pnpm install
MULTICA_LOCAL_DEV=1 pnpm dev:web
pnpm build
pnpm typecheck
pnpm lint
pnpm test             # TS/Vitest tests through Turborepo
pnpm test:smoke       # Personal Studio core Web flow
pnpm verify:desktop   # Explicit frozen Desktop verification
pnpm verify:mobile    # Explicit frozen Mobile verification
pnpm verify:clients   # Explicit verification for both frozen clients
pnpm exec playwright test
pnpm ui:add badge     # shadcn/Base UI component into packages/ui
```

`make up` records each environment in `~/.multica/dev/`, allocates its API and Web ports plus database name under a lock instead of recomputing them from the path, and verifies the database through `DATABASE_URL` rather than `docker exec` — a `docker exec` create lands in the wrong server whenever a native PostgreSQL owns 5432. It reuses an API only when `/health` proves its listener pid, process group and commit belong to this checkout. `make down` keeps the data; `make destroy` consumes the database, profile, daemon workspaces and registry entry. Agent-owned TTL environments are collected best-effort on the next `make up`, or explicitly with `make gc`.

Explicitly requested local worktrees share one PostgreSQL container and use `.env.worktree`. Use `MULTICA_LOCAL_DEV=1` for `make worktree-env`, `make setup-worktree`, and `make start-worktree`; `make dev` auto-detects the checkout type.

CI runs Node 22, the latest Go 1.26 patch, and a `pgvector/pgvector:pg17` PostgreSQL service.

## Database and Migration Rules

### 环境身份与生产数据保护

- 数据库兼容性以实际结构和当前程序查询为准，不能用迁移文件数减 ledger 条数。快照 000 覆盖到 496，后续增量仍需执行；禁止改写已发布迁移身份或伪造覆盖范围内的 ledger。
- 数据库测试显式指定本次创建的隔离目标；`make test` 还要求 `MULTICA_TEST_DATABASE_URL` 与配置目标一致，并在迁移前拒绝固定 prod/int 实例及冻结实例；该入口使用预先创建的隔离数据库，不启动或重建共享 Compose 容器。直接运行 Go 测试也须显式设置隔离连接；测试命令保存真实退出码、提交、目标和失败项，不能用输出过滤命令的成功状态代替测试结果。功能交付只包含必要变更，历史清理单独处理；代码完成、测试通过、联调验收和生产发布分别报告。
- Agent 指令负责分析和表达，服务端与确定性工具负责权限、提交范围、幂等、覆盖核对和调度，不能仅靠提示词保证这些约束。

- 环境维护入口为 `docs/operations/environment-maintenance.md`；数据库连接真源为固定控制仓库 `/data/ida/multica/.run/env/{prod,int}-services.json`。操作前核对运行后端进程的实际连接、目标 PostgreSQL 实例和库名。文档清单是带日期的核验记录，不能代替现场身份核对；禁止依据库名含 `test`、迁移条数或暂时无连接判断数据库可删除。
- 生产数据库与业务表默认只读调查。普通开发、修复、清理或程序发布授权不包含删除、清空、重建库表、覆盖还原或直接批量修改生产业务数据；破坏性操作须单独明确实例、库表、影响、备份与恢复方式并获得用户明确授权。询问“是否可删”属于调查，不是删除授权。
- 生产结构变更须属于已确认方案，先通过固定 int 验证，再核对可还原备份、旧新程序兼容性与恢复方案，通过既有发布流程实施。发布器的自动迁移范围以 `docs/operations/environment-releases.md` 为准；禁止手工改生产表或迁移 ledger 来绕过失败、满足测试或强行启动。
- `make destroy`、`make db-drop`、`make remove-worktree`、`make gc` 和清理容器/卷等入口也受上述边界约束。执行前确认目标不被固定 prod/int、备份验证、其他任务或事故留证使用；未知用途先保留。联调数据优先级低不代表可以未经授权删除固定 int。
- 恢复验收未确认前保留事故原盘、冻结容器、恢复副本和取证库；用户明确批准将指定副本或库归档后清理时，先实际还原验证，再按准确名单执行。已有备份不构成删除授权；恢复演练只清理本次明确创建且归属已核实的副本。

These are hard requirements for every new or modified database design and production migration:

- Do not add database foreign keys (`FOREIGN KEY` / `REFERENCES`), cascading deletes, or cascading updates. Resolve relationships, validation, and dependent cleanup explicitly in application code. Use an application transaction when cleanup and the parent operation must commit or roll back atomically.
- Every index created by a migration must use `CREATE INDEX CONCURRENTLY` or `CREATE UNIQUE INDEX CONCURRENTLY`, including indexes on newly created tables. PostgreSQL rejects concurrent index creation inside a transaction or a multi-command string, so keep each concurrent index build in its own single-statement migration file. The repository migration runner executes migration files outside an explicit transaction to support this.
- A conditionally skipped migration is still recorded in `schema_migrations`, so the ledger proves ordering, not that every migration's SQL executed. Later migrations that touch conditionally present objects must use idempotent DDL such as `IF EXISTS` / `IF NOT EXISTS`, and the introducing change must document recovery when losing the selected object would break runtime behavior.

## Coding Rules

- TypeScript strict mode is enabled; keep types explicit.
- Go follows standard conventions: `gofmt`, `go vet`, checked errors.
- Code comments must be English.
- Prefer existing patterns/components over new parallel abstractions.
- Avoid broad refactors unless required by the task.
- For internal, non-boundary code, do not add compatibility layers, fallback paths, dual writes, legacy adapters, or temporary shims unless explicitly requested.
- API boundaries are different from internal calls: clients can talk to newer backends, so response parsing must follow the API compatibility rules below.
- If a flow or API is being replaced and the product is not live, prefer removing the old path instead of preserving both.
- New global pre-workspace routes must be a single word (`/login`, `/inbox`) or `/{noun}/{verb}` (`/workspaces/new`). Do not add hyphenated root routes like `/new-workspace`.
- Reserved slugs live in `server/internal/handler/reserved_slugs.json`. Edit it, run `pnpm generate:reserved-slugs`, and commit the generated `packages/core/paths/reserved-slugs.ts`.
- When changing CLI commands/flags, API fields, or product behavior documented by built-in skills under `server/internal/service/builtin_skills/*`, update the relevant `SKILL.md` and `references/*-source-map.md` in the same PR.

## API Compatibility

Frontend code for active delivery clients must survive backend response drift. Frozen clients are outside this compatibility contract until reactivated.

- Parse API JSON with `parseWithFallback` in `packages/core/api/schema.ts` and a zod schema. Do not cast network JSON to `T`.
- Endpoint responses consumed by UI logic must pass through a schema before returning.
- Downstream UI should optional-chain and default fields defensively.
- Prefer explicit boolean checks (`=== true`) over truthy/falsy checks on server fields.
- Do not pin critical affordances to one backend boolean; combine signals when possible.
- Server-driven enum switches need a `default` branch.
- When adding or changing an endpoint, add/update the schema and include a malformed-response test.

## Backend UUID Rules

In `server/internal/handler/`, always know where a UUID came from before using it in write queries.

- Resource path params that may be UUIDs or human-readable IDs must be resolved through loaders such as `loadIssueForUser`, `loadSkillForUser`, `loadAgentForUser`, or `requireDaemonRuntimeAccess`; subsequent writes use the resolved `entity.ID`.
- Pure UUID inputs from request boundaries use `parseUUIDOrBadRequest(w, s, fieldName)` and return immediately on `ok=false`.
- Trusted UUID round-trips from sqlc results or test fixtures use `parseUUID(s)`, which panics on invalid input.
- Outside handlers, `util.ParseUUID(s) (pgtype.UUID, error)` is the safe variant; always check the error.

## Web Features

When adding a web page or feature:

1. Put the page/component in `packages/views/<domain>/`.
2. Add route wiring in `apps/web/app/`.
3. Use `useNavigation().push()` or `<AppLink>` in shared code.
4. Use shared guards/providers such as `DashboardGuard` from `packages/views/layout/`.
5. Keep platform-only UI in the app or inject it through props/slots.
6. Hooks that need workspace context should accept `wsId`.

CSS for the web is shared from `packages/ui/styles/`. Use semantic tokens such as `bg-background` and `text-muted-foreground`; avoid hardcoded Tailwind colors and duplicated base styles.

## Mobile Rules

Read `apps/mobile/CLAUDE.md` before touching `apps/mobile/`. It contains the mandatory pre-flight process, import limits, parity rules, tech stack, UI rules, data helpers, realtime strategy, and mobile release flow.

These rules apply when Mobile is explicitly reactivated or its own source is intentionally changed:

- Mobile shares only `@multica/core` types and pure functions.
- Mobile must match Web product semantics when it returns to the active delivery scope: counts, permissions, enums/transitions, and data identity.
- Mobile may differ in UI/interaction when the phone context requires it.

## UI Rules

- Prefer shadcn/Base UI components over custom implementations. Add them with `pnpm ui:add <component>` from the repo root.
- The Pro `@reui` registry is configured in `packages/ui/components.json`; add items with `pnpm ui:add @reui/<name>` and answer `n` to every overwrite prompt so local component customizations survive. It reads `REUI_LICENSE_KEY` from the environment — agents get it from their Multica agent environment, humans export it in their own shell. Never write the key into a repo file.
- ReUI ships source, not a dependency: route the vendored output to our layout (new primitives to `packages/ui/components/ui/`, compositions to `packages/views/<domain>/`) and rewrite it to our conventions before committing.
- Use design tokens and semantic classes; avoid hardcoded colors. Font sizes come from the role-named `--text-*` scale in `packages/ui/styles/tokens.css` (`text-caption`, `text-body`, `text-title`, …), which is the authoritative list — not Tailwind's default `text-sm` / `text-base` ramp.
- An active/selected state must stay identifiable while hovered. Express it on a dimension hover does not touch (weight, text color), or define the `data-active:hover:` compound explicitly — otherwise hovering a selected row visually downgrades it to plain hover.
- Do not introduce extra local state unless the design requires it.
- Handle overflow, long text, scrolling, alignment, and spacing deliberately. Prefer more spacing over adding a divider.
- If a component is shared by multiple clients, it belongs in a shared package.

## Testing

Tests follow the code:

| What is tested | Location |
| --- | --- |
| Shared business logic, stores, queries, hooks | `packages/core/*.test.ts` |
| Shared UI components, pages, forms, modals | `packages/views/*.test.tsx` |
| Platform wiring such as cookies, redirects, search params | `apps/web/*.test.tsx` |
| End-to-end flows | `e2e/*.spec.ts` |
| Backend | `server/` Go tests |

Rules:

- Never test shared component behavior in an app test file.
- Give each product behavior ONE canonical layer. Pure parsing, state transitions and boundary matrices belong in a `.test.ts` beside the helper; the component suite keeps the happy path, the wiring, accessibility and named regressions, and points at the canonical file in a comment. Do not re-run a helper's matrix through a DOM mount.
- A `.test.ts` that needs no DOM must start with `// @vitest-environment node`. jsdom costs ~0.8s of setup per file and buys such a suite nothing. Do not add it to a test whose code under test branches on `typeof window`/`document` — under node it would silently take the SSR path and still pass.
- `packages/views/` tests must not mock `next/*` or `react-router-dom`.
- Mock `@multica/core` stores with the Zustand callable-store shape (`selectorFn` plus `getState`).
- Mock `@multica/core/api` for API calls.
- E2E tests should use `TestApiClient` for setup/teardown.
- Prefer writing the failing test in the correct package before implementation when the change is behavioral.
- DB-backed Go tests build their rows through `server/internal/testutil` (`dbfx.Issue`, `dbfx.Task`, `dbfx.Insert`) and drive handlers through `testutil.Call(h, req).Want(status).JSON(&out)`. Do not open-code an `INSERT ... RETURNING id` with its matching `t.Cleanup(DELETE ...)`, or a `httptest.NewRecorder()` / status-check / decode quartet, in a new test. `internal/handler` held ~1000 of the first and ~1200 of the second before the shared fixtures landed, and every change to a shared contract had to be made once per copy.
- Keep an assertion where the test wrote it when its message says something the shared one cannot. `.Want()` prints the request line, both statuses and the response body; it does not know which case in a loop was running.
- Helpers in `internal/testutil` insert rows, run handlers and report mismatches. They must not assert a product rule on a test's behalf — a helper that knows what a correct response looks like has taken the assertion away from the test making it.
- Default tests must never resolve or execute user-installed agent CLIs. Pass a test-created fake executable path or a test-created missing path to agent subprocess code.
- Real-agent smoke tests belong behind the `agentintegration` build tag and must check `MULTICA_RUN_REAL_AGENT_SMOKE=1` before executable lookup or account access.
- Run an explicitly authorized real-agent smoke test with `(cd server && MULTICA_RUN_REAL_AGENT_SMOKE=1 go test -tags=agentintegration ./pkg/agent -run '<test-name>' -count=1 -v)`. This command may access an authenticated account and consume quota.
- When adding a default agent command, add it to `scripts/agent-cli-command-names.txt`; the normal Linux/macOS test entry points fail on ambient agent CLI execution.

## Verification

For code changes, run the narrowest useful checks while iterating, then run broader verification when risk justifies it or when asked.

Useful checks:

```bash
pnpm typecheck
pnpm test
make test
pnpm exec playwright test
make check
```

Do not claim verification passed unless you ran it. If you skip checks because the change is docs-only or the user asked not to run them, say so.

## Commits and Releases

- Commits should be atomic and use conventional prefixes: `feat(scope)`, `fix(scope)`, `refactor(scope)`, `docs`, `test(scope)`, `chore(scope)`.
- Instance deployment follows the fixed int/prod workflow above; Git history and the main branch are the delivery record. Public CLI releases use a `v0.x.x` tag on main and `release.yml` to publish binaries and the Homebrew tap.
- Bump patch by default unless the user specifies a version.

## Domain Reminders

- All queries filter by `workspace_id`; membership gates access; `X-Workspace-ID` selects the workspace.
- Issue assignees are polymorphic: `assignee_type` plus `assignee_id` can reference a member or an agent.
