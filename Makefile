.PHONY: help makehelp dev server daemon cli multica build test migrate-up migrate-down sqlc seed clean setup start stop check worktree-env setup-main start-main stop-main check-main setup-worktree start-worktree stop-worktree check-worktree db-up db-down db-reset selfhost selfhost-build selfhost-stop goal-test-build goal-test-deploy-dev goal-test-sync-prod goal-test-promote-prod goal-test-deploy-prod goal-test-deploy-int goal-test-deploy-all goal-test-verify-env goal-test-verify-logs goal-test-e2e-preflight goal-test-e2e goal-test-e2e-all goal-test-real-agent-e2e goal-test-smoke goal-test-fast-check goal-test-smart-verify goal-test-ui-acceptance goal-test-ui-audit goal-test-dashboard-click-audit goal-test-training-performance-audit goal-test-public-training-performance-audit goal-test-prune-dev-data goal-test-prune-prod-data
.PHONY: goal-test-deploy-dev-hot goal-test-dev-ui goal-test-dev-ui-prewarm goal-test-dev-ui-prewarm-full goal-test-dev-ui-start goal-test-dev-server goal-test-dev-daemon goal-test-dev-check

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
GOAL_TEST_REAL_AGENT_PROVIDER ?= codebuddy
GOAL_TEST_REAL_AGENT_MODEL ?= deepseek-v4-pro-ioa
GOAL_TEST_REAL_AGENT_FALLBACK_MODEL ?= deepseek-v4-pro-ioa
GOAL_TEST_INT_API_URL ?= http://127.0.0.1:18762
GOAL_TEST_INT_WORKSPACE ?= ai-studio
GOAL_TEST_INT_ACCOUNT ?= develop
GOAL_TEST_INT_PASSWORD ?= develop123

define REQUIRE_ENV
	@if [ ! -f "$(ENV_FILE)" ]; then \
		echo "缺少 env 文件：$(ENV_FILE)"; \
		echo "请从 .env.example 创建 .env，或运行 'make worktree-env' 使用 .env.worktree。"; \
		exit 1; \
	fi
endef

# 默认 target 从 selfhost 改为 help：裸 `make` 现在会打印这份帮助，
# 而非启动一次完整 Docker Compose 构建，对新人更安全。
.DEFAULT_GOAL := help

##@ 帮助

help: ## 显示可用 make target 与常用本地工作流
	@awk 'BEGIN {FS = ":.*## "; printf "\n用法:\n  make \033[36m<target>\033[0m\n\n快速开始:\n  \033[36mmake dev\033[0m          引导当前 checkout 并启动一切\n  \033[36mmake check\033[0m        跑完整本地验证流水线\n\nCheckout 模式:\n  主 checkout 用 \033[36m.env\033[0m\n  Worktree 用 \033[36m.env.worktree\033[0m（用 \033[36mmake worktree-env\033[0m 生成）\n\n"} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

makehelp: help ## `make help` 的别名

# ---------- 自部署（Docker Compose）----------
##@ 自部署

selfhost: ## 若需要则创建 .env，然后拉取并启动官方自部署镜像
	@if [ ! -f .env ]; then \
		echo "==> 从 .env.example 创建 .env..."; \
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
		echo "==> 已生成随机 JWT_SECRET 与 POSTGRES_PASSWORD"; \
	fi
	@echo "==> 拉取官方 Multica 镜像..."
	@if ! docker compose -f docker-compose.selfhost.yml pull; then \
		echo ""; \
		echo "tag '$${MULTICA_IMAGE_TAG:-latest}' 的官方镜像尚未发布。"; \
		echo "若这是首次 GHCR release 之前，从当前 checkout 构建："; \
		echo "  make selfhost-build"; \
		exit 1; \
	fi
	@echo "==> 通过 Docker Compose 启动 Multica..."
	docker compose -f docker-compose.selfhost.yml up -d
	@echo "==> 等待后端就绪..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
			break; \
		fi; \
		sleep 2; \
	done
	@if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
		echo ""; \
		echo "✓ Multica 已启动！"; \
		echo "  前端：http://localhost:$${FRONTEND_PORT:-3000}"; \
		echo "  后端：http://localhost:$${PORT:-8080}"; \
		echo ""; \
		echo "镜像：$${MULTICA_BACKEND_IMAGE:-ghcr.io/multica-ai/multica-backend}:$${MULTICA_IMAGE_TAG:-latest}"; \
		echo "      $${MULTICA_WEB_IMAGE:-ghcr.io/multica-ai/multica-web}:$${MULTICA_IMAGE_TAG:-latest}"; \
		echo ""; \
		echo "在前端用账号名与密码登录。"; \
		echo ""; \
		echo "下一步——装 CLI 并连接你的机器："; \
		echo "  brew install multica-ai/tap/multica"; \
		echo "  multica setup self-host"; \
	else \
		echo ""; \
		echo "服务仍在启动中。查看日志："; \
		echo "  docker compose -f docker-compose.selfhost.yml logs"; \
	fi

