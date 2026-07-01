.PHONY: help makehelp dev server daemon cli multica build test migrate-up migrate-down sqlc seed clean setup start stop check worktree-env setup-main start-main stop-main check-main setup-worktree start-worktree stop-worktree check-worktree db-up db-down db-reset selfhost selfhost-build selfhost-stop goal-test-build goal-test-deploy-dev goal-test-sync-prod goal-test-promote-prod goal-test-deploy-prod goal-test-deploy-int goal-test-deploy-all goal-test-verify-env goal-test-verify-logs goal-test-e2e-preflight goal-test-e2e goal-test-e2e-all goal-test-real-agent-e2e goal-test-training-browser-e2e goal-test-training-curl-e2e goal-test-seed-business-training goal-test-prod-seed-business-training goal-test-prod-training-curl-e2e goal-test-coding-squad-curl-e2e goal-test-user-center-squad-curl-e2e goal-test-sop-customer-comment-e2e goal-test-sop-browser-audit goal-test-prod-user-center-squad-curl-e2e goal-test-new-account-mcp-onboarding-e2e goal-test-prod-new-account-mcp-onboarding-e2e goal-test-acceptance-fixture-governance goal-test-real-benchmark-audit goal-test-historical-benchmark-readiness goal-test-quick-entry-cross-service goal-test-squad-curl-e2e goal-test-variable-project-topology-fixture goal-test-variable-agent-squad-curl-e2e goal-test-variable-agent-topology-fixture goal-test-topology-generalization-audit goal-test-tapd-gongfeng-sop-gap-audit goal-test-prod-release-audit goal-test-smoke goal-test-fast-check goal-test-smart-verify goal-test-ui-acceptance goal-test-final-acceptance goal-test-ui-audit goal-test-dashboard-click-audit goal-test-training-performance-audit goal-test-public-training-performance-audit goal-test-dataset-stream-audit goal-test-prune-dev-data goal-test-prune-prod-data goal-test-playground-difference-audit goal-test-session-retro goal-test-token-audit
.PHONY: goal-test-deploy-dev-hot goal-test-dev-ui goal-test-dev-ui-start goal-test-dev-server goal-test-dev-daemon goal-test-dev-check goal-test-codex-network-check
.PHONY: goal-test-historical-service-sandbox goal-test-quick-entries-service-sandbox
.PHONY: goal-test-benchmark-training-loop

MAIN_ENV_FILE ?= .env
WORKTREE_ENV_FILE ?= .env.worktree
ENV_FILE ?= $(if $(wildcard $(MAIN_ENV_FILE)),$(MAIN_ENV_FILE),$(if $(wildcard $(WORKTREE_ENV_FILE)),$(WORKTREE_ENV_FILE),$(MAIN_ENV_FILE)))

ifneq ($(wildcard $(ENV_FILE)),)
include $(ENV_FILE)
endif

POSTGRES_DB ?= multica
POSTGRES_USER ?= multica
POSTGRES_PASSWORD ?= multica
POSTGRES_PORT ?= 5432
PORT := $(or $(BACKEND_PORT),$(API_PORT),$(SERVER_PORT),$(PORT),8080)
FRONTEND_PORT ?= 3000
FRONTEND_ORIGIN ?= http://localhost:$(FRONTEND_PORT)
MULTICA_APP_URL ?= $(FRONTEND_ORIGIN)
DATABASE_URL ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable
NEXT_PUBLIC_API_URL ?= http://localhost:$(PORT)
NEXT_PUBLIC_WS_URL ?= ws://localhost:$(PORT)/ws
MULTICA_SERVER_URL ?= ws://localhost:$(PORT)/ws
LOCAL_UPLOAD_BASE_URL ?= http://localhost:$(PORT)

export

MULTICA_ARGS ?= $(ARGS)

