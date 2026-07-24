# CLI 与智能体守护进程指南

`multica` CLI 把你的本地机器接入 Multica。它处理认证、工作区管理、issue 跟踪，并运行执行 AI 任务的智能体守护进程。

## 安装

完整安装步骤见 [CLI_INSTALL.md](CLI_INSTALL.md)，AI 智能体可按其 SOP 直接执行。

### Homebrew（macOS/Linux）

```bash
brew install multica-ai/tap/multica
```

### 从源码构建

```bash
git clone https://github.com/multica-ai/multica.git
cd multica
make build
cp server/bin/multica /usr/local/bin/multica
```

### 更新

```bash
brew upgrade multica-ai/tap/multica
```

安装脚本或手动安装的用：

```bash
multica update
```

`multica update` 自动检测安装方式并据此升级。

## 快速开始

```bash
# 一条命令完成配置、认证、启动守护进程
multica setup

# 自部署（本地）环境用：
multica setup self-host
```

或分步：

```bash
# 1. 认证（打开浏览器登录）
multica login

# 2. 启动智能体守护进程
multica daemon start

# 3. 完成——watched workspace 里的智能体现在可以在本机执行任务
```

`multica login` 自动发现你所属的所有工作区并加入守护进程 watch 列表。

## 认证

### 浏览器登录

```bash
multica login
```

打开浏览器做 Multica 账号/密码认证，创建 90 天 personal access token，并自动配置工作区。

### Token 登录

```bash
multica login --token <mul_...>
```

直接用 personal access token 认证。适用于 headless 环境。传 `--token=` 加空值做交互式提示输入（避免 token 进入 shell 历史）。

### 查看状态

```bash
multica auth status
```

显示当前 server、用户与 token 有效期。

### 登出

```bash
multica auth logout
```

移除存储的认证 token。

## 智能体守护进程

守护进程是本地智能体运行时。它检测机器上可用的 AI CLI、向 Multica server 注册，并在智能体被分配任务时执行。

### 启动

```bash
multica daemon start
```

默认后台运行，日志写到 `~/.multica/daemon.log`。

前台运行（便于调试）：

```bash
multica daemon start --foreground
```

### 停止

```bash
multica daemon stop
```

### 状态

```bash
multica daemon status
multica daemon status --output json
```

显示 PID、uptime、检测到的智能体、watched workspace。

### 日志

```bash
multica daemon logs              # 最近 50 行
multica daemon logs -f           # 跟踪（tail -f）
multica daemon logs -n 100       # 最近 100 行
```

### 支持的智能体

守护进程自动检测 PATH 中的这些 AI CLI：

