# CLAUDE.md

本文件为在本仓库中工作的 AI 智能体（Claude Code、Codex 等）提供工程规范。

## 语言约定（重要）

- 根目录 Markdown 文档与面向用户的运维配置注释统一使用**中文**。
- **Go / TypeScript 源代码注释仍使用英文**，保持与现有代码库一致。
- Makefile 章节/目标说明、`make help` 输出、错误与进度信息使用中文；命令、参数与必须精确匹配的输出保持原样。
- JSON / YAML 键名、环境变量名、镜像名、服务名、文件名、正则表达式、命令参数、锁文件数据**不修改**。

## 命名与文案规范

**代码命名、i18n 翻译词汇表、中文文案风格**的唯一权威来源是文档站点：

- **`apps/docs/content/docs/developers/conventions.mdx`**（英文）
- **`apps/docs/content/docs/developers/conventions.zh.mdx`**（中文）

在以下情况之前先读该文档：

- 编写或修改翻译（`packages/views/locales/`）
- 命名新的路由、包、文件、数据库列或 TS 类型
- 编写中文产品文案（UI 文案、错误信息、文档）

旧版 `packages/views/locales/glossary.md` 现仅是跳转到上述文档的桩文件，不要依赖它。

## 项目上下文

Multica 是一个 AI 原生任务管理平台——类似 Linear，但 AI 智能体是一等公民。

- 智能体可以被分配 issue、创建 issue、发表评论、修改状态
- 支持本地守护进程与固定机器的智能体运行时
- 面向 2-10 人的 AI 原生团队

## Goal 执行档案

生成长 Codex goal 或在本仓库运行 goal 时使用以下档案。

- 主仓库：`/data/ida/multica`
- 决策账本：`/data/ida/docs/tapd/20260605-ai设计/全流程sop设计-v4`
- 联调环境：`http://9.134.129.162:13682`
- 联调登录页：`http://9.134.129.162:13682/login`
- 默认验收账号：`develop`
- 本地 E2E 脚本默认验收密码：`develop123`；若设置了对应环境变量，优先使用环境变量。
- 默认范围：先验证联调环境。仅在用户明确要求时才触碰生产环境。
- AI Studio 环境文件：`.run/env/goal-test-int.env`、`.run/env/goal-test-prod.env`
- AI Studio 日志：`.run/int-server.log`、`.run/int-web.log`、`.run/int-daemon.log`、`.run/prod-server.log`、`.run/prod-web.log`、`.run/prod-daemon.log`
- 证据目录：`artifacts/acceptance/` 与 `artifacts/acceptance/ui-audit-screenshots/`

常用 goal-test 命令：

```bash
make goal-test-verify-env
make goal-test-verify-logs
make goal-test-e2e-preflight
make goal-test-smoke
make goal-test-fast-check
make goal-test-smart-verify MODE=precommit
make goal-test-smart-verify MODE=final DRY_RUN=1
make goal-test-ui-audit
make goal-test-ui-acceptance
make goal-test-dashboard-click-audit
make goal-test-real-agent-e2e
GOAL_TEST_TOKEN_OPTIMIZER=rtk make goal-test-smart-verify MODE=dev
GOAL_TEST_TOKEN_OPTIMIZER=rtk make goal-test-smart-verify MODE=precommit
pnpm exec playwright test e2e/production-acceptance.spec.ts --project=chromium
```

复杂 goal-test 交付时，主控模型按团队当前在用配置选择高能力模型；生成本地 goal 提示词或只读探索可使用更低成本模型。本地运行时优先使用 Codex，除非用户明确选择其他运行时。

AI Studio 验收必须包含真实浏览器 UI 检查、E2E/API 数据闭环、性能证据、当前部署 `.run` 日志窗口扫描、决策账本更新，以及一次提交或明确的不提交理由。联调 Playwright 运行先执行 `make goal-test-e2e-preflight`，再使用 `.run/env/goal-test-int.env` 中的项目变量加上 `PLAYWRIGHT_BASE_URL=http://9.134.129.162:13682`；不要只依赖 `E2E_BASE_URL`。E2E 测试可复用默认账号/工作区，但必须通过公开 API/UI 创建自己的业务数据，不要依赖已有的 prompt、issue 或素材。优先使用 `TestApiClient.createPromptForE2E` 和 `TestApiClient.createIssueForE2E` 创建唯一命名的 fixture。