COMPOSE := docker compose
GOAL_TEST_TMPDIR ?= /data/tmp/goal-test
GOAL_TEST_GOCACHE ?= /data/tmp/goal-test-gocache
GOAL_TEST_REAL_AGENT_PROVIDER ?= codex
GOAL_TEST_REAL_AGENT_MODEL ?= gpt-5.3-codex-spark
GOAL_TEST_REAL_AGENT_FALLBACK_MODEL ?= gpt-5.4-mini
GOAL_TEST_INT_API_URL ?= http://127.0.0.1:18762
GOAL_TEST_INT_WORKSPACE ?= goal-test-daemon
GOAL_TEST_INT_ACCOUNT ?= goal-test-daemon
GOAL_TEST_INT_PASSWORD ?= e2e-password

define REQUIRE_ENV
	@if [ ! -f "$(ENV_FILE)" ]; then \
		echo "Missing env file: $(ENV_FILE)"; \
		echo "Create .env from .env.example, or run 'make worktree-env' and use .env.worktree."; \
		exit 1; \
	fi
endef

# Default target changed from selfhost to help: bare `make` now prints this help
# instead of launching a full Docker Compose build, which is safer for onboarding.
.DEFAULT_GOAL := help

##@ Help

help: ## Show available make targets and common local workflows
	@awk 'BEGIN {FS = ":.*## "; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nQuick start:\n  \033[36mmake dev\033[0m          Bootstrap the current checkout and start everything\n  \033[36mmake check\033[0m        Run the full local verification pipeline\n\nCheckout modes:\n  Main checkout uses \033[36m.env\033[0m\n  Worktrees use \033[36m.env.worktree\033[0m (generate with \033[36mmake worktree-env\033[0m)\n\n"} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

makehelp: help ## Alias for `make help`

# ---------- Self-hosting (Docker Compose) ----------
##@ Self-hosting

selfhost: ## Create .env if needed, then pull and start the official self-hosted images
	@if [ ! -f .env ]; then \
		echo "==> Creating .env from .env.example..."; \
		cp .env.example .env; \
		JWT=$$(openssl rand -hex 32); \
		PGPASS=$$(openssl rand -hex 24); \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=$$JWT/" .env; \
			sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$$PGPASS/" .env; \
			sed -i '' -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$$PGPASS\2#" .env; \
		else \
			sed -i "s/^JWT_SECRET=.*/JWT_SECRET=$$JWT/" .env; \
			sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$$PGPASS/" .env; \
			sed -i -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$$PGPASS\2#" .env; \
		fi; \
		echo "==> Generated random JWT_SECRET and POSTGRES_PASSWORD"; \
	fi
	@echo "==> Pulling official Multica images..."
	@if ! docker compose -f docker-compose.selfhost.yml pull; then \
		echo ""; \
		echo "Official images for tag '$${MULTICA_IMAGE_TAG:-latest}' are not published yet."; \
		echo "If this is before the first GHCR release, build from the current checkout:"; \
		echo "  make selfhost-build"; \
		exit 1; \
	fi
	@echo "==> Starting Multica via Docker Compose..."
	docker compose -f docker-compose.selfhost.yml up -d
	@echo "==> Waiting for backend to be ready..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
			break; \
		fi; \
		sleep 2; \
	done
	@if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
		echo ""; \
		echo "✓ Multica is running!"; \
		echo "  Frontend: http://localhost:$${FRONTEND_PORT:-3000}"; \
		echo "  Backend:  http://localhost:$${PORT:-8080}"; \
		echo ""; \
		echo "Images: $${MULTICA_BACKEND_IMAGE:-ghcr.io/multica-ai/multica-backend}:$${MULTICA_IMAGE_TAG:-latest}"; \
		echo "        $${MULTICA_WEB_IMAGE:-ghcr.io/multica-ai/multica-web}:$${MULTICA_IMAGE_TAG:-latest}"; \
		echo ""; \
		echo "Log in with an account name and password at the frontend."; \
		echo ""; \
		echo "Next — install the CLI and connect your machine:"; \
		echo "  brew install multica-ai/tap/multica"; \
		echo "  multica setup self-host"; \
	else \
		echo ""; \
		echo "Services are still starting. Check logs:"; \
		echo "  docker compose -f docker-compose.selfhost.yml logs"; \
	fi

