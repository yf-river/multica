# 仓库指南

本文件为在本仓库工作的 AI 智能体提供入口指引。

> **唯一真相来源：** 本文件只是一个简短的指针文档。
> 所有权威的架构、编码规则、命令与约定都位于项目根目录的 **CLAUDE.md**。请先读它。

## 快速入口

- **工程规范、架构、命令、状态管理、测试规则、提交规则**：见 [CLAUDE.md](CLAUDE.md)。
- **本地开发流程、worktree、数据库、测试**：见 [CONTRIBUTING.md](CONTRIBUTING.md)。
- **CLI 安装 SOP（供 AI 智能体执行）**：见 [CLI_INSTALL.md](CLI_INSTALL.md)。
- **CLI 与守护进程完整参考**：见 [CLI_AND_DAEMON.md](CLI_AND_DAEMON.md)。
- **自部署快速上手与运维**：见 [SELF_HOSTING.md](SELF_HOSTING.md)。
- **自部署高级配置（环境变量、反向代理、迁移、usage rollup）**：见 [SELF_HOSTING_ADVANCED.md](SELF_HOSTING_ADVANCED.md)。
- **自部署 AI 智能体执行清单**：见 [SELF_HOSTING_AI.md](SELF_HOSTING_AI.md)。

## 架构概览

Go 后端 + monorepo 前端（pnpm workspaces + Turborepo）+ 共享包。

- `server/` — Go 后端（Chi router、sqlc、gorilla/websocket）
- `apps/web/` — Next.js 前端（App Router）
- `packages/core/` — 无头业务逻辑（Zustand store、React Query hook、API client）
- `packages/ui/` — 原子级 UI 组件（shadcn/Base UI，零业务逻辑）
- `packages/views/` — 共享业务页面/组件
- `packages/tsconfig/` — 共享 TypeScript 配置

## 常用命令

```bash
make dev              # 自动配置 + 启动一切
pnpm typecheck        # TypeScript 检查
pnpm test             # TS 单测（Vitest）
make test             # Go 测试
make check            # 完整验证流水线
```

完整命令参考见 [CLAUDE.md](CLAUDE.md)。

## 验证策略

开发期运行 changed-aware 检查，提交前按影响面验证，完整发布或全栈变更再运行 `make check`。详见 [CLAUDE.md → 验证策略](CLAUDE.md)。

## 联调环境运维要点

- **联调 postgres 在远端**：`.run/env/goal-test-int.env` 的 `DATABASE_URL` 指向 `9.134.129.162:5432`，**不是本机 docker 容器**。本机 `multica_*-postgres-1` 容器与联调环境无关。
- **查联调数据优先用 API**：`curl http://9.134.129.162:18762/api/workspaces`（先 `POST /auth/login` 拿 token）。不要直接查本机 postgres——那会读到空库，误判为「无数据」。
- **goal-test 默认工作区是 `ai-studio`**：`GOAL_TEST_INT_WORKSPACE=ai-studio`。删除该工作区会导致 `make goal-test-e2e-preflight` 与部署验证失败，需同步改环境变量或重建。