广泛的 goal-test UI 或训练审计前先跑 `make goal-test-smoke`。`make goal-test-ui-audit` 和 `make goal-test-training-performance-audit` 已在 JSON 证据中包含 smoke 与当前部署标记日志扫描。部署前会把旧的 `.run/*-{server,web,daemon}.log` 归档到 `.run/log-archive/`，再写入新的标记窗口。

### 验证策略（速优优先）

- 开发期：优先 `make goal-test-fast-check` 或 `make goal-test-smart-verify MODE=dev`；不要每次小改动都跑部署、广泛 UI 审计或训练性能审计。
- 提交前：用 `make goal-test-smart-verify MODE=precommit` 做 changed-aware 聚焦检查；仅在切片稳定、需要账本证据时才使用 `MODE=final`。
- 每个 60-120 分钟切片至多跑一次 `make goal-test-deploy-dev`；仅当后端启动、迁移、环境文件、构建配置或远端环境本身变更时才提前重新部署。
- 若 `MODE=final` 发现当前提交已部署且无部署相关文件变更，必须验证现有环境而非重新部署。
- 同一条 Playwright 或审计命令失败两次后，停止盲目重跑。检查 trace、screenshot、console/pageerror 与 `.run/int-{server,web,daemon}.log`，修复根因后再重跑最小的失败 grep/spec。
- `scripts/goal-test-smart-verify.mjs` 把命令耗时写入 `artifacts/acceptance/command-timings.jsonl`；启动下一轮长续跑前先查该文件识别重复的昂贵门禁。
- token 优化是平衡的，不是全局的。`scripts/goal-test-smart-verify.mjs` 可通过 `scripts/goal-test-command-wrapper.mjs` 对高噪声命令做摘要，但必须把原始输出完整保留在 `artifacts/acceptance/raw-command-logs/`，并在 `command-timings.jsonl` 中记录原始/摘要字节数。
- 不要自动压缩发现类或精确证据类命令：`rg`、`find`、`ls`、`git diff`、失败测试堆栈窗口、部署失败窗口、任何 `panic`/`FATAL`/`ERROR` 窗口必须可直接查看。重跑宽门禁前先看原始日志路径。
- RTK 仅通过 `GOAL_TEST_TOKEN_OPTIMIZER=rtk` 显式开启；当 `rtk` 已安装且命令在安全白名单内时，包装器向 `rtk rewrite` 请求具体的 `rtk ...` 命令并执行该重写后的命令。若 RTK 缺失、不安全或拒绝重写，回退到保留原始输出的内置摘要。除非用户明确要求，不要为本仓库安装全局 RTK hook。

### 长会话治理

- 每 3-5 条决策账本条目后、每个 wave 结束时、最终验收前后、自动进入新主题前，输出一次 checkpoint。checkpoint 必须包含：近似完成百分比、当前分支、已提交 commit、已部署 commit、未提交 diff、运行中的命令、证据路径、未结 P0/P1、下一步建议命令、下一切片收益/成本/风险、明确的「不要接着做」事项。
- 启动下一个自动 P0/P1 切片前，写明为什么现在值得做。包含预期用户/产品收益、验证成本、可能风险、是否需要部署/E2E、跳过该切片会推迟什么。
- 重跑或扩大范围前先对失败分类。默认分类：产品缺口、测试脚本 bug、fixture 或脏数据噪声、外部运行时或沙箱不稳定、部署不匹配、日志噪声、性能回退、证据解析错误。
- 仅在证明或证伪某个假设时才允许脏部署。账本证据、最终验收、演示证据必须来自干净的已提交部署，否则把「无法干净部署」记为 blocker。
- 验收数据必须通过公开 UI/API/CLI 用唯一命名创建。优先归档、隐藏、禁用或测试标记，而非删除证据；仅在测试合同明确允许时才删除。复用证据必须说明为何仍然有效、什么会让它失效。
- 区分 `demo-ready` 与 `production-complete`。联调 UI/API/E2E 证据足够展示时可视为 demo-ready；但当深度 Opik 功能、权限、安全、迁移、稳定性、成本控制或完整最终验收仍有未结项时，不算 production-complete。

### 门禁适用范围

