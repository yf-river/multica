# 自部署 — 高级配置

本文档覆盖自部署 Multica 的高级配置。快速上手见 [SELF_HOSTING.md](SELF_HOSTING.md)。

## 配置

所有配置通过环境变量完成。复制 `.env.example` 作为起点。

### 必需变量

| 变量 | 说明 | 示例 |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL 连接串 | `postgres://multica:multica@localhost:5432/multica?sslmode=disable` |
| `JWT_SECRET` | **必须改默认值。** 签 JWT token 的密钥。用长随机串。 | `openssl rand -hex 32` |
| `FRONTEND_ORIGIN` | 前端被服务的 URL（用于 CORS） | `https://app.example.com` |

### 数据库连接池调优（可选）

这些有合理默认，仅在调优大型或受限部署时设。优先级（从高到低）：env var → `DATABASE_URL` 上的 `pool_*` query 参数 → 内置默认。

| 变量 | 说明 | 默认值 |
|----------|-------------|---------|
| `DATABASE_MAX_CONNS` | 每个 pod 的 pgxpool 最大连接数。`pod_count × DATABASE_MAX_CONNS` 应远低于 Postgres `max_connections` 上限。前置连接池器（PgBouncer / RDS Proxy / Supavisor）时可显著调高。 | `25` |
| `DATABASE_MIN_CONNS` | 每个 pod 的 pgxpool 预热基线连接数。自动钳到 `DATABASE_MAX_CONNS`。 | `5` |

### 账号登录

Multica 用账号 + 密码登录。开启 signup 时，某账号首次成功登录创建该账号；后续登录需同一密码。

### 注册控制（可选）

| 变量 | 说明 |
|----------|-------------|
| `ALLOW_SIGNUP` | 设为 `false` 禁用私有实例的新用户注册 |
| `ALLOWED_ACCOUNTS` | 可选，逗号分隔的精确账号名 allowlist |
| `DISABLE_WORKSPACE_CREATION` | 设为 `true` 让 `POST /api/workspaces` 对所有调用方返回 403——管理员从工作区设置添加用户 |

变更需重启后端 / compose stack 后生效。Web UI 在运行时从 `/api/config` 读 `ALLOW_SIGNUP` 与 `DISABLE_WORKSPACE_CREATION`，无需 web 重建。

#### 锁定工作区创建

`ALLOW_SIGNUP=false` 阻止新账号创建，但**不**阻止已登录用户通过 `POST /api/workspaces` 创建另一个工作区。在所有 issue/repo/agent 都必须对平台管理员可见的自部署实例上，设 `DISABLE_WORKSPACE_CREATION=true` 关掉这个口子。推荐引导顺序：

1. 以 `DISABLE_WORKSPACE_CREATION=false`（默认）启动实例。
2. 以管理员身份登录并创建共享工作区。
3. 设 `DISABLE_WORKSPACE_CREATION=true` 并重启后端。若同时想阻止新账号创建，同时设 `ALLOW_SIGNUP=false`。
4. 之后管理员从工作区设置添加用户——UI 中的「创建工作区」入口被隐藏，任何直接 API 调用返回 403。

> 注：设 `ALLOW_SIGNUP=false` 阻止自助账号创建。管理员创建的用户仍可从工作区设置管理。

### 文件存储（可选）

文件上传与附件配置 S3 与（可选的）CloudFront：

| 变量 | 说明 |
|----------|-------------|
| `S3_BUCKET` | 仅 bucket 名（如 `my-bucket`）。**不要**包含 `.s3.<region>.amazonaws.com` 后缀——server 从 `S3_BUCKET` + `S3_REGION` 构造公开 URL |
| `S3_REGION` | AWS region（默认 `us-west-2`）。必须与 bucket 实际 region 一致——SDK 签名与公开 URL 都用它 |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | 静态凭证。两者都未设时用 AWS SDK 默认凭证链 |
| `AWS_ENDPOINT_URL` | 自定义 S3 兼容 endpoint（如 MinIO、R2、B2）。设置后切换到 path-style URL |
| `ATTACHMENT_DOWNLOAD_MODE` | 附件下载行为：`auto`（默认）、`cloudfront`、`presign`、`proxy`。Docker/VPC-only endpoint（如 `http://rustfs:9000`）后的私有 bucket 用 `proxy` |
| `ATTACHMENT_DOWNLOAD_URL_TTL` | CloudFront 签名 URL 与 S3 presigned 下载 URL 的 TTL（默认 `30m`） |
| `CLOUDFRONT_DOMAIN` | CloudFront distribution 域名——设置后公开 URL 用此 host 而非 S3 host |
| `CLOUDFRONT_KEY_PAIR_ID` | CloudFront 签名 URL 的 key pair ID |
| `CLOUDFRONT_PRIVATE_KEY` | CloudFront 私钥（PEM 格式） |

