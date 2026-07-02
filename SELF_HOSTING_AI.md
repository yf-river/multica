# Self-Hosting Setup (for AI Agents)

This document is designed for AI agents to execute. Follow these steps exactly to deploy a local Multica instance and connect to it.

## Prerequisites

- Docker and Docker Compose installed
- Homebrew installed (for CLI)
- At least one AI agent CLI on PATH: `claude` or `codex`

## Install

```bash
# Install CLI + provision self-host server
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --with-server

# Configure CLI for localhost, authenticate, and start daemon
multica setup self-host
```

Wait for the server output `✓ Multica server is running and CLI is ready!` before running `multica setup self-host`.

**Expected result:**
- Frontend at http://localhost:3000
- Backend at http://localhost:8080
- `multica` CLI installed and configured for localhost

## Alternative: Manual Setup

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
make selfhost
brew install multica-ai/tap/multica
multica setup self-host
```

The `multica setup self-host` command will:
1. Configure CLI to connect to localhost:8080 / localhost:3000
2. Open a browser for account/password login
3. Discover workspaces automatically
4. Start the daemon in the background

## Verification

```bash
multica daemon status
```

Should show `running` with detected agents.

## Production Demo / Observability Verification

Before marking a self-host deployment ready for internal team use or a leadership demo, collect evidence from the product, API, logs, and database. The full Chinese runbook lives in:

```text
apps/docs/content/docs/production-observability.zh.mdx
```

最小检查：

```bash
curl -fsS http://localhost:8080/health
curl -fsS http://localhost:8080/readyz
curl -fsS -o /tmp/multica-login.html -w '%{http_code} %{time_total}\n' http://localhost:3000/login
pnpm acceptance:evidence
```

然后打开 `/{workspaceSlug}/training?view=demo-dashboard` 并确认：

- 页面能看到 CodeBuddy 运行时就绪状态。
- 训练评估运行指标不是空数据。
- SOP/任务观测指标不是空数据，或明确标记为空。
- 运行历史里至少能展开一条真实 Agent 运行，并看到任务标识、模型、运行时、token、trace 事件、任务消息和单次执行结果。
- 优化候选必须经过人工发布或拒绝。

真实 CodeBuddy 执行证据需要运行显式开启的 E2E：

```bash
RUN_REAL_AGENT_E2E=1 \
REAL_AGENT_E2E_ACCOUNT=<daemon-account> \
REAL_AGENT_E2E_WORKSPACE=<workspace-slug> \
MULTICA_PROMPT_EVALUATION_AGENT_MODEL=minimax-m2.7-ioa \
PLAYWRIGHT_BASE_URL=http://localhost:3000 \
FRONTEND_ORIGIN=http://localhost:3000 \
NEXT_PUBLIC_API_URL=http://localhost:8080 \
NEXT_PUBLIC_WS_URL=ws://localhost:8080/ws \
pnpm exec playwright test e2e/prompt-library-real-agent.spec.ts --timeout=300000
```

## Stopping

```bash
# Stop the daemon
multica daemon stop

# Stop all Docker services
cd multica
make selfhost-stop
```

## Custom Ports

If the default ports (8080/3000) are in use:

1. Edit `.env` and change `PORT` and `FRONTEND_PORT`
2. Run `make selfhost`
3. Run `multica setup self-host --port <PORT> --frontend-port <FRONTEND_PORT>`

## Troubleshooting

- **Backend not ready:** `docker compose -f docker-compose.selfhost.yml logs backend`
- **Frontend not ready:** `docker compose -f docker-compose.selfhost.yml logs frontend`
- **Daemon issues:** `multica daemon logs`
- **Health checks:** `curl http://localhost:8080/health` for liveness, `curl http://localhost:8080/readyz` for dependency-aware readiness