- 开发门禁：`make goal-test-fast-check` 或 `make goal-test-smart-verify MODE=dev`；编辑中与广泛验证前使用。
- 提交门禁：`make goal-test-smart-verify MODE=precommit`；提交聚焦代码变更前使用。
- 部署/日志门禁：`make goal-test-deploy-dev`，然后 `make goal-test-verify-env` 和 `make goal-test-verify-logs`；当代码需要联调环境证据时使用。
- UI 验收门禁：`make goal-test-ui-acceptance`；用于广泛的已部署 UI 行为，不强制跑每条长守护进程路径。
- 宽门禁：`make goal-test-smoke` 或 `make goal-test-ui-acceptance`；当当前 UI、可观测性与已部署行为必须一起展示时，用于 wave 或里程碑收尾。
- 训练 UI 门禁：`make goal-test-training-performance-audit`；用于当前训练页面、路由面板、数据集、测试套件、评估记录或性能敏感的训练导航。
- 真实智能体门禁：`make goal-test-real-agent-e2e`；当真实智能体派发或跨项目运行时行为变更时使用。
- 性能门禁：`make goal-test-dashboard-click-audit` 和 `make goal-test-public-training-performance-audit`；仅当受影响面或里程碑需要该证据时使用。

## 架构

**Go 后端 + monorepo 前端（pnpm workspaces + Turborepo）+ 共享包。**

- `server/` — Go 后端（Chi 路由、sqlc 生成 DB 代码、gorilla/websocket 实时通信）
- `apps/web/` — Next.js 前端（App Router）
- `packages/core/` — 无头业务逻辑（零 react-dom）
- `packages/ui/` — 原子级 UI 组件（零业务逻辑）
- `packages/views/` — 共享业务页面/组件（零 `next/*` 导入）
- `packages/tsconfig/` — 共享 TypeScript 配置

*共享原则*一节说明了为共享目的各模块放在哪里——读一次即可。

### 关键架构决策

**Internal Packages 模式** — 所有共享包都导出原始 `.ts`/`.tsx` 文件（无预编译）。消费方应用的 bundler 直接编译，零配置 HMR，即时跳转定义。

**依赖方向：** `views/ → core/ + ui/`。core 与 ui 互相独立。任何包都不得导入 `next/*` 或应用专属代码。

**平台桥：** `packages/core/platform/` 提供 `CoreProvider`——初始化 API client、auth/workspace store、WS 连接和 QueryClient。每个应用用 `<CoreProvider>` 包裹根组件，并自行提供 `NavigationAdapter` 处理路由。

**pnpm catalog** — `pnpm-workspace.yaml` 通过 `catalog:` 固定版本。所有共享依赖使用 `catalog:` 引用以保证各包版本一致。新增共享依赖（含测试依赖）时先加到 catalog。

### 状态管理

架构依赖服务端状态与客户端状态的严格分离。混用是最常见的架构破坏方式。

- **TanStack Query 拥有所有服务端状态。** issue、用户、工作区、inbox——任何从 API 取的数据都活在 Query 缓存里。WS 事件通过 invalidation 保持新鲜；不要轮询，不要用 `staleTime` 绕路。
- **Zustand 拥有所有客户端状态。** UI 选择、过滤器、草稿、modal 状态、导航历史。store 放在 `packages/core/`（绝不放 `packages/views/`），这样才能共享。
- **React Context** 留给横切平台管道——`WorkspaceIdProvider`、`NavigationProvider`。不要拿它装通用状态。
- **auth 和 workspace store 是仅有的允许直接调用 `api.*` 的 store**，因为它们管理着 query 能跑之前必须存在的关键状态。通过工厂函数 + 依赖注入创建，由平台层注册。

**硬性规则——架构一致性靠它们维持：**

- **绝不把服务端数据复制到 Zustand。** 来自 API 的数据属于 Query 缓存。复制到 store 会造成两个真相来源并最终漂移。
- **工作区作用域 query 必须以 `wsId` 为 key。** 这样切换工作区是自动的——cache key 变了，正确数据出现，无需手动 invalidation。
- **mutation 默认乐观。** 本地先应用变更，发请求，失败回滚，settle 时 invalidate。用户不该等服务端。
- **WS 事件 invalidate query——绝不直接写 store。** 这让缓存成为唯一真相来源，避免竞态。
- **跨重启值得保留的才持久化**（用户偏好、草稿、tab 布局）。**不要持久化临时 UI 状态**（modal 开关、瞬时选择）或服务端数据。

**Zustand 常见坑：**

