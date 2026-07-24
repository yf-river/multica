# 贡献指南

本指南面向在 Multica 代码库上工作的贡献者，记录本地开发流程。

涵盖内容：

- 首次配置
- 主 checkout 的日常开发
- 隔离的 worktree 开发
- 共享 PostgreSQL 模型
- 测试与验证
- 全栈隔离测试（后端 + 前端 + 守护进程均从源码跑）
- 故障排查与破坏性重置选项

架构规范、包边界、状态管理、编码规则、提交规则等不在本指南范围，请参阅 [CLAUDE.md](CLAUDE.md)。

## 开发模型

本地开发使用一个共享 PostgreSQL 容器，每个 checkout 一个数据库。

- 主 checkout 通常用 `.env` 和 `POSTGRES_DB=multica`
- 每个 git worktree 用自己的 `.env.worktree`
- 所有 checkout 连接同一个 PostgreSQL host：`localhost:5432`
- 隔离发生在数据库级别，不是通过另起一个 Docker Compose 项目
- 后端与前端端口仍然每个 worktree 唯一

这样 Docker 保持简单，schema 与数据仍然隔离。

## 环境要求

- Node.js `v20+`
- `pnpm` `v10.28+`
- Go `v1.26+`
- Docker

## 重要规则

- 主 checkout 用 `.env`。
- worktree 用 `.env.worktree`。
- 不要把 `.env` 复制进 worktree 目录。

原因：

- 当前命令流优先读 `.env` 而非 `.env.worktree`
- 若 worktree 里存在 `.env`，可能意外指回主数据库

## 环境文件

### 主 checkout

创建一次 `.env`：

```bash
cp .env.example .env
```

默认情况下 `.env` 指向：

```bash
POSTGRES_DB=multica
POSTGRES_PORT=5432
DATABASE_URL=postgres://multica:multica@localhost:5432/multica?sslmode=disable
PORT=8080
FRONTEND_PORT=3000
```

### worktree

在 worktree 内生成 `.env.worktree`：

```bash
make worktree-env
```

生成形如：

```bash
POSTGRES_DB=multica_my_feature_702
POSTGRES_PORT=5432
PORT=18782
FRONTEND_PORT=13702
DATABASE_URL=postgres://multica:multica@localhost:5432/multica_my_feature_702?sslmode=disable
```

注意：

- `POSTGRES_DB` 每个 worktree 唯一
- `POSTGRES_PORT` 固定为 `5432`
- 后端与前端端口由 worktree 路径 hash 派生
- `make worktree-env` 拒绝覆盖已存在的 `.env.worktree`

重新生成 worktree env 文件：

```bash
FORCE=1 make worktree-env
```

## 首次配置

### 快速开始（推荐）

从任意 checkout（主或 worktree）：

```bash
make dev
```

这一条命令会：

- 自动检测当前在主 checkout 还是 worktree
- 若 `.env` 或 `.env.worktree` 不存在则创建对应文件
- 检查环境依赖（Node.js、pnpm、Go、Docker）是否已安装
- 安装 JS 依赖
- 确保共享 PostgreSQL 容器在跑
- 若应用数据库不存在则创建
- 跑所有迁移
- 同时启动后端与前端

### 显式配置（高级）

若偏好分开控制 setup 与启动：

#### 主 checkout

```bash
cp .env.example .env
make setup-main
make start-main
```

停止：

```bash
make stop-main
```

#### worktree

```bash
make worktree-env
make setup-worktree
make start-worktree
```

停止：

```bash
make stop-worktree
```

## 推荐日常流程

### 主 checkout

想要 `main` 的稳定本地环境时用主 checkout。

```bash
make start-main
make stop-main
make check-main
```

### 功能 worktree

想要隔离数据与独立应用端口时用 worktree。

```bash
git worktree add ../multica-feature -b feat/my-change main
cd ../multica-feature
make dev
```

之后日常命令：

```bash
make dev              # 启动（必要时重跑 setup，幂等）
make stop-worktree    # 停止
make check-worktree   # 验证
```

## 同时跑主 checkout 与 worktree

这是一等公民工作流。

例如：

- 主 checkout
  - 数据库：`multica`
  - 后端：`8080`
  - 前端：`3000`
- worktree checkout
  - 数据库：`multica_my_feature_702`
  - 后端：生成的 worktree 端口，如 `18782`
  - 前端：生成的 worktree 端口，如 `13702`

两者都用：

- 同一个 PostgreSQL 容器
- 同一个 PostgreSQL 端口：`5432`

但应用数据不共享，因为各自用不同的数据库。

## 命令参考

### 共享基础设施

启动共享 PostgreSQL 容器：

```bash
make db-up
```

