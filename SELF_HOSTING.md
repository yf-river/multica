# 自部署指南

几分钟内在自己的基础设施上部署 Multica。

## 架构

| 组件 | 说明 | 技术栈 |
|-----------|-------------|------------|
| **后端** | REST API + WebSocket server | Go（单二进制） |
| **前端** | Web 应用 | Next.js 16 |
| **数据库** | 主数据存储 | PostgreSQL 17 with pgvector |

每个本地跑 AI 智能体的用户还需在自己的机器上装 **`multica` CLI** 并运行**智能体守护进程**。

## 快速安装（推荐）

两条命令搞定一切——server、CLI、配置：

```bash
# 1. 装 CLI 并 provision 自部署 server
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --with-server

# 2. 配置 CLI、认证、启动守护进程
multica setup self-host
```

这会装 `multica` CLI、checkout 最新自部署素材、从 GHCR 拉取官方 Multica 镜像，并配好 localhost。

打开 http://localhost:3000 用账号名与密码登录。详情见 [步骤 2 — 登录](#step-2--登录)。

> **前置：** 需安装 Docker 与 Docker Compose。脚本会检查并给出安装链接。
>
> **只要 CLI？** 若自部署 server 已在跑，且仅需在 macOS/Linux 机器上装 CLI，用 Homebrew：
>
> ```bash
> brew install multica-ai/tap/multica
> ```

---

## 分步配置（备选）

若偏好手动跑每一步：

### 步骤 1 — 启动 server

**前置：** Docker 与 Docker Compose。

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
make selfhost
```

`make selfhost` 自动从示例创建 `.env`、生成随机 `JWT_SECRET`，并通过 Docker Compose 启动所有服务。

默认从 GHCR 拉最新稳定 release 镜像。要从当前 checkout 构建后端/前端，跑 `make selfhost-build`。若所选 GHCR tag 尚未发布，`make selfhost` 会提示回退到 `make selfhost-build`。`make selfhost-build` 用本地 `multica-backend:dev` / `multica-web:dev` tag，不会覆盖拉取的 `:latest` 镜像。

就绪后：

- **前端：** http://localhost:3000
- **后端 API：** http://localhost:8080

> **提示：** 若偏好手动跑 Docker Compose 步骤，见下方 [手动 Docker Compose 配置](#手动-docker-compose-配置)。

### 步骤 2 — 登录

浏览器打开 http://localhost:3000，用账号名与密码登录。开启 signup 时，首次成功登录创建账号；后续登录需用同一密码。

`ALLOW_SIGNUP` 与 `DISABLE_WORKSPACE_CREATION` 的变更需重启后端 / compose stack 后生效。Web UI 在运行时从 `/api/config` 读这两个值，无需 web 重建。锁定工作区创建的推荐顺序见 [高级配置 → 注册控制](SELF_HOSTING_ADVANCED.md#注册控制可选)。

### 步骤 3 — 装 CLI 并启动守护进程

守护进程跑在你本地机器上（不在 Docker 内）。它检测已装的 AI 智能体 CLI、向 server 注册，并在智能体被分配任务时执行。

每个想本地跑 AI 智能体的团队成员需：

#### a) 装 CLI 与一个 AI 智能体

```bash
brew install multica-ai/tap/multica
```

还需至少装一个 AI 智能体 CLI：
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)（PATH 中的 `claude`）
- [Codex](https://github.com/openai/codex)（PATH 中的 `codex`）
- [GitHub Copilot CLI](https://docs.github.com/en/copilot)（PATH 中的 `copilot`）
- [OpenClaw](https://github.com/openclaw/openclaw)（PATH 中的 `openclaw`）
- [OpenCode](https://github.com/anomalyco/opencode)（PATH 中的 `opencode`）
- [Hermes](https://github.com/NousResearch/hermes)（PATH 中的 `hermes`）
- Gemini（PATH 中的 `gemini`）
- [Pi](https://pi.dev/)（PATH 中的 `pi`）
- [Cursor Agent](https://cursor.com/)（PATH 中的 `cursor-agent`）
- Kimi（PATH 中的 `kimi`）
- Kiro CLI（PATH 中的 `kiro-cli`）

#### b) 一键配置

```bash
multica setup self-host
```

自动：
1. 配置 CLI 连接 `localhost`（端口 8080/3000）
2. 打开浏览器认证
3. 发现你的工作区
4. 后台启动守护进程

自定义域名部署：

```bash
multica setup self-host --server-url https://api.example.com --app-url https://app.example.com
```

验证守护进程在跑：

```bash
multica daemon status
```

> **备选：** 偏好手动步骤见下方 [手动 CLI 配置](#手动-cli-配置)。

### 步骤 4 — 验证并开始使用

1. 在 web 应用 http://localhost:3000 打开你的工作区
2. 进入 **设置 → 运行时**——应看到你的机器已列出
3. 进入 **设置 → 智能体**，新建一个智能体
4. 创建 issue 并分配给你的智能体——它会自动接手任务

---

## Kubernetes 部署（备选）

若已有 Kubernetes 集群，可改用发布的 OCI Helm chart（`oci://ghcr.io/multica-ai/charts/multica`）或源码 chart（[`deploy/helm/multica/`](deploy/helm/multica/)）部署 Multica。目标是一个典型的 k3s / k8s 环境，带 Ingress controller 和默认 `ReadWriteOnce` StorageClass——按 k3s + Traefik + `local-path` 编写，其他集群小调即可。