- selector 必须返回稳定引用。每次调用都返回新建对象或数组（如 `s => ({ a: s.a, b: s.b })` 或 `s => s.items.map(...)`）会触发无限重渲染。要么分别 select 基础类型，要么用 shallow 比较。
- 需要 workspace 上下文的 hook 应接受 `wsId` 作为参数，而不是内部调用 `useWorkspaceId()`——这样它能在 `WorkspaceIdProvider` 之外工作（例如在工作区加载前渲染的 sidebar）。

## 共享原则

monorepo 分为两个共享区：

- **Web 与桌面**通过 `packages/core/`、`packages/ui/`、`packages/views/` 共享业务逻辑、组件、hook、store 与 view。沿用既有模式即可。

## 命令

```bash
# 一键开发（自动配置 + 启动一切）
make dev              # 自动创建 env、安装依赖、启动 DB、初始化 schema、启动应用

# 显式配置与运行（若偏好分开）
make setup            # 首次：确保共享 DB、创建应用 DB、初始化 schema
make start            # 同时启动后端 + 前端
make stop             # 停止当前 checkout 的应用进程
make db-down          # 停止共享 PostgreSQL 容器

# 前端（所有命令走 Turborepo）
pnpm install
pnpm dev:web          # Next.js dev server（端口 3000）
pnpm build            # 构建所有前端应用
pnpm typecheck        # TypeScript 检查（所有包 + 应用，经 turbo）
pnpm lint             # ESLint
pnpm test             # TS 测试（Vitest，所有包 + 应用，经 turbo）

# 后端（Go）
make server           # 仅运行 Go server（端口 8080）
make daemon           # 运行本地守护进程
make build            # 构建 server + CLI 二进制到 server/bin/
make cli ARGS="..."   # 运行 multica CLI（如 make cli ARGS="config"）
make test             # Go 测试
make sqlc             # 修改 server/pkg/db/queries/ 下的 SQL 后重新生成 sqlc 代码
make schema-init      # 初始化空数据库或验证当前 schema

# 跑单个 TS 测试（任何含 test 脚本的包都适用）
pnpm --filter @multica/views exec vitest run auth/login-page.test.tsx
pnpm --filter @multica/core exec vitest run runtimes/version.test.ts
pnpm --filter @multica/web exec vitest run app/\(auth\)/login/page.test.tsx

# 跑单个 Go 测试
cd server && go test ./internal/handler/ -run TestName

# 跑单个 E2E 测试（需后端 + 前端运行中）
pnpm exec playwright test e2e/tests/specific-test.spec.ts

# shadcn — 配置在 packages/ui/components.json（Base UI 变体，base-nova 风格）
pnpm ui:add badge                # 添加组件到 packages/ui/components/ui/

# 基础设施
make db-up            # 启动共享 PostgreSQL（pgvector/pg17 镜像）
make db-down          # 停止共享 PostgreSQL
make db-reset         # 删除 + 重建当前 env 的 DB，再初始化 schema（仅本地；先停后端）
```

### CI 要求

CI 在 Node 22、Go 1.26.1 上运行，配合 `pgvector/pgvector:pg17` PostgreSQL service。见 `.github/workflows/ci.yml`。

### Worktree 支持

所有 checkout 共享一个 PostgreSQL 容器。隔离在数据库级别——每个 worktree 通过 `.env.worktree` 拿到独立 DB 名和端口。主 checkout 用 `.env`。

`make dev` 自动检测 worktree 并处理一切。显式控制：

```bash
make worktree-env       # 生成带独立 DB/端口的 .env.worktree
make setup-worktree     # 用 .env.worktree 配置
make start-worktree     # 用 .env.worktree 启动
```

## 编码规则

