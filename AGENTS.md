# Repository Guidelines

This file provides guidance to AI agents when working with code in this repository.

> **Single source of truth:** This file is a concise pointer document.
> All authoritative architecture, coding rules, and conventions
> live in **CLAUDE.md** at the project root. Read that file first.
> Use `Makefile`, `package.json`, and `pnpm-workspace.yaml` as the
> source of truth for the full command list.

## Quick Reference

### Default Development Delivery

Ordinary feature requests proceed through confirmed plan, isolated task branch, focused checks, fixed **int** acceptance and **in_review**. Production deployment requires separate user confirmation for the delivery. Unapproved changes remain on task branches. Read `CLAUDE.md` and `docs/operations/environment-releases.md` for the authoritative workflow.

Use persisted delivery records and the existing external publisher for queueing, recovery, integration and production updates. Build from pinned commits rather than ephemeral task directories. Only explicit user requests allow independent environments with `MULTICA_LOCAL_DEV=1`. Explicit local-only or no-deployment boundaries take precedence.

并行验收使用固定 `verify-1/2/3`，最终集成仍使用固定 `int`，生产发布另行授权；验收种子及重置入口以 `docs/operations/environment-releases.md` 为准，禁止把验收副本或种子凭据用于生产。

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

Read `CLAUDE.md` → “环境身份与生产数据保护” and `docs/operations/environment-maintenance.md` before database maintenance or cleanup. Live connection configuration determines environment identity; a database name containing `test` does not imply disposable data.

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