chart 在目标 namespace 创建：

- `multica-postgres` — `pgvector/pgvector:pg17`，背后是 10Gi PVC
- `multica-backend` — Go API/WS server。默认背后是 5Gi `ReadWriteOnce` uploads PVC；配置了 S3（`backend.config.s3Bucket`）且不想要该 PVC 时设 `backend.uploads.persistence.enabled=false`
- `multica-frontend` — Next.js standalone server
- 两个 `Ingress`：一个 web host，一个 backend host
- `multica-config` ConfigMap（从 `values.yaml` 渲染）

`multica-secrets` Secret **不由 chart 管理**——用 `kubectl` 创建一次，真实值不必进 git。

> **每个 namespace 一个 release：** 预构建的 `multica-web` 镜像在构建时把 `REMOTE_API_URL=http://backend:8080` 烤进去，所以 chart 提供一个名为 `backend` 的 ExternalName Service。因该名无前缀，每个 namespace 只能跑一个 Multica release，`helm install` 在已存在 `Service/backend` 时会失败（传 `--take-ownership`，或用专用 namespace）。若你构建了改 `REMOTE_API_URL` 的 web 镜像，设 `frontend.compatibility.backendAlias: false` 去掉 alias。

> **前置：** `kubectl` 与 `helm`（v3.13+ 支持 `--take-ownership`，或 v4+）已配置好目标集群、一个 Ingress controller（Traefik / NGINX）、一个默认 StorageClass。

### 步骤 1 — 把主机名指向集群

chart 默认 `multica.dev.lan`（web）与 `api.multica.dev.lan`（backend）。选其一：

- **`/etc/hosts`**（每台需访问的机器：开发者笔记本 + 跑守护进程的机器）：

  ```text
  192.168.1.206  multica.dev.lan api.multica.dev.lan
  ```

  把 `192.168.1.206` 换成你的 Ingress controller Service 可达的任一节点 IP。

- **本地 DNS**（Pi-hole、Unbound 等）：为两个主机名加 A 记录指向集群 Ingress IP。

