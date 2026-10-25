# 交付审查与质量监督

交付审查判断当前交付能否通过：实现是否完整、方案是否直接合理、业务验证是否充分，以及受影响的文档和提示词是否同步。质量监督是长期补充，可以深入核对源码、方案、用户问答、执行轨迹、跨任务重复问题和 token 使用效率。审查通过不阻止监督发现有新证据的缺陷；重大方案取舍交用户决定。

审查员只按既有评论和 review submit 入口提交结论，不直接开发或部署。监督员只使用本轮授权的监督 MCP，遵循现有 JSON、覆盖范围和证据协议，不修改代码、任务状态或全局规则。任务是否参与自动监督由调度代码决定，提示词不改变扫描范围。

## 真源与加载

| 材料 | 真源 | 消费者 |
| --- | --- | --- |
| 默认审查协议及职责 | `server/internal/review/default_instructions.md` | 未自定义的工作区审查设置 |
| 默认监督协议及职责 | `server/internal/supervision/default_instructions.md` | 未自定义的监督设置；只读前缀由 instructions.go 添加 |
| 项目分流与交付核对方法 | `skills/delivery-review/SKILL.md` | 绑定该技能的托管审查员 |
| 项目分流与深入调查方法 | `skills/delivery-supervision/SKILL.md` | 绑定该技能的托管监督员 |

技能源通过 `scripts/quality-agent-skills.py` 导入工作区并分别绑定现有托管角色，不创建 Agent，不加入全体 Agent 共享的内置技能。技能不包含凭据或部署操作。项目上下文按实际仓库、准确提交和授权来源选择；其他项目使用其自身规则。

托管角色通过各自设置端点更新，不直接修改 Agent instructions。平台已排队的监督任务保存提示词快照；配置更新不取消或重启现有运行。新默认提示词需随服务端程序发布才对未自定义的其他工作区生效。

监督只允许专用 MCP，无法通过普通文件工具加载技能正文。Daemon 在技能包解析后，将已绑定的 `delivery-supervision` 正文带入该轮指令，同时用于隔离执行环境及 CodeBuddy 显式系统提示词；不内联开发技能或其他角色技能。此能力需要发布相应 Daemon。审查使用普通技能发现与加载路径。

## 工作区同步

先预览，核对 profile 对应的服务端环境、登录身份和工作区：

```bash
python3 scripts/quality-agent-skills.py \
  --profile ~/.multica/profiles/goal-test-prod/config.json \
  --environment prod --workspace <workspace-uuid>
```

需要应用时增加 `--apply --backup <不存在的私有快照路径>`。脚本保存变更前设置、技能和绑定，不保存 token；通过官方 API 写入并回读。备份目录由操作者事先创建。快照含工作区私有内容，按私有资料保存。

同步保留开关、运行时、模型、推理等级、监督间隔、调查开关、并发和其他技能绑定。只迁移空提示词、与服务端默认一致的提示词、或本脚本上次维护的提示词；其他自定义内容保留并在预览标识。同名非托管技能、技能内容被用户改过或绑定已禁用时停止。技能附带文件不整体替换。同步前及设置写入前重新核对快照；API 无跨端点事务和版本条件写入，遇到并发管理请避开同一配置操作窗口。

部分失败不自动回滚或重发。先用快照和 API 回读核对结果，再运行预览；已创建技能按名称和内容标识复用，避免重复创建或替换其他绑定。恢复时通过相同设置端点保存快照字段，原先没有的技能只解除本次新增绑定，删除技能前检查其他消费者。

## 验证与依赖

配置同步测试使用内存 API 验证保留字段、角色绑定隔离、幂等、用户改动和中断恢复；它不等同于真实 Agent 验收。职责材料还应以受控案例检查：合理短答不误报、未同步的受影响文档能被识别、具体方案风险有依据、trace 缺失不编造 token 成本。真实工作区需回读技能及设置，并核对后续新运行是否加载对应技能。

GONG-67 维护自动监督范围与轮次，需按其交付实际发布状态核对，不能仅凭本次提示词称“只监督 done”已生效。GONG-51 维护执行、外部 trace 关联和用量准确性；关联与汇总未验证完整时，监督只分析可确认的局部数据，不宣称全部 trace 或精确节省。token 优化实现另按用户决定推进。


### Trace preparation rollback and recovery

Trace preparation is opt-in through `MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE`.
For a runtime rollback, unset that cutoff and restore the previous runtime and
supervision instructions/skill. Keep `supervision_trace_material` and existing
`trace_usage` materials. Migrations 640, 643 and 645 intentionally retain evidence,
the compatible material constraint and the consumed round allowance on downgrade;
a migration down file is not authorization to delete stored evidence. Do not
reset the preparation counters during recovery. External requests are reserved
atomically on the completion round before HTTP, shared across runs and delayed
versions, with 40 requests and 120 seconds from its first request. A crash may
consume allowance without retaining a response; it must never replenish it.

AgentLens `Retry-After` seconds and HTTP dates delay retries within that same
round deadline. A delay exceeding the remaining allowance yields partial coverage
without another request. Published evidence remains available after preparation
is disabled.

### 轨迹材料配置与权限

`MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE` 使用 RFC3339 时间，限定启用后新完成的自动轮次，避免批量重审历史任务。手动审查使用独立的一次性准备范围，不创建自动完成轮次；其请求额度、重试与产物同样持久化。取消准备只需关闭该配置；恢复不能清零已消耗的请求或自动补查额度。

