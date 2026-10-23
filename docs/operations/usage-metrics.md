# 用量统计与外部链路关联

面向运维与排障：用量从 CLI 到页面的完整链路、各段 token 的口径差异、外部链路
（AgentLens）关联的判定规则，以及汇总/补齐的已知故障形态。字段与接口以代码为准，
本文只描述"为什么这样记"和"出错时看哪里"。

## 1. 链路

```
CodeBuddy CLI (子进程)
  └─ stream-json 事件：assistant 每次调用带 usage；result 带 modelUsage + _requestId
       ↓  daemon 解析
  ├─ POST /api/daemon/tasks/{id}/usage    → task_usage            （按 provider+model 一行）
  └─ POST /api/daemon/tasks/{id}/traces   → task_provider_trace   （0..N 行，外部链路）
       ↓  定时任务 rollup_task_usage_hourly()
     task_usage_hourly  ← 页面/看板唯一的读源（runtime usage、按日、按 agent）
```

两条上报互相独立：一次执行没有 token 用量并不代表没有链路，反过来 backend 不上报
链路也不影响用量。任何一端缺失都不应成为另一端的写入前提。

## 2. token 口径（最容易算错的地方）

CodeBuddy 这条通道（`copilot.tencent.com`）与 Anthropic 原生 API 的口径**不同**：

| 来源 | `input_tokens` 含义 | 缓存字段 |
| --- | --- | --- |
| `result.usage.input_tokens` | **整段提示词总量**，已含 cache read + cache creation | 是子项，不是额外部分 |
| `result.modelUsage[m].inputTokens` | **不含缓存**的输入 | 需与两项缓存相加 |
| `assistant.message.usage.input_tokens` | 整段提示词总量（同 result.usage） | 是子项 |
| AgentLens `traceList.inputTokens` | 整段提示词总量 | `cacheReadTokens` 是子项 |
| Multica `task_usage.input_tokens` | **不含缓存** | 与 cache_read/cache_write 相加才是总量 |

实测依据：一次调用 `cache_creation=27156`、`cache_read=2816`、`input_tokens=29972`，
`27156 + 2816 = 29972`；同一请求的 `modelUsage` 给 `inputTokens=0`。生产库中
`input_tokens = cache_read + cache_write` 的行即旧回退路径按"总量"直接入库的结果，
相加会把这些行的输入算成两倍。

因此 daemon 解析时把顶层 `input_tokens` 归一为"不含缓存"再落库（`codebuddyUncachedInput`）。
核对口径的等式：

```
AgentLens.inputTokens == Multica(input_tokens + cache_read_tokens + cache_write_tokens)
```

三个真实执行已按此等式逐字段相等（见 `.run/evidence/gong-51-usage-trace-20261023/`）。

另外两点：

- **续跑会话的 `result.usage` 是累计值**。同一 session 续跑时，`result.usage` 会把
  之前轮次的用量也算进去；`modelUsage` 和每次 `assistant` 的 usage 才是本次进程的。
  所以 daemon 优先用 `modelUsage`，否则续跑会重复计数。
- `cached_write_tokens` 在 CLI 的不同事件里不一致（`model_call.completed` 报 0，
  `turn.completed`/stream-json 报真实值）。落库取 stream-json 的值；AgentLens 不
  暴露写入量，无法用它核对缓存写入。

## 3. 价格：未知不等于 0

`task_usage.cost_usd_ticks` 是可空列：NULL 表示厂商没有报价，0（正数才写入）才是
真实成本。聚合查询用 `uncosted_*_tokens` 字段带出"这部分没有价格"。

页面上，当一批用量里没有任何一行能定价（既无厂商成本、费率表也不认识该模型）时，
费用与缓存节省显示 `—` 而不是 `$0.00` —— 后者等于断言"这次运行免费"。
补价入口仍在（自定义定价）。

## 4. 外部链路关联（`task_provider_trace`）

一次执行对应 **0..N** 条链路，不要假设 1:1：

- 正常一轮 = 1 条；
- 换新会话重试 = 2 条（两个进程）；
- 进程没起来 / 没有模型调用 = 0 条；
- 同一个 session 续跑会不断产生新链路（session : trace = 1 : N）。

标识来源必须是**子进程自己**给出的值：

- `session_id`：stream-json 的 `system`/`result` 事件；
- trace id：`result` / `system(status)` 事件上的 `_requestId`，与 AgentLens 的
  `traceID` 相同。

父进程环境里的 `CODEBUDDY_ROOT_REQUEST_ID` 是**调用方**的请求标识，不能当作子进程
标识；两者在实测中确实不同。

`task_provider_trace` 刻意不建外键、不建级联：任务行删除时由 `DeleteTaskBatch` 在同一事务里
显式删除本表行，上报路径先按主键取任务并解析工作区，取不到或工作区不匹配就不写库。唯一索引
是独立单语句的并发建索引迁移，重复上报靠该索引 + `ON CONFLICT` 幂等。

`correlation` 字段区分数据性质：`exact`（子进程直接给出）与 `inferred_window`
（事后按"会话 + 时间窗"检索到的候选）。候选关联不得当成精确归属；按会话查一天
可能返回多条链路，必须逐条核对。

## 5. 小时汇总与自愈

`rollup_task_usage_hourly()` 是单例水位（`task_usage_hourly_rollup_state.id = 1`）。替换函数体属于
非新增结构变更，发布器按 SQL 内容审查后放行（只允许重定义该函数、只允许写汇总状态行）。
迁移 629 之后：

- 状态行缺失时按最早一条 `task_usage` 播种，不再永久空转；
- `1970-01-01` 视为"从未汇总"，直接跳到真实最早数据；
- 单次 tick 内按 1 天窗口连续追赶，上限 60 天；
- 追赶 tick 被 `statement_timeout` 取消时显式释放咨询锁 4246。

排查顺序：`task_usage` 有行但 `task_usage_hourly` 为空 → 先看状态行是否存在、水位
是否停在 1970；两者都正常再看脏队列 `task_usage_hourly_dirty`。

注意窗口函数只看 `tu.updated_at ∈ [from, to)`（历史 NULL 行退回 `created_at`）。
把水位回填到"现在"之前前，确认历史行的 `updated_at` 不是入库时刻，否则它们会被
水位跳过。

## 6. 历史补齐

`cmd/backfill_task_usage_hourly` 按月切片重算桶并盖水位，幂等可续跑；它**不会**
创建状态行——缺少状态行时只告警并返回，需要迁移 620 的函数来补。因此
"先跑补齐、再发布"只在状态行已存在的库上成立。

链路关联的补齐走正式接口 `POST /api/daemon/tasks/{id}/traces`（`correlation` 标
`inferred_window`），旧版本没有该接口，必须在带该接口的版本上线后执行。