要用不同主机名，安装时覆盖对应 value（见 [步骤 4](#step-4--安装-chart)）——`ingress.frontend.host`、`ingress.backend.host`，加上 `backend.config.appUrl`、`backend.config.frontendOrigin`、`backend.config.localUploadBaseUrl`。

### 步骤 2 — 创建 namespace

```bash
kubectl create namespace multica
```

### 步骤 3 — 创建 `multica-secrets` Secret

chart 按名引用该 Secret。创建一次，值随机：

```bash
kubectl -n multica create secret generic multica-secrets \
  --from-literal=JWT_SECRET="$(openssl rand -hex 32)" \
  --from-literal=POSTGRES_PASSWORD="$(openssl rand -hex 16)" \
  --from-literal=CLOUDFRONT_PRIVATE_KEY=""
```

可选值现在留空——后续再填（见 [步骤 5 — 登录](#step-5--登录)）。

### 步骤 4 — 安装 chart

```bash
helm install multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica
```

发布的 chart 版本去掉 Git tag 的前导 `v`。例如 release tag `v0.3.5` 发布 chart 版本 `0.3.5`；chart 默认后端与前端镜像 tag 为 `v0.3.5`。

覆盖默认值：导出 chart values、编辑、用 `-f` 传：

```bash
helm show values oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> > my-values.yaml
# 编辑 my-values.yaml——如改 ingress host、镜像 tag、资源限制
helm install multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

从 checkout 开发时，用本地 chart 路径：

```bash
helm install multica deploy/helm/multica -n multica
```

观察 pod 起来：

```bash
kubectl -n multica get pods -w
```

冷集群上后端可能 `Running` 但不 `Ready` 持续几分钟，等 PostgreSQL 并跑迁移——startupProbe 吸收这段时间，pod 不会重启。后端 `Ready` 后迁移完成，`/healthz` 返回 OK：

```bash
curl -H "Host: api.multica.dev.lan" http://<ingress-ip>/healthz
# {"status":"ok","checks":{"db":"ok","migrations":"ok"}}
```

然后浏览器打开 http://multica.dev.lan。

### 步骤 5 — 登录

chart 默认 `APP_ENV=production`（在 `values.yaml` 的 `backend.config.appEnv`）。打开前端用账号名与密码登录。

`ALLOW_SIGNUP`、`ALLOWED_ACCOUNTS`、`DISABLE_WORKSPACE_CREATION` 在 `values.yaml` 的 `backend.config.*` 下（`allowSignup`、`allowedAccounts`、`disableWorkspaceCreation`）。`helm upgrade` 后 ConfigMap hash 变化，后端 pod 自动滚动；web UI 在运行时从 `/api/config` 读 signup/工作区创建 flag，无需 web 重建。

### 步骤 6 — 装 CLI 并启动守护进程

守护进程跑在你本地机器上，不在集群里。按上方 [步骤 3](#step-3--装-cli-并启动守护进程) 装 CLI 与一个 AI 智能体，然后把 CLI 指向你的 Ingress 主机名：

```bash
multica setup self-host \
  --server-url http://api.multica.dev.lan \
  --app-url http://multica.dev.lan
```

确保跑守护进程的机器有 [步骤 1](#step-1--把主机名指向集群) 中相同的 `/etc/hosts`（或 DNS）条目。

### 更新

values 仍用可变的 `latest` 镜像 tag 时，不换 chart 版本拉最新镜像：

```bash
kubectl -n multica rollout restart deploy/multica-backend deploy/multica-frontend
```

升级到特定 Multica release 时升级到对应 chart 版本。发布的 chart 默认 app 镜像为对应 Git tag：

```bash
helm upgrade multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

需要独立于 chart 版本覆盖 app 镜像时，在 values 文件设镜像 tag：

```yaml
images:
  backend:
    tag: v0.2.4
  frontend:
    tag: v0.2.4
```

然后用同一升级命令带 `-f my-values.yaml`：

```bash
helm upgrade multica oci://ghcr.io/multica-ai/charts/multica \
  --version <chart-version> \
  -n multica \
  -f my-values.yaml
```

升级出问题时回滚：

```bash
helm -n multica rollback multica
```

> **从 `v0.3.4` 升级到 `v0.3.5+` 失败、提示 `refusing to drop legacy daily rollups: ...`？** 完整恢复流程见 [高级配置 → Usage Dashboard Rollup](SELF_HOSTING_ADVANCED.md#usage-dashboard-rollup)。MUL-2957 之后 `migrate up` 会自动跑幂等的 monthly-slice backfill，正常情况一次 `helm upgrade` + 后端滚动即可完成。

### 拆除

```bash
# 移除工作负载但保留 PVC 与 Secret
helm -n multica uninstall multica

# 全部清空，含 PostgreSQL 数据与 uploads
kubectl delete namespace multica
```

---

## Usage Dashboard Rollup

Usage / Runtime dashboard 从派生的 `task_usage_hourly` 表读，由 `rollup_task_usage_hourly()` 填充。MUL-2957 之后后端通过 DB 调度器（`sys_cron_executions`）在**进程内**跑该 rollup，每个副本都跑；全新自部署安装无需运维操作，自带 `pgvector/pgvector:pg17` 镜像即可——**不需要**换带 `pg_cron` 的镜像、注册外部 cron job、设 systemd timer、跑 Kubernetes `CronJob`。

完整参考（审计表语义、advisory lock 4246、standalone backfill 命令、flag 说明、`v0.3.4 → v0.3.5+` 迁移 auto-hook）见 [高级配置 → Usage Dashboard Rollup](SELF_HOSTING_ADVANCED.md#usage-dashboard-rollup)。

## 停止服务

通过安装脚本装的：

```bash
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --stop
```

手动 clone 仓库的：

```bash
# 停止 Docker Compose 服务（后端、前端、数据库）
make selfhost-stop

# 停止本地守护进程
multica daemon stop
```

## 切到 Multica Cloud

若你之前自部署、想把 CLI 切到 [Multica Cloud](https://multica.ai)：

```bash
multica setup
```

这会把 CLI 重配为 multica.ai、重新认证、重启守护进程。覆盖既有配置前会提示。

> 本地 Docker 服务不受影响。若不再需要请单独停掉。

## 升级

```bash
docker compose -f docker-compose.selfhost.yml pull
docker compose -f docker-compose.selfhost.yml up -d
```

`.env` 中把 `MULTICA_IMAGE_TAG` pin 到精确版本（如 `v0.2.4`）可留在特定 release。迁移在后端启动时自动跑。若所选 GHCR tag 尚未发布，回退到 `make selfhost-build` 或 `docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --build`。

> **从 `v0.3.4` 升级到 `v0.3.5+`？** 完整说明见 [高级配置 → Usage Dashboard Rollup](SELF_HOSTING_ADVANCED.md#usage-dashboard-rollup)。MUL-2957 之后 `migrate up` 自动跑 backfill，单次调用即可完成升级。

---

## 手动 Docker Compose 配置

偏好手动跑 Docker Compose 而非 `make selfhost`：

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
cp .env.example .env
```

编辑 `.env`——至少改 `JWT_SECRET`：

```bash
JWT_SECRET=$(openssl rand -hex 32)
```

然后启动一切：

```bash
docker compose -f docker-compose.selfhost.yml pull
docker compose -f docker-compose.selfhost.yml up -d
```

## 手动 CLI 配置

偏好逐步配置 CLI 而非 `multica setup`：

```bash
# 把 CLI 指向本地 server
multica config set server_url http://localhost:8080
multica config set app_url http://localhost:3000

# 登录（打开浏览器）
multica login

# 启动守护进程
multica daemon start
```

带 TLS 的生产部署：

```bash
multica config set app_url https://app.example.com
multica config set server_url https://api.example.com
multica login
multica daemon start
```

## 高级配置

环境变量、无 Docker 的手动配置、反向代理、数据库配置等见 [高级配置指南](SELF_HOSTING_ADVANCED.md)。