selfhost-build: ## Build backend/web from the current checkout and start the self-hosted stack
	@if [ ! -f .env ]; then \
		echo "==> Creating .env from .env.example..."; \
		cp .env.example .env; \
		JWT=$$(openssl rand -hex 32); \
		PGPASS=$$(openssl rand -hex 24); \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=$$JWT/" .env; \
			sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$$PGPASS/" .env; \
			sed -i '' -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$$PGPASS\2#" .env; \
		else \
			sed -i "s/^JWT_SECRET=.*/JWT_SECRET=$$JWT/" .env; \
			sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$$PGPASS/" .env; \
			sed -i -E "s#^(DATABASE_URL=postgres://[^:]+:)[^@]*(@.*)#\1$$PGPASS\2#" .env; \
		fi; \
		echo "==> Generated random JWT_SECRET and POSTGRES_PASSWORD"; \
	fi
	@echo "==> Building Multica from the current checkout..."
	docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build
	@echo "==> Waiting for backend to be ready..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
			break; \
		fi; \
		sleep 2; \
	done
	@if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
		echo ""; \
		echo "✓ Multica is running!"; \
		echo "  Frontend: http://localhost:$${FRONTEND_PORT:-3000}"; \
		echo "  Backend:  http://localhost:$${PORT:-8080}"; \
		echo ""; \
		echo "Log in with an account name and password at the frontend."; \
		echo ""; \
		echo "Built images locally via docker-compose.selfhost.build.yml."; \
		echo "Local tags: multica-backend:dev and multica-web:dev."; \
		echo ""; \
		echo "Next — install the CLI and connect your machine:"; \
		echo "  brew install multica-ai/tap/multica"; \
		echo "  multica setup self-host"; \
	else \
		echo ""; \
		echo "Services are still starting. Check logs:"; \
		echo "  docker compose -f docker-compose.selfhost.yml logs"; \
	fi

selfhost-stop: ## Stop the self-hosted Docker Compose stack
	@echo "==> Stopping Multica services..."
	docker compose -f docker-compose.selfhost.yml down
	@echo "✓ All services stopped."

# ---------- One-click commands ----------
##@ One-click

setup: ## Prepare the current checkout from its env file: install deps, ensure DB, run migrations
	$(REQUIRE_ENV)
	@echo "==> Using env file: $(ENV_FILE)"
	@echo "==> Installing dependencies..."
	pnpm install
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "==> Running migrations..."
	cd server && go run ./cmd/migrate up
	@echo ""
	@echo "✓ Setup complete! Run 'make start' to launch the app."

start: ## Start backend and frontend for the current checkout and run migrations first
	$(REQUIRE_ENV)
	@echo "Using env file: $(ENV_FILE)"
	@echo "Backend: http://localhost:$(PORT)"
	@echo "Frontend: http://localhost:$(FRONTEND_PORT)"
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "Running migrations..."
	cd server && go run ./cmd/migrate up
	@echo "Starting backend and frontend..."
	@trap 'kill 0' EXIT; \
		(cd server && go run ./cmd/server) & \
		pnpm dev:web & \
		wait