### Cookie

| 变量 | 说明 |
|----------|-------------|
| `COOKIE_DOMAIN` | 可选的 session + CloudFront cookie 的 `Domain` 属性。单 host 部署（localhost、LAN IP 或单个主机名）**留空**。仅在前端与后端位于同一注册域的不同子域时设（如 `.example.com`）。**不要用 IP 字面值**——RFC 6265 禁止 cookie `Domain` 属性用 IP，浏览器会丢弃这类 `Set-Cookie`。 |

session cookie 的 `Secure` flag 自动从 `FRONTEND_ORIGIN` 的 scheme 派生：HTTPS origin 拿 `Secure` cookie；纯 HTTP origin（LAN / 私网自部署）拿非 secure cookie，浏览器才能存。

### Server

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `PORT` | `8080` | 后端 server 端口 |
| `METRICS_ADDR` | 空 | 可选 Prometheus metrics listener，如 `127.0.0.1:9090` |
| `FRONTEND_PORT` | `3000` | 前端端口 |
| `CORS_ALLOWED_ORIGINS` | `FRONTEND_ORIGIN` 的值 | 逗号分隔的允许 origin 列表。同时管 HTTP CORS allowlist **与** WebSocket `Origin` 检查。不在列表中（且非 `localhost`）的浏览器 origin 的 WebSocket 升级会被 `403` 拒绝，实时更新停摆直到手动刷新。 |
| `LOG_LEVEL` | `info` | 日志级别：`debug`、`info`、`warn`、`error` |

### CLI / 守护进程

这些在每个用户的机器上配置，不在 server 上：

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `MULTICA_SERVER_URL` | `ws://localhost:8080/ws` | 守护进程 → server 连接的 WebSocket URL |
| `MULTICA_APP_URL` | `http://localhost:3000` | CLI 登录流程的前端 URL |
| `MULTICA_DAEMON_POLL_INTERVAL` | `3s` | 守护进程轮询任务频率 |
| `MULTICA_DAEMON_HEARTBEAT_INTERVAL` | `15s` | 心跳频率 |

按智能体覆盖：

| 变量 | 说明 |
|----------|-------------|
| `MULTICA_CLAUDE_PATH` | 自定义 `claude` 二进制路径 |
| `MULTICA_CLAUDE_MODEL` | 覆盖使用的 Claude 模型 |
| `MULTICA_CODEX_PATH` | 自定义 `codex` 二进制路径 |
| `MULTICA_CODEX_MODEL` | 覆盖使用的 Codex 模型 |
| `MULTICA_COPILOT_PATH` | 自定义 `copilot`（GitHub Copilot CLI）二进制路径 |
| `MULTICA_COPILOT_MODEL` | 覆盖 Copilot 模型（注意：GitHub Copilot 按账户权益路由模型，可能不生效） |
| `MULTICA_OPENCODE_PATH` | 自定义 `opencode` 二进制路径 |
| `MULTICA_OPENCODE_MODEL` | 覆盖使用的 OpenCode 模型 |
| `MULTICA_HERMES_PATH` | 自定义 `hermes` 二进制路径 |
| `MULTICA_HERMES_MODEL` | 覆盖使用的 Hermes 模型 |
| `MULTICA_GEMINI_PATH` | 自定义 `gemini` 二进制路径 |
| `MULTICA_GEMINI_MODEL` | 覆盖使用的 Gemini 模型 |
| `MULTICA_PI_PATH` | 自定义 `pi` 二进制路径 |
| `MULTICA_PI_MODEL` | 覆盖使用的 Pi 模型 |
| `MULTICA_CURSOR_PATH` | 自定义 `cursor-agent` 二进制路径 |
| `MULTICA_CURSOR_MODEL` | 覆盖使用的 Cursor Agent 模型 |

## 数据库配置

Multica 需要 PostgreSQL 17 with pgvector 扩展。

### 用 Docker Compose（推荐）

`docker-compose.selfhost.yml` 已包含 PostgreSQL。无需单独配置。

### 用你自己的 PostgreSQL

若偏好用既有 PostgreSQL 实例，确保 pgvector 扩展可用：

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

在 `.env` 中设 `DATABASE_URL`，并从 compose 文件移除 `postgres` service。

### 手动初始化数据库

后端启动时会自动初始化空数据库。需要只初始化或验证 schema 时：

```bash
# 用构建的二进制
./server/bin/server --init-schema-only

# 或从源码
cd server && go run ./cmd/server --init-schema-only
```

## Usage Dashboard Rollup

