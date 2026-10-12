# Repository Guidelines

This file provides guidance to AI agents when working with code in this repository.

> **Single source of truth:** This file is a concise pointer document.
> All authoritative architecture, coding rules, and conventions
> live in **CLAUDE.md** at the project root. Read that file first.
> Use `Makefile`, `package.json`, and `pnpm-workspace.yaml` as the
> source of truth for the full command list.

## Quick Reference

### Default Development Delivery

Ordinary feature requests include implementation, focused checks, deployment to the existing **int** environment, complete evidence, automatic promotion of the same release to **prod**, and real browser acceptance. Read `CLAUDE.md` and `docs/operations/environment-releases.md` before choosing an environment or starting services. Read addresses and identity from current deployment records and private runtime configuration; do not create another preview by default.

Independent local environments require an explicit user request and `MULTICA_LOCAL_DEV=1` for that command. Commit, push, production deployment, and database deletion remain separately authorized actions. Explicit requests for analysis only, local changes only, or no deployment override the default delivery scope.

When an authorized deployment originates inside a Multica task, use `pnpm release request <release-id>` and finish the task normally. The independent publisher waits for tasks to finish; never synchronously wait for the publication from that same task. Inspect its persistent result with `pnpm release request-status <request-id>` outside the initiating task.

The publisher deploys the requested release to int first. Production promotion requires the task to have completed naturally and the persisted int evidence to include deployment identity, browser/API/WebSocket checks, real task completion, artifacts, workspace handoff, and zero fatal logs. A task completion or health response alone is not production evidence.

### Architecture

Go backend + monorepo frontend (pnpm workspaces + Turborepo) with shared packages.

Current delivery scope is Web + Server + Daemon + PostgreSQL + Agent Runtime. Desktop and Mobile source remain in the repository but are frozen until the user explicitly reactivates them; routine changes and checks do not include them.

- `server/` - Go backend (Chi router, sqlc, gorilla/websocket)
- `apps/web/` - Next.js frontend (App Router)
- `apps/mobile/` - Expo / React Native iOS app (read `apps/mobile/CLAUDE.md` first)
- `apps/docs/` - Fumadocs documentation site
- `packages/core/` - Headless business logic (Zustand stores, React Query hooks, API client)
- `packages/ui/` - Atomic UI components (shadcn/Base UI, zero business logic)
- `packages/views/` - Shared business pages/components
- `packages/tsconfig/` - Shared TypeScript config
- `packages/eslint-config/` - Shared ESLint config

### State Management (critical)

- **React Query** owns all server state (issues, members, agents, inbox, workspace list)
- **Zustand** owns client/view state (view filters, drafts, modals, tab layout); current workspace identity is route-driven and only mirrored for platform plumbing
- All Zustand stores live in `packages/core/` - never in `packages/views/` or app directories
- WS events update React Query for server data; store writes are only for clearing client-owned pointers with a single responder/self-event guard

### Package Boundaries (hard rules)

- `packages/core/` - zero react-dom, zero localStorage, zero process.env
- `packages/ui/` - zero `@multica/core` imports
- `packages/views/` - zero `next/*`, zero `react-router-dom`, use `NavigationAdapter` for routing
- `apps/web/platform/` - only place for Next.js APIs

### Database Migrations (hard rules)

- Never add database foreign keys or cascading actions. Enforce relationships and perform dependent cleanup explicitly in the application layer, using transactions when the operation must be atomic.
- Every index created by a migration, including unique indexes and indexes on new tables, must use `CREATE [UNIQUE] INDEX CONCURRENTLY`. Keep each concurrent index build in its own single-statement migration file.

### Commands

```bash
node scripts/goal-test-environments.mjs verify  # Inspect isolated int and production
pnpm typecheck        # TypeScript check
pnpm test             # TS unit tests (Vitest)
make test             # Go tests
make check            # Full verification pipeline
pnpm test:smoke       # Personal Studio core Web flow
pnpm verify:clients   # Explicit frozen-client verification
```

See CLAUDE.md for the authoritative rules and common commands.

For explicitly requested independent local development: `MULTICA_LOCAL_DEV=1 make up C=api,web` or `MULTICA_LOCAL_DEV=1 make dev`.