selfhost-build: ## 从当前 checkout 构建后端/web 镜像并启动自部署 stack
	@if [ ! -f .env ]; then \
		echo "==> 从 .env.example 创建 .env..."; \
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
		echo "==> 已生成随机 JWT_SECRET 与 POSTGRES_PASSWORD"; \
	fi
	@echo "==> 从当前 checkout 构建 Multica..."
	docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build
	@echo "==> 等待后端就绪..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
			break; \
		fi; \
		sleep 2; \
	done
	@if curl -sf http://localhost:$${PORT:-8080}/health > /dev/null 2>&1; then \
		echo ""; \
		echo "✓ Multica 已启动！"; \
		echo "  前端：http://localhost:$${FRONTEND_PORT:-3000}"; \
		echo "  后端：http://localhost:$${PORT:-8080}"; \
		echo ""; \
		echo "在前端用账号名与密码登录。"; \
		echo ""; \
		echo "已通过 docker-compose.selfhost.build.yml 本地构建镜像。"; \
		echo "本地 tag：multica-backend:dev 与 multica-web:dev。"; \
		echo ""; \
		echo "下一步——装 CLI 并连接你的机器："; \
		echo "  brew install multica-ai/tap/multica"; \
		echo "  multica setup self-host"; \
	else \
		echo ""; \
		echo "服务仍在启动中。查看日志："; \
		echo "  docker compose -f docker-compose.selfhost.yml logs"; \
	fi

selfhost-stop: ## 停止自部署 Docker Compose stack
	@echo "==> 停止 Multica 服务..."
	docker compose -f docker-compose.selfhost.yml down
	@echo "✓ 所有服务已停止。"

# ---------- 一键命令 ----------
##@ 一键

setup: ## 从 env 文件配置当前 checkout：装依赖、确保 DB、跑迁移
	$(REQUIRE_ENV)
	@echo "==> 使用 env 文件：$(ENV_FILE)"
	@echo "==> 安装依赖..."
	pnpm install
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "==> 跑迁移..."
	cd server && go run ./cmd/migrate up
	@echo ""
	@echo "✓ 配置完成！跑 'make start' 启动应用。"

start: ## 启动当前 checkout 的后端与前端，先跑迁移
	$(REQUIRE_ENV)
	@echo "使用 env 文件：$(ENV_FILE)"
	@echo "后端：http://localhost:$(PORT)"
	@echo "前端：http://localhost:$(FRONTEND_PORT)"
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "跑迁移..."
	cd server && go run ./cmd/migrate up
	@echo "启动后端与前端..."
	@trap 'kill 0' EXIT; \
		(cd server && go run ./cmd/server) & \
		pnpm dev:web & \
		wait

