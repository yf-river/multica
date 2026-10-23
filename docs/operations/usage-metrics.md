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
     task_usage_hourly  ← runtime 的按日/按周视图读这里
```

读源要分清，两个"按 agent"不是同一张表：

- **看板** `/api/dashboard/usage/daily` 与 `/api/dashboard/usage/by-agent`：都读
  `task_usage_hourly`（`ListDashboardUsageDaily` / `ListDashboardUsageByAgent`），所以
  新写入的用量要等一次小时汇总才会出现。
- **运行时面板** `/api/runtimes/{id}/usage/by-agent` 与单次执行/issue 明细：读
  `task_usage` 原始行（`ListRuntimeUsageByAgent`、`GetTaskUsage`），写入即可见。

两边都按 `task_usage.created_at` 分桶，所以补齐时写进去的时间决定它落在哪一天（见第 7 节）。

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

统计页费用 KPI、智能体排行的费用列、执行日志合计与按次用量弹窗都走同一条判断
（`hasKnownCost`）：整批没有任何可定价来源（`cost_usd_ticks=0` 且 `uncosted_*` 等于全部
token，费率表也不认识这些模型）时显示 `—`，不再把"没有价格"渲染成 `$0.00`。判定粒度跟着消费者走：合计只看合计覆盖的数据；排行榜每一行看这个 agent 自己的用量分组；
按次弹窗的按 agent 分项看该 agent 的任务用量、逐 run 看该次运行的用量。窗口里有一个可
定价模型，不会让其它完全不可定价的行显示成 `$0.00`。只要这一行可定价，仍按现口径求和
显示金额。

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

`rollup_task_usage_hourly()` 是单例水位（`task_usage_hourly_rollup_state.id = 1`）。自愈在
调度器 handler 里完成，不改函数、不需要迁移（原因见第 6 节）。行为：

- 状态行缺失时按最早一条 `task_usage` 播种，不再永久空转；
- `1970-01-01` 视为"从未汇总"，直接跳到真实最早数据；
- 单次 tick 内按 1 天窗口连续追赶，上限 60 天；
- 整轮跑在一条固定连接上，取消/超时后归还连接池前显式 `pg_advisory_unlock(4246)`，锁不会跟着连接回到池里。

排查顺序：`task_usage` 有行但 `task_usage_hourly` 为空 → 先看状态行是否存在、水位
是否停在 1970；两者都正常再看脏队列 `task_usage_hourly_dirty`。

注意窗口函数只看 `tu.updated_at ∈ [from, to)`（历史 NULL 行退回 `created_at`）。
把水位回填到"现在"之前前，确认历史行的 `updated_at` 不是入库时刻，否则它们会被
水位跳过。

## 6. 小时汇总与空区间自愈

调度器 `internal/scheduler/jobs_task_usage.go` 复用既有SQL汇总函数与4246锁。状态播种、
读取待处理起点和跳过空区间处于同一事务锁内；锁被占用时本轮正常退出，不修改水位。

原始变更的筛选时间是 `updated_at`，只有旧行的该值为空时才看 `created_at`；展示的
小时桶仍按 `created_at`。查找当前水位之后的最早待处理原始变更，同时纳入脏键的
`enqueued_at`。已处理旧明细不会卡住中间空档；旧脏键不按水位过滤，因为原函数仍需用它
清理旧小时桶。三个起点查询分别使用已有更新日期、旧行创建日期部分索引和脏键索引。

确定没有待处理数据的区间可以前跳，起点向下取整到小时且不超过当前时间减5分钟；
空库也只前进到该安全边界，不回退有效水位。晚提交的正常历史上报使用真实当前
`updated_at`，不会因为它的历史 `created_at` 早于新水位而被当成已处理。
之后每次函数调用仍处理一天窗口；水位不再推进即停，单tick最多60次。90天处理上限
回归使用90天实际变更数据，不能把无数据的空档当成必须逐日慢爬的工作。

调度周期及调度延迟各为5分钟，未满安全窗口的新变更不会立刻出现在日期报表。
验收应等待自然调度并核对水位、执行审计和实际小时桶；任务明细、按Agent、按日期必须
使用同一冻结工作区/runtime、时间范围和模型集合，明确区分真实provider用量与受控费用
测试数据。不得在共享验收或生产环境手工调用汇总函数、改水位或补台账来制造通过结果。

## 7. 历史补齐

`cmd/backfill_task_usage_hourly` 按月切片重算桶并盖水位，幂等可续跑；它**不会**
创建状态行——缺少状态行时只告警并返回，需要调度器 handler 的补种逻辑先把行补上
（第 6 节）。因此"先跑补齐、再发布"只在状态行已存在的库上成立。

**入账时间**：`POST /api/daemon/tasks/{id}/usage` 现在接受可选 `executed_at`（RFC3339）。
受控的历史补齐用它把用量记到真实执行时刻，而不是补齐当天；口径是任务创建之后、当前
时间之前。普通 daemon 上报不带该字段，按 now() 入账。已有行的更正只更新数值与
`updated_at`，原始 `created_at` 保留。写入后接口会回读核对，数值不符返回 500 而不是
成功——补齐流程靠这个状态决定是否重试。

链路关联的补齐走正式接口 `POST /api/daemon/tasks/{id}/traces`。`correlation` 只能是
`exact`（子进程自报）或 `inferred_window`（事后按会话+时间窗检索的候选）。写库规则：
弱来源不能覆盖强来源，exact 的 session/source 不会被后来的 inferred_window 改写，只有
exact 能把 inferred_window 升级，升级时 session 使用精确来源自报值，未自报则留空，历史猜测不能变成精确事实。同强度下 session 不一致不静默覆盖，两个值记进
`metadata.session_conflict`。整批请求先校验再写，任何一条非法都整批 400，不留下半成品；
写入后逐条回读，缺行或强度被降级返回 500；非空 exact session 与已有精确关联冲突时返回 409，并保留原值与冲突记录。


### CodeBuddy 两份用量来源与实时监控

CodeBuddy 的 assistant 流携带单次模型调用的用量快照；实际 `_requestId` 与 `_messageId`
共同标识调用。同调用的重复工具消息只计一次，更新快照替换该调用原贡献；缺少稳定标识的
旧协议仍按已有流式方式累计，不能视为已证明调用去重。缓存输入先归一为不含缓存的输入。

结束 `result.modelUsage` 可能包含同一会话之前轮次的累计用量。存在本轮 assistant 流时，
选择完整的流式来源，即使摘要模型键一致且数值更大；不拼接字段最大值、不猜模型别名、
不把两图相加。摘要与流不同时，两图和 `chosen_source` 记录在带任务上下文的 daemon
日志中，不是 API 可查询的 task_trace metadata。全新会话缺流时兼容已有摘要回退；续接
会话缺流时保留用量缺口，不能把会话累计量算成本轮消费。流式来源只证明已观测调用，不能
证明未落盘或未出现在流中的调用不存在。SDK 事件中已观测的实际 session ID 在结束包缺值
时仍予保留。CodeBuddy 实际请求标识与外部 AgentLens trace/span 的对应仍须通过外部观测
系统验证；平台精确关联落库不代表外部全链已验收。

精确本地来源的 CodeBuddy 关联可通过既有 traces 上报入口补充 `sdk_trace_id`、
`sdk_trace_source_sha256` 与固定 `sdk_trace_source_path=providerData.traceId`。这三项一起
提供，要求 `provider=codebuddy`、`correlation=exact`、`source=local_session_jsonl` 和实际
session；SDK ID 为32位十六进制，文件来源为 SHA256。它们保存在原行 metadata 并随执行
记录读回，`provider_trace_id` 继续保存实际 conversationRequestId。相同来源重放幂等；
SDK ID、文件hash或session冲突返回409，整批关联变更回滚；已有精确行的会话冲突仅追加
`session_conflict`审计，原身份、来源与SDK证据保留。旧daemon不传这些字段仍可上报。
SDK自报的 traceId 与外部 AgentLens 的解析/权限/逐span对照是不同证据，后者仍需单独验证。

取消立即终止backend，最多5秒收取末Result，期间继续消费已在途消息。已观测的 Usage、
session、provider 请求标识随结果保留，取消/超时/watchdog终态不变；末结果未返回时明确
记录 accounting incomplete，不能当成零消费。记账沿原daemon凭据，使用不继承父取消的
总5秒上下文上报usage和traces，session沿既有trace元组保存，不增加会话机制。

缓存节省与已知总费用分开判断：厂商报价本身不能推算缓存输入/读取费率。存在缓存读取
而缺少对应费率时，节省额显示未知；混合窗口不能把已知部分当完整节省。没有缓存读取时
节省额才可确认为零。自定义价格保存/删除后，页面按同一价格来源即时重算。

`POST /api/daemon/tasks/{id}/usage` 按 run 加锁并整批事务写入、回读核对。正常 daemon
在 runner 返回后上报一次完整 run/model 聚合，不传 `executed_at`；实时 token/cost counter
只记录首次成功写入的新实时模型行。重放、更正和显式 `executed_at` 历史补齐不向当前
counter 累计。Counter 是实时首次观测，能更正、可重新汇总的账本是数据库；已有 counter
不作负值修正。历史缺失行以已证明的 run.started_at 归入历史桶，属于 run/model 聚合，
不是逐调用小时账单；已有行更正保留原 created_at 与本次明确传回的权威费用。