- TypeScript 严格模式已开启；保持类型显式。
- Go 代码遵循标准 Go 规范（gofmt、go vet）。
- **代码注释仅用英文。**
- 优先使用既有模式/组件，不要引入并行的抽象。
- 除非用户明确要求向后兼容，**不要**为内部、非边界代码（同一包内函数互相调用、组件读自身状态、store helper 等）添加兼容层、回退路径、双写逻辑、legacy 适配器或临时 shim。
- 该规则**不**适用于 API 边界：桌面应用无法假设它对话的后端与构建时形状一致（旧桌面安装会活过任何一次后端构建）。API 响应处理必须遵守下方 **API 响应兼容** 规则——那是防御性边界，不是 legacy shim。
- 若一个流程或 API 正在被替换且产品尚未上线，优先移除旧路径，而非保留新旧两套行为。
- 除非任务需要，避免大范围重构。
- 新的全局（pre-workspace）路由必须使用单个词（`/login`、`/inbox`）或 `/{noun}/{verb}` 对（`/workspaces/new`）。绝不加连字符的多词根路由（`/new-workspace`、`/create-team`）——它们会与常见用户工作区名冲突，迫使你做无休止的保留 slug 审计。保留名词（`workspaces`）即自动保护整棵 `/workspaces/*` 子树。
- 保留 slug 列表只活在**一处**：`server/internal/handler/reserved_slugs.json`。Go 侧 embed 该 JSON；`packages/core/paths/reserved-slugs.ts` 由 `pnpm generate:reserved-slugs` 从中生成。改 JSON，跑生成器，两者一起提交。CI 会重跑生成器并在漂移时失败，所以陈旧的 TS 文件进不来。
- 当你修改 CLI 命令或 flag、API 请求/响应字段，或内置 skill 文档化（`server/internal/service/builtin_skills/*`）的产品行为时，必须在同一 PR 更新该 skill 的 `SKILL.md` **和**其 `references/*-source-map.md`。内置 skill 是发给智能体的源可追溯合同——代码动了 skill 不动，会默默教错行为。

### API 响应兼容

用户机器上安装的桌面应用比它对话的任何后端都旧：0.2.26 的用户会命中 0.3.x 的 server，然后 0.4.x，再往后。每个响应形状都是一份**会**漂移的合同，前端必须能在漂移中存活而不白屏。已有三起事故因违反此规则而发生——#2143、#2147、#2192。

编写消费 API 响应的代码时，遵循：

- **解析，不要 cast。** 跨网络的未类型化 JSON 不是 `T`。在 `packages/core/api/schema.ts` 中用 `parseWithFallback`、一个 `zod` schema 和显式 fallback。校验失败时记 warning 并返回 fallback；绝不向 UI 抛异常。
- **响应体上不要裸 `as`。** 任何被 UI 逻辑消费的端点方法，返回前必须经过 schema。
- **下游全用可选链与默认值。** 把每个字段都当可能缺失。用显式布尔检查（`=== true`）而非 truthy/falsy 否定，后者会把 `undefined` 和 `null` 默默当 `false`。
- **不要把 UI 能力钉在单个后端字段上。** 若一个按钮或指示器只依赖服务端一个布尔值，后端 bug 一删它就坏了。组合多个信号（游标存在、页长度等），让能力在最坏情况下仍可用。
- **枚举漂移要降级，不要崩。** 新的服务端枚举值应渲染通用 fallback。对服务端驱动字符串的 `switch` 必须有 `default` 分支。
- **新增或修改端点时：** 同 PR 加 schema，并至少写一个喂入畸形响应（缺字段、错类型、`null` 数组）的测试。若未来变更破坏了合同，测试会失败关闭。

这不是过度防御——这是已部署应用架构的必要防御。前端构建与后端部署是独立发布的，响应形状会漂移，前端必须能在漂移中存活而不白屏。

### 后端 Handler UUID 解析约定

`server/internal/handler/` 下每个 Go handler 都遵循这些规则。约定存在是因为 `util.ParseUUID` 曾对非法输入默默返回零 UUID，导致 #1661——一个 `DELETE` 返回 204 成功，而 SQL `DELETE` 匹配 0 行。

- **既接受 UUID 也接受人类可读标识的资源路径参数**（如 issue 的 `chi.URLParam(r, "id")`，同时接受 `MUL-123` 和 UUID）必须通过专用 loader（`loadIssueForUser` / `loadSkillForUser` / `loadAgentForUser` / `requireDaemonRuntimeAccess`）解析。解析后所有后续 DB 调用——尤其是 `Queries.Delete*` / `Queries.Update*`——必须用解析对象的 `entity.ID`。绝不要把原始 URL 字符串再 round-trip 过 `parseUUID` 用于写查询。
- **来自请求边界的纯 UUID 输入**（总是 UUID 的 URL 参数、请求体字段、query 参数、header）必须用 `parseUUIDOrBadRequest(w, s, fieldName)` 校验。非法输入时写 400 并返回 `ok=false`——立即返回。
- **可信 UUID round-trip**（sqlc 返回的 UUID 再传回 query、测试 fixture）用 `parseUUID(s)`，它调用 `util.MustParseUUID`，非法输入时 panic。这里的 panic 意味着一个未防护的用户输入字符串溜进来了——这是真 bug。`chi` 的 `middleware.Recoverer` 把 panic 转成 500，进程继续跑。
- **`util.ParseUUID(s) (pgtype.UUID, error)`** 是 handler 包外唯一安全的变体。始终检查 error。