停止共享 PostgreSQL 容器：

```bash
make db-down
```

注意：

- `make db-down` 停止容器但保留 Docker volume
- 本地数据库被保留

### 应用生命周期

主 checkout：

```bash
make setup-main
make start-main
make stop-main
make check-main
```

worktree：

```bash
make worktree-env
make setup-worktree
make start-worktree
make stop-worktree
make check-worktree
```

当前 checkout 的通用 target：

```bash
make setup
make start
make stop
make check
make dev
make test
make migrate-up
make migrate-down
```

这些通用 target 要求当前目录存在合法的 env 文件。

## 数据库创建机制

数据库创建是自动的。

以下命令都会在继续前确保目标数据库存在：

- `make setup`
- `make start`
- `make dev`
- `make test`
- `make migrate-up`
- `make migrate-down`
- `make check`

该逻辑位于 `scripts/ensure-postgres.sh`。

## 测试

跑全部本地检查：

```bash
make check-main
```

或从 worktree：

```bash
make check-worktree
```

会跑：

1. TypeScript typecheck
2. TypeScript 单元测试
3. Go 测试
4. Playwright E2E 测试

注意：

- Go 测试创建自己的 fixture 数据
- E2E 测试创建自己的工作区和 issue fixture
- check 流程仅在后端/前端未运行时才启动它们

## 本地 Codex 守护进程

跑本地守护进程：

```bash
make daemon
```

守护进程用 CLI 存储的 token（`multica login`）认证。它为 CLI config 中所有 watched workspace 注册运行时。

## 全栈隔离测试

本节讲如何从源码跑完整 stack（后端、前端、守护进程），在完全隔离的环境中。适用于跨多组件的端到端变更，或需要零人工介入的 CI/AI 工作流。

### 为什么不只用 `make daemon`？

`make daemon` 用系统已装 CLI 的存储 token，连接 `~/.multica/config.json` 中配置的 server。日常对着共享 server 开发没问题，但全栈隔离测试需要：

- 本地后端与前端（从源码）
- 本地守护进程（从源码）带自己的 profile
- 自动认证（无浏览器登录）
- 不干扰生产 CLI 配置

### 动态 profile 命名

每个 worktree 必须用唯一的守护进程 profile，避免多 feature 并行时冲突。

profile 名由 worktree 目录用与 `scripts/init-worktree-env.sh` 相同的 slug + hash 模式派生：

```bash
WORKTREE_DIR="$(basename "$PWD")"
SLUG="$(printf '%s' "$WORKTREE_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
HASH="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
OFFSET=$((HASH % 1000))
PROFILE="dev-${SLUG}-${OFFSET}"
```

例如：worktree 在 `../multica-feat-auth` 会得到 profile `dev-multica_feat_auth-347`，与该 worktree 的端口和数据库分配匹配。

### 启动隔离环境

所有步骤都在 worktree 根（Makefile 所在处）执行。

#### 1. 启动后端、前端、数据库

```bash
make dev
```

等待后端 healthy：

```bash
PORT=$(grep '^PORT=' .env.worktree 2>/dev/null || grep '^PORT=' .env | head -1 | cut -d= -f2)
PORT=${PORT:-8080}
SERVER="http://localhost:${PORT}"

for i in $(seq 1 30); do
  curl -sf "$SERVER/health" > /dev/null 2>&1 && break
  sleep 2
done
```

#### 2. 创建测试用户与 token（自动认证）

```bash
JWT=$(curl -s -X POST "$SERVER/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"account": "dev", "password": "dev-password"}' | jq -r '.token')

PAT=$(curl -s -X POST "$SERVER/api/tokens" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{"name": "auto-dev", "expires_in_days": 365}' | jq -r '.token')
```

#### 3. 创建工作区

```bash
WS=$(curl -s -X POST "$SERVER/api/workspaces" \
  -H "Authorization: Bearer $PAT" \
  -H "Content-Type: application/json" \
  -d '{"name": "Dev", "slug": "dev"}' | jq -r '.id')
```

#### 4. 计算 profile 名并写 CLI config

```bash
# 计算 profile（见上方「动态 profile 命名」）
WORKTREE_DIR="$(basename "$PWD")"
SLUG="$(printf '%s' "$WORKTREE_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')"
HASH="$(printf '%s' "$PWD" | cksum | awk '{print $1}')"
OFFSET=$((HASH % 1000))
PROFILE="dev-${SLUG}-${OFFSET}"

FRONTEND_PORT=$(grep '^FRONTEND_PORT=' .env.worktree 2>/dev/null || grep '^FRONTEND_PORT=' .env | head -1 | cut -d= -f2)
FRONTEND_PORT=${FRONTEND_PORT:-3000}

CONFIG_DIR="$HOME/.multica/profiles/$PROFILE"
mkdir -p "$CONFIG_DIR"

cat > "$CONFIG_DIR/config.json" << EOF
{
  "server_url": "$SERVER",
  "app_url": "http://localhost:${FRONTEND_PORT}",
  "token": "$PAT",
  "workspace_id": "$WS",
  "watched_workspaces": [{"id": "$WS", "name": "Dev"}]
}
EOF
```

