# 自部署配置清单（面向 AI 智能体）

本文档为 AI 智能体执行用，缩为可执行检查清单。详细说明见 [SELF_HOSTING.md](SELF_HOSTING.md)（快速上手与常规运维）与 [SELF_HOSTING_ADVANCED.md](SELF_HOSTING_ADVANCED.md)（环境变量、反向代理、迁移、usage rollup 等唯一详细来源）。

## 前置

- 已装 Docker 与 Docker Compose
- 已装 Homebrew（用于 CLI）
- PATH 上至少有一个 AI 智能体 CLI：`claude` 或 `codex`

## 安装

```bash
# 装 CLI + provision 自部署 server
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash -s -- --with-server

# 配置 CLI 指向 localhost、认证、启动守护进程
multica setup self-host
```

等 server 输出 `✓ Multica server is running and CLI is ready!` 后再跑 `multica setup self-host`。

**预期结果：**
- 前端：http://localhost:3000
- 后端：http://localhost:8080
- `multica` CLI 已装并配为 localhost

## 备选：手动配置

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
make selfhost
brew install multica-ai/tap/multica
multica setup self-host
```

`multica setup self-host` 会：
1. 配置 CLI 连接 localhost:8080 / localhost:3000
2. 打开浏览器做账号/密码登录
3. 自动发现工作区
4. 后台启动守护进程

## 验证

```bash
multica daemon status
```

应显示 `running` 与检测到的智能体。

## 生产演示 / 可观测性验证

自部署标记为内部团队使用或领导演示就绪前，从产品、API、日志、数据库收集证据。完整中文 runbook 见：

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

然后打开 `/{workspaceSlug}/training?view=demo-dashboard` 确认：

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

## 停止

```bash
# 停守护进程
multica daemon stop

# 停所有 Docker 服务
cd multica
make selfhost-stop
```

## 自定义端口

默认端口（8080/3000）被占用时：

1. 编辑 `.env`，改 `PORT` 与 `FRONTEND_PORT`
2. 跑 `make selfhost`
3. 跑 `multica setup self-host --port <PORT> --frontend-port <FRONTEND_PORT>`

## 故障排查

- **后端未就绪：** `docker compose -f docker-compose.selfhost.yml logs backend`
- **前端未就绪：** `docker compose -f docker-compose.selfhost.yml logs frontend`
- **守护进程问题：** `multica daemon logs`
- **健康检查：** `curl http://localhost:8080/health` 查存活，`curl http://localhost:8080/readyz` 查带依赖感知的 readiness

环境变量、反向代理、迁移、usage rollup 等详细说明见 [SELF_HOSTING_ADVANCED.md](SELF_HOSTING_ADVANCED.md)。