新增 `Queries.Delete*` 或 `Queries.Update*` 调用时问自己：「这个 UUID 从哪来？」若答案是「未校验的原始用户输入」，先过 `parseUUIDOrBadRequest` 或 loader。

### 依赖声明规则

每个 workspace（`apps/` 与 `packages/` 目录）必须在自己的 `package.json` 中显式声明所有直接导入的外部包。禁止依赖 pnpm hoist 解析未声明的导入（幽灵依赖）——pnpm 创建 peer-dep 变体时会导致生产构建失败。

- 用 `"pkg": "catalog:"` 引用 `pnpm-workspace.yaml` 中的共享版本。
- CI 通过 `eslint-plugin-import-x/no-extraneous-dependencies` 强制。

### 包边界规则

这些是硬约束。违反会破坏架构：

- `packages/core/` — 零 react-dom、零 localStorage（用 StorageAdapter）、零 process.env、零 UI 库。**共享 Zustand store 放这里**，连 view 相关的也放（过滤器、view mode）——store 是纯状态，不是 UI。
- `packages/ui/` — 零 `@multica/core` 导入（纯 UI，无业务逻辑）。
- `packages/views/` — 零 `next/*` 导入、零 store。所有路由用 `NavigationAdapter`。
- `apps/web/platform/` — 唯一允许 Next.js API（`next/navigation`）的地方。

### 不重复规则

**若同一逻辑在多处出现，必须抽取到共享包。**

适用于组件、hook、guard、provider、工具函数。决策流程：

1. 这段代码依赖 Next.js API 吗？→ 留在 `apps/web/`。
2. 依赖 `next/navigation` 吗？→ 留在 `apps/web/platform/` 层。
3. 其他 → 属于 `packages/core/`（无头逻辑）或 `packages/views/`（UI 组件）。

当不同位置对同一概念需要不同行为（如不同 loading UI），把共享逻辑抽成带 props/slot 的组件。不要复制逻辑。

### 跨平台开发规则

为 web 新增页面或功能时：

1. **新页面组件** → 加到 `packages/views/<domain>/`。绝不从 `next/*` 导入。
2. **在应用中接线** → 在 `apps/web/app/`（Next.js page 文件）加路由。
3. **导航** → 用 `useNavigation().push()` 或 `<AppLink>`。共享代码中绝不用框架专属的 link/router API。
4. **共享 guard/provider** → 用 `packages/views/layout/` 的 `DashboardGuard`。不要另搞一套 guard 逻辑。
5. **平台专属 UI** → 若功能只属于 web，留在 `apps/web/`。用共享布局组件的 props slot（`extra`、`topSlot`）注入平台专属 UI。
6. **需要 workspace 上下文的新 hook** → 接受 `wsId` 参数，而不是从 `useWorkspaceId()` Context 读，这样它在 `WorkspaceIdProvider` 内外都能工作。

### CSS 架构

Web 应用使用 `packages/ui/styles/` 的 CSS 基础。

- **Design token** → 用语义 token（`bg-background`、`text-muted-foreground`）。绝不用硬编码 Tailwind 颜色（`text-red-500`、`bg-gray-100`）。
- **共享样式** → `packages/ui/styles/`。绝不在应用 CSS 中重复滚动条样式、keyframe 或 base 层规则。
- **`@source` 指令** → 应用扫描共享包，让 Tailwind 看到所有类名。

## UI/UX 规则

- 优先用 shadcn 组件而非自定义实现。通过 `pnpm ui:add <component>` 从项目根安装——加到 `packages/ui/components/ui/`。所有组件用 Base UI 原语（`@base-ui/react`），不是 Radix。
- 用 shadcn design token 做样式。避免硬编码颜色值。
- 除非设计明确需要，不要引入额外状态（useState、context、reducer）。
- 密切关注 **overflow**（截断长文本、可滚动容器）、**alignment**、**spacing** 一致性。
- **若一个组件在多处复用，它属于共享包。** 不要复制粘贴。

## 测试规则

### 测试写在哪