Usage 与 Runtime dashboard 从 `task_usage_hourly` 读，该派生表由 `rollup_task_usage_hourly()` 填充。MUL-2957 之后后端通过 DB 调度器（`sys_cron_executions`）在**进程内**跑该 rollup，每个副本都跑——全新自部署安装无需运维操作，自带 `pgvector/pgvector:pg17` 镜像即可。

### 进程内调度器工作机制

Every backend replica ticks every 30 seconds and tries to claim the current 5-minute UTC plan in `sys_cron_executions`. The unique key `(job_name, scope_kind, scope_id, plan_time)` makes the claim a single-winner contest across all replicas, so multi-instance deployments do not double-write. The handler then calls `SELECT rollup_task_usage_hourly()`; the SQL function holds advisory lock `4246` internally so manual calls cannot race the scheduler. Inspect the audit table for steady-state operation:

```sql
SELECT plan_time, status, attempt, runner_id,
       error_code, error_msg, started_at, finished_at
  FROM sys_cron_executions
 WHERE job_name = 'rollup_task_usage_hourly'
 ORDER BY plan_time DESC
 LIMIT 20;
```

## Manual Setup (Without Docker Compose)

If you prefer to build and run services manually:

**Prerequisites:** Go 1.26+, Node.js 20+, pnpm 10.28+, PostgreSQL 17 with pgvector.

```bash
# Start your PostgreSQL (or use: docker compose up -d postgres)

# Build the backend
make build

# 启动后端 server；空数据库会自动初始化
DATABASE_URL="your-database-url" PORT=8080 JWT_SECRET="your-secret" ./server/bin/server
```

前端：

```bash
pnpm install
pnpm build

# 启动前端（生产模式）
cd apps/web
REMOTE_API_URL=http://localhost:8080 pnpm start
```

## 反向代理

生产中在后端与前端前置反向代理处理 TLS 与路由。

### Caddy（推荐）

**单域名布局**——前端与后端在同一主机名（`docker-compose.selfhost.yml` 默认）：

```
multica.example.com {
    # WebSocket 路由——必须放在 catch全之前
    @multica_ws path /ws /ws/*
    handle @multica_ws {
        reverse_proxy localhost:8080 {
            flush_interval -1
        }
    }

    # 其他都 → 前端
    reverse_proxy localhost:3000
}
```