#### 5. 从源码启动守护进程

```bash
make cli ARGS="daemon start --profile $PROFILE"
```

守护进程从当前 worktree 的 Go 源码跑，连接本地后端。智能体执行的 `multica` 命令自动用同一二进制（守护进程把自己的目录前置到 `PATH`）。

### 停止隔离环境

```bash
# 计算 profile（同公式）
PROFILE="dev-$(printf '%s' "$(basename "$PWD")" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/_/g; s/__*/_/g; s/^_//; s/_$//')-$(( $(printf '%s' "$PWD" | cksum | awk '{print $1}') % 1000 ))"

# 1. 停止守护进程
make cli ARGS="daemon stop --profile $PROFILE"

# 2. 停止后端 + 前端
make stop            # 主 checkout
make stop-worktree   # worktree checkout

# 3. （可选）停止共享 PostgreSQL
make db-down

# 4. （可选）清理构建产物
make clean

# 5. （可选）删除 profile 配置
rm -rf "$HOME/.multica/profiles/$PROFILE"
```

### 隔离保证

本流程不触碰系统已装的 `multica` 或默认 `~/.multica/config.json`：

| 资源 | 系统/生产 | 本地开发（per-worktree） |
|---|---|---|
| 配置 | `~/.multica/config.json` | `~/.multica/profiles/dev-<slug>-<hash>/config.json` |
| 守护进程 PID | `~/.multica/daemon.pid` | `~/.multica/profiles/dev-<slug>-<hash>/daemon.pid` |
| 健康端口 | `19514` | `19514 + 1 + (name_hash % 1000)` |
| workspace 目录 | `~/multica_workspaces/` | `~/multica_workspaces_dev-<slug>-<hash>/` |
| 数据库 | 远程 / 生产 | 本地 Docker：`multica_<slug>_<hash>` |

多个 worktree 可并行运行不冲突。

## 故障排查

### 缺少 env 文件

若看到：

```text
Missing env file: .env
```

或：

```text
Missing env file: .env.worktree
```

先创建对应 env 文件。

主 checkout：

```bash
cp .env.example .env
```

worktree：

```bash
make worktree-env
```

### 查某个 checkout 用哪个数据库

查看 env 文件：

```bash
cat .env
cat .env.worktree
```

关注：

- `POSTGRES_DB`
- `DATABASE_URL`
- `PORT`
- `FRONTEND_PORT`

### 列出共享 PostgreSQL 中所有本地数据库

```bash
docker compose exec -T postgres psql -U multica -d postgres -At -c "select datname from pg_database order by datname;"
```

### worktree 误用主数据库

检查 worktree 是否含 `.env`。

不应含。

安全的 worktree setup：

```bash
make worktree-env
make setup-worktree
make start-worktree
```

### 应用停了但 PostgreSQL 还在跑

这是预期行为。

- `make stop`
- `make stop-main`
- `make stop-worktree`

只停后端/前端进程。

停共享 PostgreSQL 容器：

```bash
make db-down
```

## 破坏性重置

想停 PostgreSQL 并保留本地数据库：

```bash
make db-down
```

想为当前 checkout 重置数据库（drop `POSTGRES_DB` 中的库、重建、跑所有迁移）：

```bash
make stop        # 先停后端/前端
make db-reset
make start
```

- 只影响当前 env 的数据库；其他 worktree 数据库不动
- 若 `DATABASE_URL` 指向远程主机则拒绝运行
- 传 `ENV_FILE=.env.worktree` 指定特定 worktree

想清空本仓库所有本地 PostgreSQL 数据：

```bash
docker compose down -v
```

警告：

- 删除共享 Docker volume
- 删除主数据库与该 volume 里的所有 worktree 数据库
- 之后必须重跑 `make setup-main` 或 `make setup-worktree`

## 典型流程

### 稳定的主环境

```bash
make dev
```

### 功能 worktree

```bash
git worktree add ../multica-feature -b feat/my-change main
cd ../multica-feature
make dev
```

### 回到已配置过的 worktree

```bash
cd ../multica-feature
make start-worktree
```

### 推送前验证

主 checkout：

```bash
make check-main
```

worktree：

```bash
make check-worktree
```
