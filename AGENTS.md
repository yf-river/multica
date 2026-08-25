# Repository Guidelines

This file provides guidance to AI agents when working with code in this repository.

> **Single source of truth:** This file is a concise pointer document.
> All authoritative architecture, coding rules, commands, and conventions
> live in **CLAUDE.md** at the project root. Read that file first.

## Quick Reference

### Architecture

Go backend + monorepo frontend (pnpm workspaces + Turborepo) with shared packages.

- `server/` — Go backend (Chi router, sqlc, gorilla/websocket)
- `apps/web/` — Next.js frontend (App Router)
- `apps/desktop/` — Electron desktop app
- `packages/core/` — Headless business logic (Zustand stores, React Query hooks, API client)
- `packages/ui/` — Atomic UI components (shadcn/Base UI, zero business logic)
- `packages/views/` — Shared business pages/components
- `packages/tsconfig/` — Shared TypeScript config

### State Management (critical)

- **React Query** owns all server state (issues, members, agents, inbox, workspace list)
- **Zustand** owns all client state (current workspace selection, view filters, drafts, modals)
- All Zustand stores live in `packages/core/` — never in `packages/views/` or app directories
- WS events invalidate React Query — never write directly to stores

### Package Boundaries (hard rules)

- `packages/core/` — zero react-dom, zero localStorage, zero process.env
- `packages/ui/` — zero `@multica/core` imports
- `packages/views/` — zero `next/*`, zero `react-router-dom`, use `NavigationAdapter` for routing
- `apps/web/platform/` — only place for Next.js APIs

### Commands

```bash
make dev              # Auto-setup + start everything
pnpm typecheck        # TypeScript check
pnpm test             # TS unit tests (Vitest)
make test             # Go tests
make check            # Full verification pipeline
```

See CLAUDE.md for the complete command reference.

### Goal-Test Validation

Use changed-aware checks during goal-test work so long sessions do not spend most of their time redeploying or rerunning broad audits.

```bash
make goal-test-fast-check                         # Development gate; changed-aware, no deploy
make goal-test-smart-verify MODE=dev              # Same smart verifier with explicit mode
make goal-test-smart-verify MODE=precommit        # Focused precommit gate plus smoke
make goal-test-smart-verify MODE=final DRY_RUN=1  # Preview final deploy/audit plan
make goal-test-deploy-dev                         # Deploy once at the end of a stable slice
make goal-test-ui-audit                           # Broad browser/UI audit after deploy/smoke
GOAL_TEST_TOKEN_OPTIMIZER=rtk make goal-test-smart-verify MODE=dev
```

During implementation, prefer `make goal-test-fast-check`. Run `make goal-test-deploy-dev` at most once per 60-120 minute slice unless backend startup, migrations, environment files, build configuration, or the remote environment changed. `scripts/goal-test-smart-verify.mjs` records command timings in `artifacts/acceptance/command-timings.jsonl`; inspect that file before repeating expensive gates.

Token optimization is intentionally conservative. High-noise smart-verify commands may be summarized for the model, but full raw output is preserved under `artifacts/acceptance/raw-command-logs/`. Do not globally compress `rg`, `find`, `ls`, `git diff`, failing stack traces, deploy failure windows, or `panic`/`FATAL`/`ERROR` windows. RTK is opt-in with `GOAL_TEST_TOKEN_OPTIMIZER=rtk`; when installed and the command is safe, the wrapper uses `rtk rewrite` and executes the rewritten `rtk ...` command. Do not install a global RTK hook unless explicitly requested.