| CLI | 命令 | 说明 |
|-----|---------|------|
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | `claude` | Anthropic 的编码智能体 |
| [Codex](https://github.com/openai/codex) | `codex` | OpenAI 的编码智能体 |
| [GitHub Copilot CLI](https://docs.github.com/en/copilot) | `copilot` | GitHub 的编码智能体（模型按你的 GitHub 权益路由） |
| OpenCode | `opencode` | 开源编码智能体 |
| OpenClaw | `openclaw` | 开源编码智能体 |
| Hermes | `hermes` | Nous Research 编码智能体 |
| Gemini | `gemini` | Google 的编码智能体 |
| [Pi](https://pi.dev/) | `pi` | Pi 编码智能体 |
| [Cursor Agent](https://cursor.com/) | `cursor-agent` | Cursor 的 headless 编码智能体 |
| Kimi | `kimi` | Moonshot 编码智能体 |
| Kiro CLI | `kiro-cli` | Kiro ACP 编码智能体 |

至少需要装一个。守护进程把每个检测到的 CLI 注册为一个可用运行时。

### 工作机制

1. 启动时检测已装的智能体 CLI，为每个 watched workspace 中的每个智能体注册一个运行时
2. 按可配置间隔（默认 3s）轮询 server 拉取已认领任务
3. 任务到达时创建隔离的 workspace 目录，spawn 智能体 CLI，把结果流回
4. 周期性发心跳（默认 15s）让 server 知道守护进程活着
5. 关闭时所有运行时注销

### 配置

守护进程行为通过 flag 或环境变量配置：

| 设置 | flag | 环境变量 | 默认值 |
|---------|------|--------------|---------|
| 轮询间隔 | `--poll-interval` | `MULTICA_DAEMON_POLL_INTERVAL` | `3s` |
| 心跳间隔 | `--heartbeat-interval` | `MULTICA_DAEMON_HEARTBEAT_INTERVAL` | `15s` |
| 智能体超时 | `--agent-timeout` | `MULTICA_AGENT_TIMEOUT` | `0`（无上限；由 watchdog 兜底） |
| Codex 语义静默超时 | `--codex-semantic-inactivity-timeout` | `MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT` | `10m` |
| 最大并发任务 | `--max-concurrent-tasks` | `MULTICA_DAEMON_MAX_CONCURRENT_TASKS` | `20` |
| 守护进程 ID | `--daemon-id` | `MULTICA_DAEMON_ID` | hostname |
| 设备名 | `--device-name` | `MULTICA_DAEMON_DEVICE_NAME` | hostname |
| 运行时名 | `--runtime-name` | `MULTICA_AGENT_RUNTIME_NAME` | `Local Agent` |
| workspace 根目录 | — | `MULTICA_WORKSPACES_ROOT` | `~/multica_workspaces` |
| 启用 GC | — | `MULTICA_GC_ENABLED` | `true`（设 `false`/`0` 禁用） |
| GC 扫描间隔 | — | `MULTICA_GC_INTERVAL` | `1h` |
| GC TTL（done/cancelled issue） | — | `MULTICA_GC_TTL` | `24h` |
| GC 孤儿 TTL（无 `.gc_meta.json`） | — | `MULTICA_GC_ORPHAN_TTL` | `72h` |
| GC 产物 TTL（open issue） | — | `MULTICA_GC_ARTIFACT_TTL` | `12h`（设 `0` 禁用） |
| GC 产物 pattern | — | `MULTICA_GC_ARTIFACT_PATTERNS` | `node_modules,.next,.turbo` |

#### workspace 垃圾回收

守护进程周期性扫描 `MULTICA_WORKSPACES_ROOT`，按三种模式回收磁盘空间：

- **完整任务清理** — issue 状态为 `done` 或 `cancelled` 且空闲超过 `MULTICA_GC_TTL` 时，整个任务目录被删除。
- **孤儿清理** — 没有 `.gc_meta.json` 的任务目录（如守护进程崩溃残留）超过 `MULTICA_GC_ORPHAN_TTL` 后被删除。
- **仅产物清理** — 任务完成至少 `MULTICA_GC_ARTIFACT_TTL` 但 issue 仍 open 时，目录 basename 匹配 `MULTICA_GC_ARTIFACT_PATTERNS` 的可重建构建产物被删除；工作目录其余部分（源码、`.git`、`output/`、`logs/`、`.gc_meta.json`）保留，让智能体下次任务能恢复同一工作目录。

pattern 仅按 basename 匹配——含 `/` 或 `\` 的条目被默默丢弃——且绝不进入 `.git` 子树。默认列表（`node_modules`、`.next`、`.turbo`）刻意收窄；若仓库持续产出其他可重建目录，按部署扩展（例如 `MULTICA_GC_ARTIFACT_PATTERNS=node_modules,.next,.turbo,target,__pycache__`）。完全禁用产物清理设 `MULTICA_GC_ARTIFACT_TTL=0`。

按智能体覆盖：

| 变量 | 说明 |
|----------|------|
| `MULTICA_CLAUDE_PATH` | 自定义 `claude` 二进制路径 |
| `MULTICA_CLAUDE_MODEL` | 覆盖使用的 Claude 模型 |
| `MULTICA_CLAUDE_ARGS` | Claude Code 运行的默认额外参数 |
| `MULTICA_CODEX_PATH` | 自定义 `codex` 二进制路径 |
| `MULTICA_CODEX_MODEL` | 覆盖使用的 Codex 模型 |
| `MULTICA_CODEX_ARGS` | Codex 运行的默认额外参数 |
| `MULTICA_COPILOT_PATH` | 自定义 `copilot` 二进制路径 |
| `MULTICA_COPILOT_MODEL` | 覆盖 Copilot 模型（注意：GitHub Copilot 按账户权益路由模型，可能不生效） |
| `MULTICA_OPENCODE_PATH` | 自定义 `opencode` 二进制路径 |
| `MULTICA_OPENCODE_MODEL` | 覆盖使用的 OpenCode 模型 |
| `MULTICA_OPENCLAW_PATH` | 自定义 `openclaw` 二进制路径 |
| `MULTICA_OPENCLAW_MODEL` | 覆盖使用的 OpenClaw 模型 |
| `MULTICA_HERMES_PATH` | 自定义 `hermes` 二进制路径 |
| `MULTICA_HERMES_MODEL` | 覆盖使用的 Hermes 模型 |
| `MULTICA_GEMINI_PATH` | 自定义 `gemini` 二进制路径 |
| `MULTICA_GEMINI_MODEL` | 覆盖使用的 Gemini 模型 |
| `MULTICA_PI_PATH` | 自定义 `pi` 二进制路径 |
| `MULTICA_PI_MODEL` | 覆盖使用的 Pi 模型 |
| `MULTICA_CURSOR_PATH` | 自定义 `cursor-agent` 二进制路径 |
| `MULTICA_CURSOR_MODEL` | 覆盖使用的 Cursor Agent 模型 |
| `MULTICA_KIMI_PATH` | 自定义 `kimi` 二进制路径 |
| `MULTICA_KIMI_MODEL` | 覆盖使用的 Kimi 模型 |
| `MULTICA_KIRO_PATH` | 自定义 `kiro-cli` 二进制路径 |
| `MULTICA_KIRO_MODEL` | 覆盖使用的 Kiro 模型 |

`MULTICA_CLAUDE_ARGS` 与 `MULTICA_CODEX_ARGS` 按 POSIX shellword 引号解析，所以 `--model "gpt-5.1 codex" --sandbox read-only` 这类值会按 shell 命令行拆分。智能体参数应用顺序：Multica 硬编码默认 → 守护进程级 env 默认 → 任务的 per-agent `custom_args`。

### 自部署 server

连接自部署 Multica 实例时，最简单的方式：

```bash
# 一条命令——配置 localhost、认证、启动守护进程
multica setup self-host

# 或带自定义域名：
multica setup self-host --server-url https://api.example.com --app-url https://app.example.com
```

或手动配置：

```bash
# 分别设置 URL
multica config set server_url http://localhost:8080
multica config set app_url http://localhost:3000

# 生产带 TLS：
# multica config set server_url https://api.example.com
# multica config set app_url https://app.example.com

multica login
multica daemon start
```

### Profile

Profile 让你在同一台机器上跑多个守护进程——例如一个连生产、一个连 staging。

```bash
# 配置 staging profile
multica setup self-host --profile staging --server-url https://api-staging.example.com --app-url https://staging.example.com

# 启动其守护进程
multica daemon start --profile staging

# 默认 profile 独立运行
multica daemon start
```

每个 profile 有自己的配置目录（`~/.multica/profiles/<name>/`）、守护进程状态、健康端口与 workspace 根目录。

## 工作区

### 多工作区操作

每条命令针对单个工作区。CLI 按以下顺序解析（优先级从高到低）：

1. 命令上的 `--workspace-id <id>` flag
2. `MULTICA_WORKSPACE_ID` 环境变量
3. 当前 profile 中存储的默认工作区（由 `multica workspace switch` 或 `multica login` 设置）

`multica workspace switch <id|slug>` 是日常改默认工作区的方式。脚本或 headless 场景不想留状态时，优先用 `--workspace-id` flag 或环境变量。`multica config set workspace_id <id>` 是 `switch` 的底层等价物（写同一设置但跳过访问检查）。

若需要组织/账户间的完全隔离——独立 token、独立守护进程、独立配置目录——用 `--profile <name>`。每个 profile 保留自己的默认工作区。

### 列出工作区

```bash
multica workspace list
multica workspace list --full-id
multica workspace list --output json
```

当前默认工作区用 `*` 标记。表格输出显示短 UUID 前缀——需要规范 UUID 时传 `--full-id`。

### 切换默认工作区

```bash
multica workspace switch <workspace-id>
multica workspace switch <slug>
```

验证你有访问权限后设为当前 profile 的默认。不带 `--workspace-id` 与 `MULTICA_WORKSPACE_ID` 的后续命令都指向它。要改非默认 profile 的工作区，配合 `--profile`。

### 查详情

```bash
multica workspace get <workspace-id>
multica workspace get <workspace-id> --output json
```

不传 `<workspace-id>` 时解析为当前默认工作区，所以 `multica workspace get` 兼作「我在哪个工作区」。

### 列出成员

```bash
multica workspace member list <workspace-id>
```

## Issue

### 列出 issue

```bash
multica issue list
multica issue list --status in_progress
multica issue list --priority urgent --assignee "Agent Name"
multica issue list --assignee-id 5fb87ac7-23b5-4a7a-81fa-ed295a54545d
multica issue list --full-id
multica issue list --limit 20 --output json
```

表格输出显示可路由的 issue `KEY`（如 `MUL-123`）；复制到 `issue get`、`issue comment list`、`issue status`、`--parent` 等后续命令。需要规范 UUID 时加 `--full-id`。可用过滤器：`--status`、`--priority`、`--assignee` / `--assignee-id`、`--project`、`--metadata`、`--limit`。名称重叠时用 `--assignee-id <uuid>` 做无歧义过滤。

用 `--metadata key=value`（可重复；AND 组合）按 per-issue metadata 过滤。值做 JSON 解析：`true`/`false` 转 bool，数字转数字，其他为字符串。要强制字符串避免被嗅探为数字时用 `'"42"'`：

```bash
multica issue list --metadata pipeline_status=waiting_review
multica issue list --metadata pr_number=482 --metadata is_blocked=true
```

### 查 issue

```bash
multica issue get <id>
multica issue get <id> --output json
```

### 创建 issue

```bash
multica issue create --title "Fix login bug" --description "..." --priority high --assignee "Lambda"
multica issue create --title "Fix login bug" --assignee-id 5fb87ac7-23b5-4a7a-81fa-ed295a54545d
```

flag：`--title`（必填）、`--description`、`--status`、`--priority`、`--assignee` / `--assignee-id`、`--parent`、`--project`、`--due-date`。脚本场景用 `--assignee-id <uuid>`（与 `--assignee` 互斥），从 `multica workspace member list --output json` / `multica agent list --output json` 拿 ID。

### 更新 issue

```bash
multica issue update <id> --title "New title" --priority urgent
```

### 分配 issue

```bash
multica issue assign <id> --to "Lambda"
multica issue assign <id> --to-id 5fb87ac7-23b5-4a7a-81fa-ed295a54545d
multica issue assign <id> --unassign
```

用 `--to-id <uuid>` 按规范 UUID 分配（与 `--to` 互斥）；成员与智能体名称重叠时有用。

### 改状态

```bash
multica issue status <id> in_progress
```

合法状态：`backlog`、`todo`、`in_progress`、`in_review`、`done`、`blocked`、`cancelled`。

### 评论

```bash
# 列出评论——扁平时间线，按时间排序。硬上限 2000 行；
# 长运行 issue 优先用下面的线程感知读取，保持上下文窗口紧凑。
multica issue comment list <issue-id>

# 单个线程（根 + 所有后代）。锚点可以是根本身或线程内任一回复——
# server 会向上走到根。
multica issue comment list <issue-id> --thread <comment-id>

# 单个线程，只取最近 N 条回复。线程根始终包含（即使 --tail 0），
# 所以智能体落到长线程仍能拿到「主题是什么」的上下文，
# 不会被数百条回复撑爆 prompt。
multica issue comment list <issue-id> --thread <comment-id> --tail 30

# 在同一线程内向更旧的回复翻页。--before / --before-id 是上一次
# 响应在 stderr 输出的回复游标：
# `Next reply cursor: --before <ts> --before-id <reply-id>`。
multica issue comment list <issue-id> --thread <comment-id> --tail 30 \
    --before <ts> --before-id <reply-id>

# 最近活跃的线程（根 + 所有后代），按线程分组。返回 N 个完整对话弧，
# 最旧活跃的在前，让最新线程最接近「now」出现在智能体 prompt 中。
multica issue comment list <issue-id> --recent 20

# 向更旧的线程翻页。--recent 下，--before / --before-id 是
# 线程游标（线程 last_activity_at + 根 id），在 stderr 输出为
# `Next thread cursor: --before <ts> --before-id <root-id>`。
multica issue comment list <issue-id> --recent 20 \
    --before <ts> --before-id <root-id>

# 增量轮询。与 --thread 或 --recent 组合；过滤掉页内创建时间
# <= <ts> 的回复（线程根豁免，智能体始终拿上下文）。
multica issue comment list <issue-id> --thread <comment-id> --tail 30 \
    --since <RFC3339-timestamp>

# 加评论
printf '%s\n' "Looks good, merging now" > reply.md
multica issue comment add <issue-id> --content-file reply.md

# 回复特定评论
printf '%s\n' "Thanks!" > reply.md
multica issue comment add <issue-id> --parent <comment-id> --content-file reply.md

# 删除评论
multica issue comment delete <comment-id>
```

**`--before` / `--before-id` 语义取决于翻页模式**，刻意设计成同一 flag 不同作用域：

| 模式 | 游标走什么 | stderr 标签 |
| --- | --- | --- |
| `--recent N` | 更旧的*线程*（last_activity_at, root_id） | `Next thread cursor` |
| `--thread <id> --tail N` | 该线程内更旧的*回复*（created_at, id） | `Next reply cursor` |

这两种模式之外（`--thread` 无 `--tail`，或无 `--thread` 也无 `--recent`），游标 flag 被拒绝，避免默默 no-op。server 仅在确实存在更旧页时输出游标 header（`X-Multica-Next-Before` / `X-Multica-Next-Before-Id`）——边界恰好整页时（如 3 条回复的线程跑 `--tail 3`）刻意不返回游标，让调用方停止翻页。

`--since` 与 `--recent` 或 `--thread --tail` 组合时，server 还会在游标本身比 `since` 旧时抑制游标。更旧的页严格走更旧行，无法满足 `> since`——在那里输出游标只会让调用方拿到只有根的页直到线程/issue 开头。增量轮询在第一个游标目标早于 watermark 的页停止。

### metadata

per-issue metadata 是智能体用来跟踪管道状态（PR 号、管道状态、waiting_on 等）的小 KV map。key 匹配 `^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`，值是基础类型（string / number / bool），每个 issue 最多 50 个 key，blob 上限 8KB。

写入门槛很高：仅当对 issue 实质重要且很可能被未来同一 issue 上的运行重读时才 pin 一个值（PR URL、deploy URL、被什么阻塞）。多数运行写 0 个新 key——这是预期情况。不要 pin 运行时簿记如 `attempts`、单次调查笔记、大日志、密钥/token、description/comment 副本——完整反模式列表见智能体运行时 prompt。

```bash
# 列出 issue 上所有 key
multica issue metadata list <issue-id>

# 读单个 key
multica issue metadata get <issue-id> --key pipeline_status

# 写单个 key——值自动类型推断（true/false → bool，数字 → number，其他 → string）
multica issue metadata set <issue-id> --key pipeline_status --value waiting_review
multica issue metadata set <issue-id> --key pr_number --value 482
multica issue metadata set <issue-id> --key is_blocked --value true

# 嗅探会选错类型时强制指定类型
multica issue metadata set <issue-id> --key code --value 42 --type string

# 删除 key
multica issue metadata delete <issue-id> --key pipeline_status
```

所有写都是单 key 原子——并发智能体写不同 key 不会丢彼此更新。查询用 `multica issue list --metadata key=value`（见上方「列出 issue」）。

### 订阅者

```bash
# 列出 issue 订阅者
multica issue subscriber list <issue-id>

# 自己订阅
multica issue subscriber add <issue-id>

# 按名订阅其他成员或智能体
multica issue subscriber add <issue-id> --user "Lambda"

# 自己退订
multica issue subscriber remove <issue-id>

# 退订其他成员或智能体
multica issue subscriber remove <issue-id> --user "Lambda"
```

订阅者收到 issue 活动通知（新评论、状态变更等）。不带 `--user` 时作用于调用者自身。

### 执行历史

```bash
# 列出 issue 的所有执行 run
multica issue runs <issue-id>
multica issue runs <issue-id> --full-id
multica issue runs <issue-id> --output json

# 查看某次执行 run 的消息
multica issue run-messages <task-id>
multica issue run-messages <short-task-id> --issue <issue-id>
multica issue run-messages <task-id> --output json

# 增量拉取（仅某个序列号之后的消息）
multica issue run-messages <task-id> --since 42 --output json
```

`runs` 显示 issue 的所有过去与当前执行（含运行中的任务）。表格输出默认用短 task UUID 前缀；需要规范 task UUID 时传 `--full-id`。`run-messages` 直接接受完整 task UUID；复制的短前缀必须用 `--issue <issue-id>` 限定，CLI 只检查该 issue 的 run。它显示单次 run 的详细消息日志（工具调用、思考、文本、错误）。用 `--since` 高效轮询进行中的 run。

## 项目

项目把相关 issue 分组（如一个 sprint、一个 epic、一个工作流）。每个项目属于一个工作区，可选有 lead（成员或智能体）。

### 列出项目

```bash
multica project list
multica project list --status in_progress
multica project list --output json
```

可用过滤器：`--status`。

### 查项目

```bash
multica project get <id>
multica project get <id> --output json
```

### 创建项目

```bash
multica project create --title "2026 Week 16 Sprint" --icon "🏃" --lead "Lambda"
```

flag：`--title`（必填）、`--description`、`--status`、`--icon`、`--lead`。

### 更新项目

```bash
multica project update <id> --title "New title" --status in_progress
multica project update <id> --lead "Lambda"
```

flag：`--title`、`--description`、`--status`、`--icon`、`--lead`。

### 改状态

```bash
multica project status <id> in_progress
```

合法状态：`planned`、`in_progress`、`paused`、`completed`、`cancelled`。

### 删除项目

```bash
multica project delete <id>
```

### 把 issue 关联到项目

用 `issue create` / `issue update` 的 `--project` flag 关联，或在 `issue list` 上按项目过滤：

```bash
multica issue create --title "Login bug" --project <project-id>
multica issue update <issue-id> --project <project-id>
multica issue list --project <project-id>
```

## setup

```bash
# Multica Cloud 一键 setup：配置、认证、启动守护进程
multica setup

# 本地自部署
multica setup self-host

# 自定义端口
multica setup self-host --port 9090 --frontend-port 4000

# 自定义域名
multica setup self-host --server-url https://api.example.com --app-url https://app.example.com
```

`multica setup` 配置 CLI、打开浏览器认证、启动守护进程——一步搞定。连自部署 server 而非 Multica Cloud 用 `multica setup self-host`。

## 配置

### 查看配置

```bash
multica config show
```

显示配置文件路径、server URL、app URL、默认工作区。

### 设值

```bash
multica config set server_url https://api.example.com
multica config set app_url https://app.example.com
multica config set workspace_id <workspace-id>
```

`config set workspace_id <id>` 是底层接口——原样写值，不检查工作区是否存在或是否有访问权限。日常换工作区优先用 `multica workspace switch <id|slug>`；它会做这两项检查后再保存。

## Autopilot 命令

Autopilot 是调度/触发的自动化，派发智能体任务（创建 issue 或直接跑智能体）。

### 列出 autopilot

```bash
multica autopilot list
multica autopilot list --full-id
multica autopilot list --status active --output json
```

autopilot 表格 ID 是短 UUID 前缀；后续 autopilot 命令接受复制的短前缀（当在工作区内唯一时）。需要规范 UUID 时传 `--full-id`。

### 查 autopilot 详情

```bash
multica autopilot get <id>
multica autopilot get <id> --output json   # 含 trigger
```

### 创建 / 更新 / 删除

```bash
multica autopilot create \
  --title "Nightly bug triage" \
  --description "Scan todo issues and prioritize." \
  --agent "Lambda" \
  --mode create_issue

multica autopilot update <id> --status paused
multica autopilot update <id> --description "New prompt"
multica autopilot delete <id>
```

`--mode` 接受 `create_issue`（每次运行创建新 issue 并分配给智能体）或 `run_only`（不入队直接跑智能体任务，不创建 issue）。`--agent` 接受名或 UUID。

### 手动触发

```bash
multica autopilot trigger <id>            # 触发一次，返回 run
```

### 运行历史

```bash
multica autopilot runs <id>
multica autopilot runs <id> --limit 50 --output json
```

### 调度触发器

```bash
multica autopilot trigger-add <autopilot-id> --cron "0 9 * * 1-5" --timezone "America/New_York"
multica autopilot trigger-update <autopilot-id> <trigger-id> --enabled=false
multica autopilot trigger-delete <autopilot-id> <trigger-id>
```

CLI 目前仅暴露 cron 类型的 `schedule` 触发器。数据模型还定义了 `webhook` 和 `api` 类型，但尚无 server 端点触发它们，故未暴露。

## 其他命令

```bash
multica version              # 显示 CLI 版本与 commit hash
multica update               # 更新到最新版本
multica agent list           # 列出当前工作区的智能体
```

## 输出格式

多数命令支持 `--output` 两种格式：

- `table` — 人读表格（list 命令默认）
- `json` — 结构化 JSON（脚本/自动化用）

```bash
multica issue list --output json
multica daemon status --output json
```

## 错误信息

CLI 把返回到顶层 handler 的命令错误统一过一个面向用户的翻译层（`server/internal/cli/errors.go`），让终端看到的是一句简短可操作的句子，而非原始 Go error、HTTP 状态行或内部的 `resolve issue: ...` 链。（少数命令打印自己的输出或跑刻意快速探测——如 `setup` 的 `/health` 可达性检查——不走该层。）底层细节按需可见（见 `--debug`）。

### 你看到什么

- **友好单行消息。** 传输失败（超时、DNS、连接拒绝、TLS）与 HTTP 状态失败（401/403/404/409/400·422/429/5xx）各渲染为一句清晰的话并附下一步——例如超时提示检查网络或调高 `MULTICA_HTTP_TIMEOUT`，401 提示跑 `multica login`。
- **服务端校验信息保留原样。** 400/422 携带 server 消息时，该消息原样显示（`请求无效：<server message>`）；仅当无消息时才给通用的「检查取值 / 跑 --help」提示。
- **默认不泄露内部。** 原始 URL、状态行、JSON body 与内部动词链默认隐藏，除非你要求。

### 语言

CLI 面向用户的错误信息固定为**中文**。

```bash
multica issue get MUL-9999
```

### 退出码

进程退出码按失败类别分级，便于脚本分支：

| 退出码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `1` | 通用 / 未分类错误 |
| `2` | 网络错误（超时、DNS、连接拒绝、TLS、离线） |
| `3` | 认证 / 授权（HTTP 401、403） |
| `4` | 未找到（HTTP 404） |
| `5` | 校验（HTTP 400、422） |

```bash
multica issue get MUL-9999
if [ $? -eq 4 ]; then echo "no such issue"; fi
```

### 看完整细节（`--debug`）

传全局 `--debug` flag（或设 `MULTICA_DEBUG=1`）在友好消息下方打印完整原始错误链——内部动词链、请求 method/path/status、server 原始 body——便于报 bug 或精确理解 server 返回：

```bash
multica issue list --debug
MULTICA_DEBUG=1 multica issue update MUL-1234 --title "x"
```

### 请求超时

API 请求默认超时 30 秒。慢网络上调高 `MULTICA_HTTP_TIMEOUT`；接受 Go duration（`45s`、`2m`）或纯秒数（`45`）。命令级 deadline 始终不低于该值，所以调高会作用于所有命令。

```bash
MULTICA_HTTP_TIMEOUT=60s multica issue list
```