stop: ## 停止当前 checkout 的后端与前端进程
	$(REQUIRE_ENV)
	@echo "停止服务..."
	@-lsof -ti:$(PORT) | xargs kill -9 2>/dev/null
	@-lsof -ti:$(FRONTEND_PORT) | xargs kill -9 2>/dev/null
	@case "$(DATABASE_URL)" in \
		""|*@localhost:*|*@localhost/*|*@127.0.0.1:*|*@127.0.0.1/*|*@\[::1\]:*|*@\[::1\]/*) \
			echo "✓ 应用进程已停止。共享 PostgreSQL 仍在 localhost:$(POSTGRES_PORT) 运行。" ;; \
		*) \
			echo "✓ 应用进程已停止。远程 PostgreSQL 未受影响。" ;; \
	esac

check: ## 为当前 checkout 跑 typecheck、TS 测试、Go 测试、Playwright E2E
	$(REQUIRE_ENV)
	@ENV_FILE="$(ENV_FILE)" bash scripts/check.sh

# ---------- goal-test 部署 ----------
##@ goal-test 部署

goal-test-build: ## 构建后端/CLI 二进制与 web 生产 bundle，用于 goal-test
	$(MAKE) build
	pnpm --filter @multica/web build

goal-test-deploy-dev: ## 构建一次、部署 goal-test 联调开发环境、然后验证
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy int --build --frontend-mode next-start
	GOWORK=off node scripts/goal-test-environments.mjs verify int
	GOWORK=off node scripts/goal-test-environments.mjs verify-logs int

goal-test-deploy-dev-hot: ## 构建一次、用 web 热重载部署 goal-test 联调开发环境
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy int --build --frontend-mode next-dev
	GOWORK=off node scripts/goal-test-environments.mjs verify int
	GOWORK=off node scripts/goal-test-environments.mjs verify-logs int

goal-test-dev-ui: ## 确保 goal-test 联调 web 热重载在跑，不重建后端或守护进程
	node scripts/goal-test-environments.mjs dev-ui int

goal-test-dev-ui-prewarm: ## 不重启服务预热 goal-test 联调 web 热重载路由
	node scripts/goal-test-environments.mjs dev-ui-prewarm int

goal-test-dev-ui-prewarm-full: ## 不重启服务预热所有已知 goal-test 联调 web 热重载路由
	GOAL_TEST_WEB_PREWARM_SCOPE=full node scripts/goal-test-environments.mjs dev-ui-prewarm int

goal-test-dev-ui-start: ## 确保 goal-test 联调 web 生产启动在跑，不重建后端或守护进程
	node scripts/goal-test-environments.mjs dev-ui-start int

goal-test-dev-server: ## 仅重建并重启 goal-test 联调后端 server
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs dev-server int

goal-test-dev-daemon: ## 仅重建并重启 goal-test 联调守护进程
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	GOWORK=off TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs dev-daemon int

goal-test-dev-check: ## 跑最小 goal-test 设置/UI 检查，快速反馈
	node scripts/goal-test-environments.mjs dev-check int

goal-test-sync-prod: ## 把已构建的 goal-test 产物同步到生产，然后验证
	node scripts/goal-test-environments.mjs deploy prod
	node scripts/goal-test-environments.mjs verify prod
	node scripts/goal-test-environments.mjs verify-logs prod

goal-test-promote-prod: goal-test-sync-prod ## 别名：把当前构建产物提升到生产

goal-test-deploy-prod: ## 直接构建并部署 goal-test 生产稳定环境
	@mkdir -p "$(GOAL_TEST_TMPDIR)" "$(GOAL_TEST_GOCACHE)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" GOCACHE="$(GOAL_TEST_GOCACHE)" node scripts/goal-test-environments.mjs deploy prod --build
	node scripts/goal-test-environments.mjs verify prod
	node scripts/goal-test-environments.mjs verify-logs prod

goal-test-deploy-int: goal-test-deploy-dev ## 别名：构建并部署 goal-test 联调开发环境

goal-test-deploy-all: ## 先部署 dev，再把同一产物同步到生产
	$(MAKE) goal-test-deploy-dev
	$(MAKE) goal-test-sync-prod

goal-test-verify-env: ## 验证 goal-test 生产与联调环境
	node scripts/goal-test-environments.mjs verify all

goal-test-verify-int-env: ## 仅验证 goal-test 联调环境
	node scripts/goal-test-environments.mjs verify int

goal-test-verify-logs: ## 验证当前部署的 goal-test 日志，不扫描旧的追加历史
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-e2e-preflight: ## 浏览器 E2E 前检查 goal-test Playwright/API/DB 前置
	PLAYWRIGHT_BASE_URL=$${PLAYWRIGHT_BASE_URL:-http://9.134.129.162:13682} node scripts/goal-test-e2e-preflight.mjs

goal-test-e2e: goal-test-e2e-preflight ## 用固定联调 env 跑 goal-test Playwright；传 SPEC="e2e/file.spec.ts" 与 ARGS="--project=chromium"
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs

goal-test-e2e-all: goal-test-e2e-preflight ## 用固定联调 env 跑全部 goal-test Playwright spec
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs --project=chromium

goal-test-real-agent-e2e: goal-test-e2e-preflight ## 为 user-center squad 与训练/评估跑慢的真实 Codex Agent E2E
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	RUN_REAL_AGENT_E2E=1 \
	MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER="$(GOAL_TEST_REAL_AGENT_PROVIDER)" \
	MULTICA_PROMPT_EVALUATION_AGENT_MODEL="$(GOAL_TEST_REAL_AGENT_MODEL)" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" \
	node scripts/goal-test-playwright.mjs e2e/squad-real-agent.spec.ts e2e/prompt-library-real-agent.spec.ts --project=chromium

goal-test-smoke: ## 快速 goal-test 门禁：E2E preflight、环境验证、当前日志窗口验证
	$(MAKE) goal-test-e2e-preflight
	node scripts/goal-test-environments.mjs verify int
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-fast-check: ## changed-aware 开发门禁；除非 smart verifier 要求，避免部署/审计
	node scripts/goal-test-smart-verify.mjs --mode dev

goal-test-smart-verify: ## changed-aware goal-test 门禁；传 MODE=dev|precommit|final 与 DRY_RUN=1 预览
	node scripts/goal-test-smart-verify.mjs --mode $${MODE:-dev} $${DRY_RUN:+--dry-run}

goal-test-ui-acceptance: goal-test-smoke ## 为当前 goal-test dev 部署跑固定的浏览器/UI/性能/日志验收
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-ui-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-dashboard-click-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-playwright.mjs e2e/navigation.spec.ts e2e/production-acceptance.spec.ts --project=chromium
	node scripts/goal-test-environments.mjs verify-logs int

goal-test-ui-audit: goal-test-smoke ## 跑真实浏览器 goal-test 联调 UI、性能、console、中文语义、日志窗口审计
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-ui-audit.mjs

goal-test-dashboard-click-audit: goal-test-smoke ## 跑真实浏览器 dashboard sidebar/导航点击延迟审计
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-dashboard-click-audit.mjs

goal-test-training-performance-audit: goal-test-smoke ## 跑真实浏览器训练/评估路由请求、性能、日志窗口审计
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs

goal-test-public-training-performance-audit: goal-test-smoke ## 通过公开前端 URL 跑训练/评估性能审计
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	GOAL_TEST_BROWSER_URL="$${GOAL_TEST_BROWSER_URL:-http://9.134.129.162:13682}" \
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-training-performance-audit.mjs

goal-test-prune-dev-data: goal-test-smoke ## 通过公开 API 清理旧 goal-test 联调数据；传 APPLY=1 执行
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	TMPDIR="$(GOAL_TEST_TMPDIR)" node scripts/goal-test-prune-dev-data.mjs $${APPLY:+--apply} $${KEEP:+--keep=$${KEEP}}

goal-test-prune-prod-data: ## 通过公开 API 清理旧 goal-test 生产稳定测试数据；dry-run 审查后才能传 APPLY=1
	@mkdir -p "$(GOAL_TEST_TMPDIR)"
	node scripts/goal-test-environments.mjs verify prod
	TMPDIR="$(GOAL_TEST_TMPDIR)" GOAL_TEST_PRUNE_ENV=prod node scripts/goal-test-prune-dev-data.mjs $${APPLY:+--apply} $${KEEP:+--keep=$${KEEP}} $${CANONICAL_SOP_ONLY:+--canonical-sop-only}
	node scripts/goal-test-environments.mjs verify-logs prod

db-up: ## 启动主 checkout 与 worktree 共用的 PostgreSQL 容器
	@$(COMPOSE) up -d postgres

db-down: ## 停止共享 PostgreSQL 容器，不删除其 Docker volume
	@$(COMPOSE) down

# 删除 + 重建当前 env 的数据库，再跑所有迁移。
# 用于本地开发重置干净状态。只影响 ENV_FILE 中的 POSTGRES_DB；
# 共享 postgres 容器与其他 worktree DB 不受影响。远程主机拒绝运行。
db-reset: ## 删除并重建当前 env 的数据库，再跑所有迁移
	$(REQUIRE_ENV)
	@case "$(DATABASE_URL)" in \
		""|*@localhost:*|*@localhost/*|*@127.0.0.1:*|*@127.0.0.1/*|*@\[::1\]:*|*@\[::1\]/*) ;; \
		*) echo "拒绝重置：DATABASE_URL 指向远程主机。"; exit 1 ;; \
	esac
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	@echo "==> 删除并重建数据库 '$(POSTGRES_DB)'..."
	@$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d postgres -v ON_ERROR_STOP=1 \
		-c "DROP DATABASE IF EXISTS \"$(POSTGRES_DB)\" WITH (FORCE);" \
		-c "CREATE DATABASE \"$(POSTGRES_DB)\";"
	@echo "==> 跑迁移..."
	cd server && go run ./cmd/migrate up
	@echo ""
	@echo "✓ 数据库 '$(POSTGRES_DB)' 已重置。跑 'make start' 启动应用。"

worktree-env: ## 为当前 worktree 生成带唯一 DB 名与应用端口的 .env.worktree
	@bash scripts/init-worktree-env.sh .env.worktree

setup-main: ## 用 .env 配置主 checkout
	@$(MAKE) setup ENV_FILE=$(MAIN_ENV_FILE)

start-main: ## 用 .env 启动主 checkout
	@$(MAKE) start ENV_FILE=$(MAIN_ENV_FILE)

stop-main: ## 停止 .env 定义的主 checkout 进程
	@$(MAKE) stop ENV_FILE=$(MAIN_ENV_FILE)

check-main: ## 为主 checkout 跑完整验证流水线
	@ENV_FILE=$(MAIN_ENV_FILE) bash scripts/check.sh

setup-worktree: ## 确保 .env.worktree 存在，然后配置当前 worktree
	@if [ ! -f "$(WORKTREE_ENV_FILE)" ]; then \
		echo "==> 生成带唯一端口的 $(WORKTREE_ENV_FILE)..."; \
		bash scripts/init-worktree-env.sh $(WORKTREE_ENV_FILE); \
	else \
		echo "==> 使用既有 $(WORKTREE_ENV_FILE)"; \
	fi
	@$(MAKE) setup ENV_FILE=$(WORKTREE_ENV_FILE)

start-worktree: ## 用 .env.worktree 启动当前 worktree
	@$(MAKE) start ENV_FILE=$(WORKTREE_ENV_FILE)

stop-worktree: ## 停止当前 worktree 的后端与前端进程
	@$(MAKE) stop ENV_FILE=$(WORKTREE_ENV_FILE)

check-worktree: ## 为当前 worktree 跑完整验证流水线
	@ENV_FILE=$(WORKTREE_ENV_FILE) bash scripts/check.sh

# ---------- 单项命令 ----------
##@ 单项命令

dev: ## 端到端引导当前 checkout：创建 env、确保 DB、迁移、启动服务
	@bash scripts/dev.sh

server: ## 仅跑当前 checkout 的 Go server
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/server

daemon: ## 用 CLI 存储的 auth/session 重启本地智能体守护进程
	@$(MAKE) multica MULTICA_ARGS="daemon restart --profile local"

cli: ## 用 ARGS 或 MULTICA_ARGS 从源码跑 multica CLI
	@$(MAKE) multica MULTICA_ARGS="$(MULTICA_ARGS)"

multica: ## 直接从 Go 源码树跑 multica CLI 入口
	cd server && go run ./cmd/multica $(MULTICA_ARGS)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

build: ## 构建 server、CLI、migrate 二进制到 server/bin
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/server ./cmd/server
	cd server && go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" -o bin/multica ./cmd/multica
	cd server && go build -o bin/migrate ./cmd/migrate

test: ## 确保目标 DB 存在并应用迁移后跑 Go 测试
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate up
	cd server && go test -race ./...

# 数据库
##@ 数据库

migrate-up: ## 需要时创建目标 DB，然后应用数据库迁移
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate up

migrate-down: ## 需要时创建目标 DB，然后回滚数据库迁移
	$(REQUIRE_ENV)
	@bash scripts/ensure-postgres.sh "$(ENV_FILE)"
	cd server && go run ./cmd/migrate down

sqlc: ## 重新生成 sqlc 代码
	cd server && sqlc generate

# 清理
##@ 清理

clean: ## 删除生成的 server 二进制与临时文件
	rm -rf server/bin server/tmp