stop: ## Stop backend and frontend processes for the current checkout
	$(REQUIRE_ENV)
	@echo "Stopping services..."
	@-lsof -ti:$(PORT) | xargs kill -9 2>/dev/null
	@-lsof -ti:$(FRONTEND_PORT) | xargs kill -9 2>/dev/null
	@case "$(DATABASE_URL)" in \
		""|*@localhost:*|*@localhost/*|*@127.0.0.1:*|*@127.0.0.1/*|*@\[::1\]:*|*@\[::1\]/*) \
			echo "✓ App processes stopped. Shared PostgreSQL is still running on localhost:$(POSTGRES_PORT)." ;; \
		*) \
			echo "✓ App processes stopped. Remote PostgreSQL was not affected." ;; \
	esac

check: ## Run typecheck, TS tests, Go tests, and Playwright E2E for the current checkout
	$(REQUIRE_ENV)
	@ENV_FILE="$(ENV_FILE)" bash scripts/check.sh

# ---------- goal-test deployment ----------
##@ goal-test deployment

goal-test-build: ## Build backend/CLI binaries and the web production bundle for goal-test
	$(MAKE) build
	pnpm --filter @multica/web build

goal-test-deploy-dev: ## Build once, deploy goal-test integration development environment, then verify it
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy int --build --frontend-mode next-start
	GOWORK=off node scripts/goal-test-environments.mjs verify int
	GOWORK=off node scripts/goal-test-environments.mjs verify-logs int

goal-test-deploy-dev-hot: ## Build once, deploy goal-test integration development environment with web hot reload
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy int --build --frontend-mode next-dev
	GOWORK=off node scripts/goal-test-environments.mjs verify int
	GOWORK=off node scripts/goal-test-environments.mjs verify-logs int

goal-test-dev-ui: ## Ensure goal-test integration web hot reload is running without rebuilding backend or daemon
	node scripts/goal-test-environments.mjs dev-ui int

goal-test-dev-ui-start: ## Ensure goal-test integration web production start is running without rebuilding backend or daemon
	node scripts/goal-test-environments.mjs dev-ui-start int

goal-test-dev-server: ## Rebuild and restart only the goal-test integration backend server
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs dev-server int

goal-test-dev-daemon: ## Rebuild and restart only the goal-test integration daemon
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs dev-daemon int

goal-test-codex-network-check: ## Verify the goal-test Codex runner network profile; set RESPONSES_SMOKE=1 for a real Responses request
	node scripts/goal-test-environments.mjs codex-network-check int $${RESPONSES_SMOKE:+--responses-smoke}

goal-test-dev-check: ## Run the minimal goal-test settings/UI checks for fast feedback
	node scripts/goal-test-environments.mjs dev-check int

goal-test-sync-prod: ## Sync the already-built goal-test artifact to production, then verify it
	node scripts/goal-test-environments.mjs deploy prod
	node scripts/goal-test-environments.mjs verify prod
	node scripts/goal-test-environments.mjs verify-logs prod

goal-test-promote-prod: goal-test-sync-prod ## Alias: promote the current built artifact to production

goal-test-deploy-prod: ## Build and deploy goal-test production stable environment directly
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy prod --build
	node scripts/goal-test-environments.mjs verify prod
	node scripts/goal-test-environments.mjs verify-logs prod

goal-test-deploy-int: goal-test-deploy-dev ## Alias: build and deploy goal-test integration development environment

goal-test-deploy-all: ## Build and deploy dev first, then sync the same artifact to production
	$(MAKE) goal-test-deploy-dev
	$(MAKE) goal-test-sync-prod

goal-test-verify-env: ## Verify goal-test production and integration environments
	node scripts/goal-test-environments.mjs verify all

goal-test-verify-int-env: ## Verify the goal-test integration environment only
	node scripts/goal-test-environments.mjs verify int

goal-test-verify-logs: ## Verify current-deployment goal-test logs without scanning old appended history
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-e2e-preflight: ## Check goal-test Playwright/API/DB prerequisites before browser E2E
	PLAYWRIGHT_BASE_URL=$${PLAYWRIGHT_BASE_URL:-http://9.134.129.162:13682} node scripts/goal-test-e2e-preflight.mjs

goal-test-e2e: goal-test-e2e-preflight ## Run goal-test Playwright with fixed int env; pass SPEC="e2e/file.spec.ts" and ARGS="--project=chromium"
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs

goal-test-e2e-all: goal-test-e2e-preflight ## Run all goal-test Playwright specs with fixed int env
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs --project=chromium

goal-test-real-agent-e2e: goal-test-e2e-preflight ## Run slow real Codex Agent E2E for user-center squad and training/evaluation
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	RUN_REAL_AGENT_E2E=1 \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_MODEL)" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/goal-test-playwright.mjs e2e/squad-real-agent.spec.ts e2e/prompt-library-real-agent.spec.ts --project=chromium

goal-test-training-browser-e2e: goal-test-smoke ## Run deep browser E2E for training/evaluation assets, versions, experiments, optimization, and evidence UI
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/goal-test-playwright.mjs e2e/prompt-library.spec.ts --project=chromium --grep "可以创建提示词、调试渲染并记录评测资产|提示词调试场和智能体调试场首屏职责不同|数据集版本可以对比并恢复为新的可追溯版本|实验运行会绑定资产声明的明确数据集版本|优化运行作业台汇总资产运行候选并支持取消"

goal-test-training-curl-e2e: goal-test-smoke ## Run public API curl E2E for training/evaluation assets, runs, evidence, optimization, and publishing
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	GOAL_TEST_ACCEPTANCE_DIR="$(CURDIR)/artifacts/acceptance" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/prompt-evaluation-curl-e2e.mjs

goal-test-seed-business-training: goal-test-smoke ## Seed stable non-acceptance business training assets through the public API
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	GOAL_TEST_ACCEPTANCE_DIR="$(CURDIR)/artifacts/acceptance" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/seed-business-training-assets.mjs

goal-test-coding-squad-curl-e2e: goal-test-smoke ## Run real curl/API + daemon E2E for the Multica coding squad
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	ACCEPTANCE_SQUAD_TEMPLATE_KEY=multica-coding \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	ACCEPTANCE_TASK_TIMEOUT_MS=600000 \
	ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS=720000 \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/run-model-fallback-e2e.mjs scripts/codex-squad-curl-e2e.mjs

goal-test-user-center-squad-curl-e2e: goal-test-smoke ## Run real curl/API + daemon E2E for user-center cross-project squad SOP
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	ACCEPTANCE_SQUAD_TEMPLATE_KEY=user-center \
	ACCEPTANCE_VERIFY_CROSS_PROJECT_CHILDREN=1 \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	ACCEPTANCE_TASK_TIMEOUT_MS=2700000 \
	ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS=7200000 \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/run-model-fallback-e2e.mjs scripts/codex-squad-curl-e2e.mjs

goal-test-sop-customer-comment-e2e: goal-test-e2e-preflight ## Run real customer-comment SOP E2E for usercenter/gateway/ida-deployment
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	RESPONSES_SMOKE=1 $(MAKE) goal-test-codex-network-check
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	ACCEPTANCE_REPO_REF=v5.0.0_dev_sop \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	ACCEPTANCE_TASK_TIMEOUT_MS=7200000 \
	ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS=7200000 \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/run-model-fallback-e2e.mjs scripts/run-sop-customer-comment-e2e.mjs

goal-test-sop-browser-audit: goal-test-smoke ## Audit latest customer-comment SOP issue/run-review in a real browser
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-sop-browser-audit.mjs

goal-test-squad-curl-e2e: goal-test-coding-squad-curl-e2e goal-test-user-center-squad-curl-e2e ## Run both real curl/API squad SOP E2E suites

goal-test-quick-entry-cross-service: ## Verify user-center/gateway/ida-deployment quick-entry cross-service sandbox
	node scripts/verify-quick-entry-cross-service.mjs

goal-test-quick-entries-service-sandbox: ## Run TAPD quick-entries requirement service-level process sandbox
	node scripts/goal-test-quick-entries-service-sandbox.mjs

goal-test-real-benchmark-audit: ## Audit the five real benchmark contracts and training/operation-skill closure
	node scripts/goal-test-real-benchmark-audit.mjs

goal-test-historical-benchmark-readiness: ## Probe the four v1 historical benchmark cases before service sandbox wiring
	node scripts/goal-test-historical-benchmark-readiness.mjs

goal-test-historical-service-sandbox: ## Run service-level process sandbox for the four v1 historical benchmark cases
	node scripts/goal-test-historical-service-sandbox.mjs

goal-test-benchmark-training-loop: ## Run real benchmark eval/candidate/operation-skill writeback/re-eval loop
	node scripts/goal-test-benchmark-training-loop.mjs

goal-test-variable-project-topology-fixture: goal-test-verify-int-env ## Create an int public-API fixture with one source project and three target projects
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	node scripts/goal-test-variable-project-topology-fixture.mjs

goal-test-variable-agent-squad-curl-e2e: goal-test-smoke ## Run a fresh ad-hoc single-Agent curl/API E2E for variable Agent topology
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	ACCEPTANCE_TASK_TIMEOUT_MS=300000 \
	ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS=720000 \
	ACCEPTANCE_LATEST_NAME=codex-squad-curl-e2e-variable-agent-latest.json \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/run-model-fallback-e2e.mjs scripts/codex-squad-curl-e2e.mjs

goal-test-variable-agent-topology-fixture: goal-test-variable-agent-squad-curl-e2e ## Derive a variable-agent topology artifact from a fresh completed ad-hoc E2E
	node scripts/goal-test-variable-agent-topology-from-e2e.mjs

goal-test-topology-generalization-audit: ## Audit generic cross-project and variable Agent topology evidence
	node scripts/goal-test-topology-generalization-audit.mjs

goal-test-new-account-mcp-onboarding-e2e: goal-test-verify-int-env ## Verify a newly-created member can configure TAPD/Gongfeng profiles and use MCP in int Agent runtime
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="$(GOAL_TEST_INT_API_URL)" \
	GOAL_TEST_ONBOARDING_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	GOAL_TEST_ONBOARDING_OWNER_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	GOAL_TEST_ONBOARDING_OWNER_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/goal-test-new-account-mcp-onboarding-e2e.mjs

goal-test-prod-new-account-mcp-onboarding-e2e: goal-test-verify-env ## Verify a newly-created member can configure TAPD/Gongfeng profiles and use MCP in prod Agent runtime
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="http://127.0.0.1:18760" \
	GOAL_TEST_ONBOARDING_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	GOAL_TEST_ONBOARDING_OWNER_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	GOAL_TEST_ONBOARDING_OWNER_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/goal-test-new-account-mcp-onboarding-e2e.mjs

goal-test-acceptance-fixture-governance: ## Read-only audit that acceptance-created data is traceable and governed
	node scripts/goal-test-acceptance-fixture-governance.mjs audit

goal-test-tapd-gongfeng-sop-gap-audit: ## Audit TAPD/Gongfeng/SOP final artifact against original P0 matrix
	-node scripts/goal-test-topology-generalization-audit.mjs
	node scripts/goal-test-acceptance-fixture-governance.mjs audit
	node scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs
	node scripts/tapd-gongfeng-sop-gap-audit.mjs

goal-test-prod-seed-business-training: goal-test-verify-env ## Seed stable business training assets into goal-test prod through the public API
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="http://127.0.0.1:18760" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	GOAL_TEST_ACCEPTANCE_DIR="$(CURDIR)/artifacts/acceptance" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/seed-business-training-assets.mjs

goal-test-prod-training-curl-e2e: goal-test-verify-env ## Run public API training/evaluation curl E2E against goal-test prod
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="http://127.0.0.1:18760" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	GOAL_TEST_ACCEPTANCE_DIR="$(CURDIR)/artifacts/acceptance" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/prompt-evaluation-curl-e2e.mjs

goal-test-prod-user-center-squad-curl-e2e: goal-test-verify-env ## Run real user-center squad curl/API + daemon E2E against goal-test prod
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	ACCEPTANCE_API_URL="http://127.0.0.1:18760" \
	ACCEPTANCE_WORKSPACE_SLUG="$(GOAL_TEST_INT_WORKSPACE)" \
	ACCEPTANCE_DEMO_ACCOUNT="$(GOAL_TEST_INT_ACCOUNT)" \
	ACCEPTANCE_DEMO_PASSWORD="$(GOAL_TEST_INT_PASSWORD)" \
	ACCEPTANCE_SQUAD_TEMPLATE_KEY=user-center \
	ACCEPTANCE_VERIFY_CROSS_PROJECT_CHILDREN=1 \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL="$(GOAL_TEST_REAL_AGENT_FALLBACK_MODEL)" \
	ACCEPTANCE_TASK_TIMEOUT_MS=2700000 \
	ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS=7200000 \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/run-model-fallback-e2e.mjs scripts/codex-squad-curl-e2e.mjs

goal-test-prod-release-audit: ## Generate full prod release evidence and fail if prod is not release-complete
	node scripts/goal-test-prod-release.mjs audit

goal-test-smoke: ## Fast goal-test gate: E2E preflight, environment verify, and current log window verify
	$(MAKE) goal-test-e2e-preflight
	node scripts/goal-test-environments.mjs verify int
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-fast-check: ## Changed-aware development gate; avoids deploy/audit unless the smart verifier requires it
	node scripts/goal-test-smart-verify.mjs --mode dev

goal-test-smart-verify: ## Changed-aware goal-test gate; pass MODE=dev|precommit|final and DRY_RUN=1 to preview
	node scripts/goal-test-smart-verify.mjs --mode $${MODE:-dev} $${DRY_RUN:+--dry-run}

goal-test-ui-acceptance: goal-test-smoke ## Run fixed browser/UI/performance/log acceptance for the current goal-test dev deployment
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-ui-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-dashboard-click-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playground-difference-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs e2e/navigation.spec.ts e2e/production-acceptance.spec.ts --project=chromium
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-final-acceptance: goal-test-ui-acceptance ## Run full goal-test acceptance, including real curl/API + daemon squad SOP E2E
	$(MAKE) goal-test-training-browser-e2e
	$(MAKE) goal-test-training-curl-e2e
	$(MAKE) goal-test-variable-project-topology-fixture
	$(MAKE) goal-test-variable-agent-topology-fixture
	$(MAKE) goal-test-new-account-mcp-onboarding-e2e
	$(MAKE) goal-test-acceptance-fixture-governance
	$(MAKE) goal-test-squad-curl-e2e
	$(MAKE) goal-test-quick-entry-cross-service
	$(MAKE) goal-test-quick-entries-service-sandbox
	-$(MAKE) goal-test-topology-generalization-audit
	node scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs
	node scripts/tapd-gongfeng-sop-gap-audit.mjs
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-ui-audit: goal-test-smoke ## Run real-browser goal-test integration UI, performance, console, Chinese semantics, and log-window audit
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-ui-audit.mjs

goal-test-dashboard-click-audit: goal-test-smoke ## Run real-browser dashboard sidebar/training navigation click latency audit
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-dashboard-click-audit.mjs

goal-test-training-performance-audit: goal-test-smoke ## Run real-browser training/evaluation route request, performance, and log-window audit
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs

goal-test-public-training-performance-audit: goal-test-smoke ## Run training/evaluation performance audit through the public frontend URL
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	GOAL_TEST_BROWSER_URL="$${GOAL_TEST_BROWSER_URL:-http://9.134.129.162:13682}" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs

goal-test-dataset-stream-audit: goal-test-smoke ## Create a large dataset through public API and audit Dataset Stream pagination/aggregation
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-dataset-stream-audit.mjs

goal-test-prune-dev-data: goal-test-smoke ## Prune old goal-test integration data through public APIs; pass APPLY=1 to execute
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-prune-dev-data.mjs $${APPLY:+--apply} $${KEEP:+--keep=$${KEEP}}

goal-test-prune-prod-data: ## Prune old goal-test production-stable test data through public APIs; pass APPLY=1 only after dry-run review
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	node scripts/goal-test-environments.mjs verify prod
	TMPDIR="$(GOAL_TEST_TMPDIR)" GOAL_TEST_PRUNE_ENV=prod node scripts/goal-test-prune-dev-data.mjs $${APPLY:+--apply} $${KEEP:+--keep=$${KEEP}} $${CANONICAL_SOP_ONLY:+--canonical-sop-only}
	node scripts/goal-test-environments.mjs verify-logs prod

goal-test-playground-difference-audit: goal-test-smoke ## Capture and verify prompt/agent playground visual and contract differences
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playground-difference-audit.mjs

goal-test-session-retro: ## Generate fixed session retrospective artifacts; pass SESSION=/path/to/session.jsonl
	@test -n "$(SESSION)" || (echo "Missing SESSION=/path/to/session.jsonl"; exit 2)
	node scripts/goal-test-session-retro.mjs "$(SESSION)"

goal-test-token-audit: ## Estimate goal-test token savings from session retro and command timings
	node scripts/goal-test-token-audit.mjs

db-up: ## Start the shared PostgreSQL container used by main and worktrees
	@$(COMPOSE) up -d postgres

db-down: ## Stop the shared PostgreSQL container without removing its Docker volume
	@$(COMPOSE) down

# Drop + recreate the current env's database, then run all migrations.
# Use for a clean slate in local dev. Only affects the DB named in
# ENV_FILE (POSTGRES_DB); the shared postgres container and other
# worktree DBs are untouched. Refuses to run against a remote host.
db-reset: ## Drop and recreate the current env's database, then re-run all migrations
	$(REQUIRE_ENV)
	@case "$(DATABASE_URL)" in \
		""|*@localhost:*|*@localhost/*|*@127.0.0.1:*|*@127.0.0.1/*|*@\[::1\]:*|*@\[::1\]/*) ;; \
		*) echo "Refusing to reset: DATABASE_URL points at a remote host."; exit 1 ;; \
	esac
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "==> Dropping and recreating database '$(POSTGRES_DB)'..."
	@$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d postgres -v ON_ERROR_STOP=1 \
		-c "DROP DATABASE IF EXISTS \"$(POSTGRES_DB)\" WITH (FORCE);" \
		-c "CREATE DATABASE \"$(POSTGRES_DB)\";"
	@echo "==> Running migrations..."
	cd server && go run ./cmd/migrate up
	@echo ""
	@echo "✓ Database '$(POSTGRES_DB)' reset. Run 'make start' to launch the app."

worktree-env: ## Generate .env.worktree with a unique DB name and app ports for this worktree
	@bash scripts/init-worktree-env.sh .env.worktree

setup-main: ## Prepare the main checkout using .env
	@$(MAKE) setup ENV_FILE=$(MAIN_ENV_FILE)

start-main: ## Start the main checkout using .env
	@$(MAKE) start ENV_FILE=$(MAIN_ENV_FILE)

stop-main: ## Stop the main checkout processes defined by .env
	@$(MAKE) stop ENV_FILE=$(MAIN_ENV_FILE)

check-main: ## Run the full verification pipeline for the main checkout
	@ENV_FILE=$(MAIN_ENV_FILE) bash scripts/check.sh

setup-worktree: ## Ensure .env.worktree exists, then prepare this worktree
	@if [ ! -f "$(WORKTREE_ENV_FILE)" ]; then \
		echo "==> Generating $(WORKTREE_ENV_FILE) with unique ports..."; \
		bash scripts/init-worktree-env.sh $(WORKTREE_ENV_FILE); \
	else \
		echo "==> Using existing $(WORKTREE_ENV_FILE)"; \
	fi
	@$(MAKE) setup ENV_FILE=$(WORKTREE_ENV_FILE)

start-worktree: ## Start this worktree using .env.worktree
	@$(MAKE) start ENV_FILE=$(WORKTREE_ENV_FILE)

stop-worktree: ## Stop this worktree's backend and frontend processes
	@$(MAKE) stop ENV_FILE=$(WORKTREE_ENV_FILE)

check-worktree: ## Run the full verification pipeline for this worktree
	@ENV_FILE=$(WORKTREE_ENV_FILE) bash scripts/check.sh

# ---------- Individual commands ----------
##@ Individual commands

dev: ## Bootstrap this checkout end-to-end: create env if needed, ensure DB, migrate, start services
	@bash scripts/dev.sh

server: ## Run only the Go server for the current checkout
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/server

daemon: ## Restart the local agent daemon using the CLI's stored auth/session
	@$(MAKE) multica MULTICA_ARGS="daemon restart --profile local"

cli: ## Run the multica CLI with ARGS or MULTICA_ARGS from source
	@$(MAKE) multica MULTICA_ARGS="$(MULTICA_ARGS)"

multica: ## Run the multica CLI entrypoint directly from the Go source tree
	cd server && go run ./cmd/multica $(MULTICA_ARGS)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

build: ## Build the server, CLI, and migrate binaries into server/bin
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/server ./cmd/server
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" -o bin/multica ./cmd/multica
	cd server && go build -o bin/migrate ./cmd/migrate

test: ## Run Go tests after ensuring the target DB exists and migrations are applied
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate up
	cd server && go test -race ./...

# Database
##@ Database

migrate-up: ## Create the target DB if needed, then apply database migrations
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate up

migrate-down: ## Create the target DB if needed, then roll back database migrations
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate down

sqlc: ## Regenerate sqlc code
	cd server && sqlc generate

# Cleanup
##@ Cleanup

clean: ## Remove generated server binaries and temp files
	rm -rf server/bin server/tmp