> 即使单域名，后端也要把 `FRONTEND_ORIGIN` / `CORS_ALLOWED_ORIGINS` 设为该公开 origin（如 `https://multica.example.com`）。后端默认 origin allowlist 仅 `localhost`，不设的话会用公开 URL 拒绝 WebSocket 升级返回 `403`，实时更新静默停摆。见 [LAN / 非 localhost 访问](#lan--非-localhost-访问)。

**分域名布局**——前端与后端不同主机名：

```
app.example.com {
    reverse_proxy localhost:3000
}

api.example.com {
    @multica_ws path /ws /ws/*
    handle @multica_ws {
        reverse_proxy localhost:8080 {
            flush_interval -1
        }
    }

    reverse_proxy localhost:8080
}
```

`/ws` 块里两个不显眼的点值得说明——它们都是 Caddy 前置的自部署「实时更新停摆」的常见原因：

- **`path /ws /ws/*`（不是 `/ws*`）**——裸 `handle /ws` 是精确匹配，未来 `/ws/` 下的变体会落到前端块。显然的捷径 `handle /ws*` 矫枉过正：Caddy 的 `*` 是无路径段边界的 glob，会一并捕获无关路径如 `/ws-foo`，而这是合法的工作区 URL（仅 `ws` 这个精确 slug 被保留）。显式列出 `/ws` 与 `/ws/*` 覆盖两种真实情况不过头。
- **`flush_interval -1`**——禁用响应缓冲，让 WebSocket 帧一到达就转发。否则帧会卡在 Caddy 默认 flush 窗口后，看起来像评论延迟、打字指示器丢失或「评论只在刷新页面后才出现」。

### Nginx

```nginx
# 前端
server {
    listen 443 ssl;
    server_name app.example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://localhost:3000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# 后端 API
server {
    listen 443 ssl;
    server_name api.example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # WebSocket 支持
    location /ws {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 86400;
    }
}
```

前端与后端分域名时，相应设置这些环境变量：

```bash
# 后端
FRONTEND_ORIGIN=https://app.example.com
CORS_ALLOWED_ORIGINS=https://app.example.com

# 前端（仅当通过 docker-compose.selfhost.build.yml 从源码构建 web 镜像时）
REMOTE_API_URL=https://api.example.com
NEXT_PUBLIC_API_URL=https://api.example.com
NEXT_PUBLIC_WS_URL=wss://api.example.com/ws
```

## LAN / 非 localhost 访问

默认情况下 Multica 在 `localhost` 工作。若从 LAN 上另一台机器访问（如 `http://192.168.1.100:3000`），需要告诉后端接受那个 origin：

```bash
# .env——替换为你的 server LAN IP
FRONTEND_ORIGIN=http://192.168.1.100:3000
CORS_ALLOWED_ORIGINS=http://192.168.1.100:3000
```

然后重启 stack：

```bash
docker compose -f docker-compose.selfhost.yml up -d
```

### LAN / 非 localhost 访问的 WebSocket

HTTP 请求（issue、评论、上传）在 LAN 开箱即用——Next.js rewrite 把 `/api`、`/auth`、`/uploads` 代理到后端。**WebSocket 不行**：Next.js rewrite 只转发 HTTP 请求，不转发 WebSocket 需要的 `Upgrade` 握手。在 `http://<lan-ip>:3000` 打开应用时，实时功能（聊天流、issue 实时更新、通知）会连不上，直到你做以下之一：

1. **前置反向代理（推荐）。** Nginx 或 Caddy 终止 WebSocket 升级并转发到后端 8080。见上方 [反向代理](#反向代理)——Nginx 示例已含带正确 `Upgrade` / `Connection` header 的 `location /ws { ... }` 块。代理就位后浏览器直接穿过它，无需前端重建。

2. **把 WebSocket URL 烤进 web 镜像。** 不跑反向代理时，用指向后端的 `NEXT_PUBLIC_WS_URL` 重建 web 镜像（8080 必须从浏览器可达）：

   ```bash
   # .env 中
   NEXT_PUBLIC_WS_URL=ws://<lan-ip>:8080/ws

   # 重建 web 镜像让构建时值烤入
   docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build
   ```

   `NEXT_PUBLIC_WS_URL` 是构建时变量（见 `Dockerfile.web`），所以在预构建镜像上只设 `environment:` 无效——必须用 `selfhost.build.yml` override 重建镜像。

**另需：allowlist 浏览器 origin。** 上面两个选项只修 WebSocket *升级代理*，还有第二个独立设置管连接：后端按默认仅 `localhost` 的 allowlist 校验 WebSocket `Origin` header。从任何其他 origin 打开 Multica——LAN IP **或反向代理后的公开域名**——都要在后端把 `CORS_ALLOWED_ORIGINS`（或 `FRONTEND_ORIGIN`）设为那个精确 origin 并重启，正如上方 [LAN / 非 localhost 访问](#lan--非-localhost-访问) 所示。否则升级被 `403` 拒绝：后端日志 `websocket: request origin not allowed by Upgrader.CheckOrigin`，浏览器控制台循环 `disconnected, reconnecting in 3s`，而 HTTP 请求（与手动页面刷新）仍工作，因为对页面是同源。单个值同时管 HTTP CORS 与 WebSocket origin 检查。

> **提示：** 若因其他原因需要把不同公开 API / WebSocket endpoint 硬编码进 web 镜像，用同一源构建 override：`docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build`。

## 健康检查

后端暴露公开健康端点：

```text
GET /health
 {"status":"ok"}

GET /readyz
 {"status":"ok","checks":{"db":"ok","schema":"ok"}}

GET /healthz
 同 /readyz
```

`/health` 用于基础存活/可达检查。`/readyz` 用于带依赖感知的 readiness 探针与外部监控——数据库不可用或 schema 与当前服务不一致时应失败。`/healthz` 作为 alias 保留，便于运维熟悉。

## Prometheus Metrics

后端可在独立管理 listener 上暴露 Prometheus metrics：

```bash
METRICS_ADDR=127.0.0.1:9090 ./server/bin/server
curl http://127.0.0.1:9090/metrics
```

`METRICS_ADDR` 默认空，不启动 metrics listener。公开 API 端口不服务 `/metrics`；面向互联网的部署保持如此。HTTP 请求 metrics 仅在 metrics listener 启用后才开始累计。Metrics 可暴露内部路由、流量、依赖状态与运行时健康。

Docker 或 Kubernetes 部署优先用私有抓取路径：把 metrics listener 绑到内部接口，用私有网络、allowlist、NetworkPolicy 或代理认证保护。若在容器内绑 `METRICS_ADDR=0.0.0.0:9090`，仅把该端口发布到可信网络，如 host-local 映射 `127.0.0.1:9090:9090`。

## 升级

```bash
docker compose -f docker-compose.selfhost.yml pull
docker compose -f docker-compose.selfhost.yml up -d
```

`.env` 中把 `MULTICA_IMAGE_TAG` pin 到精确 release（如 `v0.2.4`）可留在特定版本。开发阶段不支持数据库原地升级；切换到 schema 不同的版本前先备份所需数据并重建数据库。若所选 GHCR tag 尚未发布，回退到 `docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build`。