测试跟随代码，不跟随应用。这是本 monorepo 最重要的测试原则：

| 测试对象 | 测试位置 | 原因 |
|---|---|---|
| 共享业务逻辑（store、query、hook） | `packages/core/*.test.ts` | 无需 DOM，纯逻辑 |
| 共享 UI 组件（页面、表单、modal） | `packages/views/*.test.tsx` | jsdom，无框架 mock |
| 平台专属接线（cookie、redirect、searchParams） | `apps/web/*.test.tsx` | 需要框架专属 mock |
| 端到端用户流程 | `e2e/*.spec.ts` | 真实浏览器、真实后端 |

**绝不要在应用测试文件里测共享组件行为。** 若一个测试需要 mock `next/navigation` 才能测 `@multica/views` 的组件，说明测试位置错了——移到 `packages/views/` 并 mock `@multica/core`。

### 测试基础设施

- `packages/core/` — Vitest，Node 环境（无 DOM）
- `packages/views/` — Vitest，jsdom 环境，`@testing-library/react`
- `apps/web/` — Vitest，jsdom 环境，框架专属 mock
- `e2e/` — Playwright
- `server/` — Go 标准 `go test`

所有测试依赖在 pnpm catalog 中统一版本。

### Mock 约定

- 用 `vi.hoisted()` + `Object.assign(selectorFn, { getState })` 模式 mock `@multica/core` 的 store（Zustand store 既可调用又有 `.getState()`）。
- mock `@multica/core/api` 做 API 调用。
- `packages/views/` 测试中：绝不 mock `next/*`——这里不存在这些。
- `apps/web/` 测试中：仅 mock 框架专属 API 的平台专属行为。

### TDD 工作流

1. 先在**正确的包**写失败测试。
2. 写实现。
3. 跑 `pnpm test`（Turborepo 发现所有包）。
4. 绿→完成。

### Go 测试

标准 `go test`。测试应在测试数据库里创建自己的 fixture 数据。

### E2E 测试

E2E 测试应自包含。用 `TestApiClient` fixture 做数据 setup/teardown：

```typescript
import { loginAsDefault, createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

let api: TestApiClient;

test.beforeEach(async ({ page }) => {
  api = await createTestApi();
  await loginAsDefault(page);
});

test.afterEach(async () => {
  await api.cleanup();
});

test("example", async ({ page }) => {
  const issue = await api.createIssue("Test Issue");
  await page.goto(`/issues/${issue.id}`);
});
```

## 提交规则

- 用按逻辑意图分组的原子提交。
- 约定格式：`feat(scope)`、`fix(scope)`、`refactor(scope)`、`docs`、`test(scope)`、`chore(scope)`。

## 推送前最低检查

```bash
make check    # 跑所有检查：typecheck、单测、Go 测试、E2E
```

仅在用户明确要求时才运行验证。

按需定向检查：

```bash
pnpm typecheck        # 仅 TypeScript 类型错误
pnpm test             # 仅 TS 单测（Vitest，所有包）
make test             # 仅 Go 测试
pnpm exec playwright test   # 仅 E2E（需后端 + 前端运行中）
```

## AI 智能体验证循环

编写或修改代码后，始终运行完整验证流水线：

```bash
make check
```

**工作流：**
- 写代码满足需求
- 跑 `make check`
- 若任一步失败，读错误输出，修代码，重跑
- 重复直到全部通过
- 然后才算任务完成

**快速迭代：** 若确定只影响 TypeScript 或 Go，先跑对应单项拿更快反馈，最后用完整 `make check` 收尾。

## CLI 发布

**前置条件：** 每次 Production 部署必须伴随一次 CLI 发布。

1. 在 `main` 分支创建 tag：`git tag v0.x.x`
2. 推 tag：`git push origin v0.x.x`
3. GitHub Actions 自动触发 `release.yml`：跑 Go 测试 → GoReleaser 构建多平台二进制 → 发布到 GitHub Releases + Homebrew tap

除非用户指定版本，每次发布默认 bump patch（如 `v0.1.12` → `v0.1.13`）。

## 多租户

所有查询按 `workspace_id` 过滤。成员检查控制访问。`X-Workspace-ID` header 把请求路由到正确工作区。

## 智能体受理人

受理人是多态的——可以是 member 或 agent。issue 上是 `assignee_type` + `assignee_id`。智能体用独特样式渲染（紫色背景、机器人图标）。