AgentLens 凭据由服务端 `MULTICA_AGENTLENS_CREDENTIAL_FILE` 指向私有普通文件（权限 0600，禁止符号链接），内容字段为 `workspace_id` 和 `token`。由运维从已授权密钥来源挂载，不将密钥写入仓库、Skill、Agent 配置或日志。每次准备重新读取文件，轮换时原子替换私有文件；鉴权失败不重试，保留失败状态及本地监督材料，更新凭据后显式重新审查。工作区必须与文件声明一致；固定空间的可见范围继续由 Token Owner 的 AgentLens 权限约束。URL、空间与账号不接受模型输入，不调整网络或 SSH 配置。

新运行的本地快照随任务消息持久化；历史补读通过所属 runtime 的心跳请求同一 run/session。Codex 只读任务根目录索引指向且当前归属仍匹配的环境及指定会话；CodeBuddy 复用精确 CID/session/SDK 记录。目录已回收或归属改变时留下具体缺口，不搜索其他会话补齐。历史 Codex 用量同时限制起止时间，避免把后续续接的累计量算入早期运行。可见事件包括工具、压缩和 token 记录，不代表完整内部思维。

CodeBuddy 先按精确请求标识发现外部候选，再核对本地 SDK 溯源、session、response ID 和模型；CID、SDK ID 与外部 trace ID 分开保留。子调用出现外部 SDK 继承父 SDK 时，仍须由同一份父执行快照证明其子请求，并独立匹配 session/response。未验证的 span 正文不进入材料。未知格式或不全的列表保留缺口，不与本地 usage 相加或回填历史账本。

详情页先显示最新材料状态、已对照请求、差异与平台取数成本，再按需读取固定 UUID 的全文。模型调查仍受原有调用/字符预算约束。一次完成或手动范围最多消耗 40 次 HTTP 请求、120 秒，额度跨 run、版本和服务重启共享；最多登记一次自动延迟补查，其余由用户显式复查。生产同步提示词与技能仍需本任务的生产授权。

日常回滚优先关闭准备配置，保留当前支持 `trace_usage` 的读路径、材料和索引。若回退到尚不支持此材料类型的旧程序，先暂停监督，避免旧版本反复读取不认识的类型；不通过删除材料恢复运行。迁移 646–650 的恢复脚本保留所有权和额度记录，不作为删除或重置授权。

### 查询时间、分页与完成边界

AgentLens 查询使用目标执行的 `started_at`（缺失时取 `created_at`）到
`completed_at`，前后各扩展 5 分钟以容纳时钟差，并使用纳秒时间字符串。
缺少完成时间或时间倒置时保留明确缺口，不使用默认最近一小时代替。
时间只限定发现范围，正文仍须通过 CID、session、response 和 SDK 校验。
自动完成轮次与手动范围都只收录 `completed_at <= material_cutoff` 的终态执行；
截止后才结束的执行只可进入之后建立的范围，不能补进旧快照。

列表请求使用 `page_num`、`page_size=50` 和 `BY_START_TIME`，已有 JSON cursor
保存 reference、页码、页内 detail 位置、候选 ID、首个 total 与分页确认状态。
预算耗尽保留未完成位置，报告 `discovery_pending=true`；不会跳到下一个引用，
也不会重置轮次额度。每个新版本仍遵守原有不可变材料和一次自动补查上限。
响应未确认页码、total 变化、条数与 total 不符时，只核对已取得的候选，
保留待显式复查状态，不能声称发现完整。

2026-10-25 对既有测试执行的只读查询观察到：结束超过 12 小时仍可通过明确
时间与精确过滤取得链路；同一过滤的第一页 total=2/返回1条，第二页 total=1/
返回0条。服务接受 page_num 不代表该过滤下完整分页已经可靠；此限制保留为
真实接口缺口，不能用 HTTP 200 或空的后续页消除。原始正文和凭据不进入证据。

### 640–650 迁移兼容方案

新增表和并发索引继续走原有新增迁移门禁。以下五份非纯新增 SQL 使用准确文件
SHA-256 校验；任何正文变化或其他迁移均须重新评估，不能继承放行：

- 643 只扩展既有材料 kind 的 CHECK，保留所有原有取值，不更新或删除材料。
- 645 给完成轮次追加带默认值的额度列，旧查询不依赖这些新列。
- 646 新建手动范围表，给准备表追加默认 false 的 manual 标记。
- 649 创建只读范围视图，统一既有自动轮次与新增手动范围，不改来源数据。
- 650 给两类范围追加默认 false 的补查标记，不清零历史额度。

在本任务独占 PostgreSQL 17 实例应用全部迁移，并准备基线与候选的生成 SQL
及交付查询验证兼容性。发布器仍须对现场旧产物、新产物分别执行原有数据库
契约检查，验证备份和环境身份后才能切换；文件哈希匹配不能代替这些检查。
回滚停止新准备并保留表、列、视图、材料与额度；回退不支持 trace_usage 的
旧程序前先暂停监督。不得执行删除历史材料或重置额度的 down 操作。

此方案与门禁代码本身不构成生产发布授权，也不自动升级共享发布控制器或
解除已隔离槽位。当前控制器若仍使用旧规则，应通过既有维护流程应用已审查
控制器版本，再由平台恢复原分配槽位；任务 Agent 不手工改迁移台账、环境或
槽位状态来绕过隔离。
