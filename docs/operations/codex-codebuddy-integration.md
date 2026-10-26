# Codex 与 CodeBuddy 深度集成交付记录

## 已确认范围

2026-10-26，用户确认完整集成计划并要求实施，交付终点包含生产发布和真实业务验收。
基线为 `597d73fa8156289f5224c8f8b9c4eaac656ea429`；任务分支为
`feature/codex-codebuddy-integration`。主工作区和既有 DSH 交付不承担本任务开发。

1. 固定实际 CLI、版本、指纹和模型配置，采集优化前证据。
2. 原生 `/plan`、`/goal`、用户问答、审批、运行中追加输入和持久恢复。
3. 上下文、压缩、用量、工具耗时、失败分类，以及聊天和任务记录的完整分页。
4. 常驻会话、轮次凭据隔离、取消、崩溃和发布续接。
5. 定向优化 Codex / CodeBuddy 简报、角色技能、MCP 和监督配置；保留人工自定义内容。
6. 复用知识库和监督分析，完成六类真实任务、冷/热对照、固定 int 和生产验收。

Codex 原生子代理关闭；CodeBuddy 保留已有子调用，补齐轨迹、计量、等待和回收。
CodeBuddy 原生目标循环与平台续跑不能同时驱动同一目标。
原生目标成功不直接关闭业务任务。未知用量和不可观察指标不得记成零。

## 必须通过的业务检查

- 两种运行时：plan / goal 共存、问答、追加要求、刷新和重启恢复。
- 目标暂停、恢复、不可达、取消及终态；无重复调度或重复外部写入。
- 会话互斥、工作区和账号隔离、凭据解除、配置变化及进程树清理。
- 用量回放去重、迟到收尾、大历史分页、完整输出、脱敏和越权拒绝。
- CodeBuddy 子调用收尾和主子用量归属；DSH 现有路径回归。
- 每种运行时分别完成调查、修复、小功能、澄清、长输出和知识库检索对照。
- 五组冷启动与三轮热会话：实际进程复用，准备和握手中位数下降至少 20%；
  总耗时或 token 超过 10% 的退步定位修正。
- 准确 int 候选和生产程序身份、页面、真实执行、恢复、轨迹及统计验收。

## 当前完成度

2026-10-27 现场复核：独立交付 GONG-99 已将 `8ca8c661d` 纳入生产候选
`07531af70`，发布器成功，任务为 done。该交付的范围是 CodeBuddy 常驻池补交付；
生产聊天使用 Codex，不能据此关闭本文件的完整双运行时目标。下文各阶段的
“尚未发布”描述记录当时状态；第三十阶段及之后的改动尚未发布。

完整目标仍在开发与补验，部分能力已由 GONG-98 / GONG-99 发布。已实施原生 plan / goal、持久问答与审批、
CodeBuddy 子调用收尾、遥测分页、技能执行开关，以及运行中控制和持久投递入口。
真实 CLI 已验证目标循环、计划正文审批、技能开关、运行中查询与暂停；
数据库、协议与界面定向检查的证据分别记在下述阶段中。

剩余交付包括两种运行时的完整隔离与恢复验收、角色权限与提示词、知识库和监督优化、
完整冷 / 热业务对照，以及剩余增量的固定 int 和生产业务验收。CLI 层强制退出恢复不等于平台重启验收；
运行中控制的适配器故障对照已完成，平台重启与常驻恢复仍待验收。GONG-99 已有页面检查，
尚不能覆盖本完整目标的双运行时业务与剩余增量。

## 第一阶段：原生控制、遥测和分页

第一阶段实施：

- Codex 原生目标 set/get/pause/clear、计划模式参数和独立模式选择下发。
- CodeBuddy steer 请求与确认，按主会话和 provider request ID 绑定。
- Codex / CodeBuddy 统一上下文快照，Codex 计划和压缩事件；缺失指标不补零。
- runtime 消息游标分页，工作区查询缓存、加载更多、错误重试及部分数据提示。

已执行验证：

- 独立 PostgreSQL 的 5210 条记录分页，覆盖重复 seq 和损坏事件，5202 条有效记录无重复、无遗漏。
- Codex CLI 0.164.0 独立 home 原生目标控制和跨进程 thread 恢复通过；该检查未产生成功的模型调用；空白 home 的鉴权失败不能证明没有自动发起尝试。
- Agent 包默认测试通过（117.722s，使用 CLI guard）；新增控制测试、core 指标/API 测试、views 交互测试通过。
- core / views 类型检查通过；新增运行时和 daemon 路径 Go vet 通过。

CodeBuddy 空白、未认证 home 的原生命令探测未通过，不能据此声称目标控制可用；
仍需按测试账号配置验证其真实目标循环。常驻资源、角色配置、
子调用收尾、六类真实模型对照、浏览器、int 和生产验收均未完成。

私有执行证据位于控制仓库 `.run/evidence/codex-codebuddy-integration/`；其中
`test-database.json` 只用于本任务独立测试数据库，不得进入 Git 或公开报告。

## 第二阶段：持久化问答和审批

- Codex / CodeBuddy 原生问题、单选和多选答案、执行确认通过统一桥接；协议读取不会被等待用户阻塞。
- 请求绑定执行和工作区，回答使用事务锁并支持相同回答幂等重试；机器身份不能代替用户确认。
- 等待请求使用一分钟 daemon 续租，执行结束、取消、租约过期后失效，过期请求不能复活。
- 聊天与任务执行记录提供回答入口，历史执行实录按需读取已保存的回答。
- 两层看门狗识别等待用户；等待耗时作为独立交互事件记录，不计为模型耗时。
- Codex 秘密问题拒绝进入普通问答存储，要求使用凭据入口。
- 独立数据库的持久化、幂等、越权、终态和续租失效测试通过；原生问答回传、取消、daemon 桥接和看门狗 race 检查通过。
- Agent 全包在 CLI guard 与 IS_SANDBOX=1 下通过；core / views 类型检查通过；界面问答及实录测试 50 项通过；Go vet 通过。

上述问答验证使用数据库与协议夹具，尚未完成真实模型、浏览器、int 或生产验收。

## 第三阶段：CodeBuddy 原生 Goal 与子调用

CodeBuddy 2.176.0 实测：stream-json 会把 `/goal` 交给模型，并不执行原生命令。
因此普通会话沿用 stream-json，使用过 Goal 的聊天通过 ACP 原生控制恢复；
`_codebuddy.ai/goalControl` 负责目标状态和 CLI 自身的执行循环，平台不增加第二条续跑链。
裸 `/plan` 使用原生模式控制回执，后续普通输入仍按已选择模式执行。

- 原生 Goal 查询、设置、暂停、恢复、清除和会话续接已接入；`complete` 明确映射为结束并清除目标，不冒充原生判定成功。
- ACP 问答和一次性审批接入同一持久化桥接，不把一次同意扩大为永久授权。
- 原生控制的测试账号副本与实际服务账号目录分离。真实 CLI 查询、暂停、清除和跨进程续接通过。
- 真实 `deepseek-v4.1-flash` 的小目标由 CLI 原生循环完成，模型输出与原生终态通过；本次 CID 与 SDK 逐调用用量已对账。
- CodeBuddy 普通流采用可取消背压；700 条延迟读取输出完整，取消可以解除等待。
- 子调用终态不替换主会话、根 CID 或最终输出；主结果先到时等待已知前台子调用收尾，迟到子消息不复活已完成调用。
- 工具累计用时、交互累计等待、已观测子调用、原生占用与分类纳入独立遥测；SDK 日志用量与占用快照分别保存，缺失值保留未知。
- 聊天实录与任务执行记录共用游标分页，私有聊天和工作区权限校验通过；损坏分页响应进入重试流程。
- 取消先通过仍存活的原生进程暂停 Goal，再回收进程；协议进程测试和 race 检查通过。
- 原生判定内部调用未暴露的用量在上下文界面提示为未知，不作为完整总量。
- Agent 全包、后续定向协议测试、数据库分页与权限测试、core / views 类型检查及实录界面测试通过。

这些是真实 CLI 最小样本和协议测试，尚不构成六类任务或业务验收。
运行中 Goal 控制、Codex 自动续跑、模式状态投影、常驻资源与凭据切换、角色配置、
完整任务矩阵、性能对照、浏览器、int 和生产发布仍待完成。

### 全量 handler 检查边界

本次功能定向的数据库检查通过。全量 handler 检查仍失败，包含旧优先级值、
仓库迁移门尚未初始化及既有断言不匹配。固定基线同条件对照已完成：当前分支 230 个失败项全部也出现在基线，新增失败为 0。
不能把这些结果记作全量通过，也不据此跳过功能验收或提前发布。


## 第四阶段：Codex 原生 Goal 回合边界

Codex 0.164.0 的实际 CLI 验证了 `thread/goal/set` 自动发起回合，首回合输出后
会继续执行，直到原生目标完成。该行为同时与对应版本源码核对。

- 激活目标前建立准确模型、推理强度和协作模式的用户回合；原生目标负责后续续跑。
- 目标 active 的正常回合结束不关闭平台执行；失败、取消和目标终态仍按原生事件收尾。
- 后续原生回合重置完成标记；同回合重放和无法归属的无 ID 迟到完成事件不能重复计量。
- 取消执行先通过独立、有界请求持久化原生暂停，再中断当前回合并回收进程。
- 真实测试账号副本的适配层目标执行通过，精确收到原生 complete 状态和最终输出，
  报告用量已收集；本次检查显式关闭 Codex 原生子代理。
- 首回合在 Goal RPC 前结束的协议回归、取消次序、Codex 定向测试和相关 race 检查通过。

运行中目标控制、恢复时的活跃目标边界、模式状态投影、常驻资源与凭据切换，
以及完整业务和发布验收仍在执行范围内。


## 第五阶段：聊天模式恢复与输入封口

Codex / CodeBuddy 的 claim 直接读取数据库中的控制选择，不下载完整聊天记录。
查询只返回计划模式是否被选择、当前选择值和 CodeBuddy 原生目标传输标记；
全部聊天和交互历史仍由原表保存。

- 模式选择截至本次已经封口的输入，后续用户命令不能提前改变当前执行。
- 其他尚未执行的队列输入不参与当前模式；当前输入本身即使仍在队列状态也可以选择模式。
- 已持久化的计划批准表示退出计划模式，拒绝不改变选择；后续用户 `/plan` 可以再次进入。
- 目标命令与计划选择独立，助手文本不能选择用户模式，状态不能跨聊天会话读取。
- 真实 PostgreSQL 覆盖上述选择、批准、拒绝、队列和会话边界，检查通过；Go vet 通过。
- DSH 延用原有命令恢复路径，本阶段不改变其目标执行协议。

该查询复用已有持久记录，不增加数据库迁移。运行中控制和常驻资源仍未完成。


## 第六阶段：CodeBuddy 原生计划审批

真实 CodeBuddy 2.176.0 的 ACP `ExitPlanMode` 请求以工具元数据标识计划审批，
原始请求没有标题或计划正文。桥接按该元数据识别原生工具，并从当前账号的
`plans` 目录读取原生计划，完整显示在审批中。越出目录的路径和链接不能用于读取文件。

- 计划审批以 `plan` 类型持久保存，后续模式恢复可以识别用户的批准。
- 一次批准只选择 `allow_once`；拒绝选择普通 `reject_once`，避开同属拒绝类但会退出模式的 `reject_and_exit_plan`。
- 原生 cancelled 回执记为执行中止，区分于供应商失败。
- 真实测试账号的原生计划正文已通过适配层到达审批回调，受控测试拒绝执行通过；
  该测试没有将夹具回答写成真实用户批准，也未使用生产账号。
- 元数据中的后续扩展字段、审批结果、目录与链接边界及原生 Goal 路径的 race 检查通过。

协议事实依据实际 CLI，并参照 [CodeBuddy 设置](https://www.codebuddy.ai/docs/cli/settings)
及 [技能文档](https://www.codebuddy.ai/docs/cli/skills)核对后续角色配置能力。


## 第七阶段：CodeBuddy 技能选择执行链路

CodeBuddy 技能开关从 Agent 配置、claim、隔离环境到两条 CLI 启动路径贯通。
任务专属 `--settings` 同时配置技能隐藏和调用拒绝，不修改用户自己的设置或技能文件。
该策略存在时，冲突的运行时自定义 settings 参数不能覆盖本任务的禁用选择。

- 当前账号目录按 `CODEBUDDY_CONFIG_DIR` 发现技能；未配置时使用原生默认目录。
- Agent API 保存、重复禁用和重新启用的真实 PostgreSQL 检查通过；Claude 对照路径通过。
- 原生文件访问轨迹确认读取测试技能；ACP 首个命令清单没有展示该技能，不能用该清单独自证明最终可见性。
- 改用真实 CLI 与模型做启用/禁用对照。普通流和 ACP 均能调用启用技能，
  禁用后的显式 `/技能名` 均返回原生 `disabled via skillOverrides`，没有执行技能正文。
- 上述真实对照使用独立测试账号副本，测试包装器仅允许 Skill 工具；不构成业务任务矩阵验收。
- Agent 全包 103.809s、execenv 全包 23.093s 通过；相关 daemon 定向检查、API 检查与 Go vet 通过。

运行中控制、真正的进程复用、角色默认提示词和监督优化、完整业务对照与发布仍在范围内。


## 第八阶段：原生设置回执与追加消息协商

- CodeBuddy issue 追加消息的能力登记和提交 SQL 同时允许 CodeBuddy；此前服务端白名单使适配器无法进入真实投递流程。
- 追加消息的协商、去重、收尾、权限和终态等完整 PostgreSQL 检查通过，保留旧 daemon / 不支持 runtime 的关闭行为。
- CodeBuddy steer 能力仅向本次已实测的 2.176.0 及以上版本开放，未验证版本不宣称支持。
- Codex `/plan` 通过原生 `thread/settings/update` 更新，等待同线程、同模式和同模型的 `thread/settings/updated`；仅 RPC 排队回执不能当作成功。
- 模型未显式指定时使用原生 thread 响应中的实际模型；空线程的纯计划控制不发布尚未物化的 resume 指针，聊天模式由持久控制记录恢复。
- Codex 0.164.0 的完整实验协议 schema 已现场生成，验证模式控制字段和方法；真实纯计划控制覆盖默认与显式模型，未调用模型。
- 独立账号副本的强制进程退出探测证明，active Goal 跨进程保存并在 resume 后自行运行至 complete，客户端未发送 turn/start。
- 原生 turn/start 对已有回合执行 steer；适配器读取其权威回执补齐当前回合身份，拒绝迟到回执重新打开已完成回合。
- Codex 定向全量检查 65.126s、相关 race 和真实计划控制检查通过。

强制退出探测属于 CLI 层证据；完整 Multica 的运行中控制和重启业务验收仍待完成。

## 第九阶段：原生运行中控制

- 执行会话提供独立的原生命令入口与就绪状态；控制、取消与收尾共用串行门，终态执行拒绝恢复和设置新目标。
- Codex Goal 变更同时等待 RPC 回执及其后的匹配原生状态事件。迟到快照不能覆盖已经收到的预算终态；查询回执作为控制信息展示，Goal 状态来自有序事件。
- 已领取的控制使用有界执行上下文等待原生确认，调用方断连不能把已发送操作变成自动重发操作。
- CodeBuddy 在结束前重新确认原生 Goal 状态，避免旧终态快照关闭刚接受的新目标；用户暂停与原生目标失败分别处理。
- 定向 race 检查通过；Codex、CodeBuddy 和一次无关 Zeroclaw 临时脚本执行失败的复核检查通过（65.556s）。Agent 全包首次检查只有该 Zeroclaw `text file busy` 失败，未修改其实现。
- 实际 CLI 的运行中查询和暂停检查通过（24.04s）。Codex 暂停后完成当前模型回复并结束任务；CodeBuddy 在模型调用开始前接受暂停，未产生可归属的模型用量。

该阶段验收为适配器层入口，尚未贯通持久投递与用户界面；常驻资源、完整任务对照及 int / 生产业务验收仍待完成。

## 第十阶段：持久投递与界面入口

- 能力登记绑定具体 runtime 与派发批次；旧批次、不支持的 CLI、旧服务器和托管审查角色不能开放控制入口。普通 CodeBuddy 流提供文字 steer，原生控制仅向 ACP Goal 会话开放。
- 文字追加和原生命令分别持久保存。人类身份、Agent 调用权、工作区、私有聊天归属及运行状态共同约束提交；同一请求编号更换内容或作者时拒绝。
- daemon 只领取已经就绪的输入类型，命令直接进入原生控制，普通文字带入原任务追加说明。原生投递不自动重试；取消后仍完成有界回执，重复回执不会重复执行命令。
- 领取后缺少确认的输入展示投递结果未知；任务收尾区分未领取与已领取项，迟到的同次原生确认可补齐回执。终态任务不能领取或恢复新的输入。
- 聊天页和悬浮窗口使用同一投递逻辑，沿用现有输入框选择“下一轮 / 追加本轮”。已协商的原生命令直接控制本轮，附带文件的输入保留草稿并要求使用下一轮。
- 未确认提交的请求身份随草稿持久保存，刷新后的同文重试核对原记录；新意图、任务及工作区分别隔离，迟到回执不能清空另一会话或较新草稿。
- 执行详情显示控制入口、真实状态和原生回执；历史按需展开、按游标加载更早记录。数据库验证 208 条输入在相同时间戳边界无重复、无遗漏，畸形响应提供重试。
- 已确认的聊天运行中命令参与后续模式恢复，pending、未知结果和普通文字不能改变模式选择。聊天、issue、任务和工作区删除显式清理新记录；旧程序遗留的私有聊天记录也不能扩大可见范围。
- 新增迁移 675–678，仅在本任务独立数据库应用；能力与输入保存在独立表，旧程序的任务热表扫描结构不受影响，索引独立并发建立。

验证：真实 PostgreSQL 定向 race 检查通过（3.873s），daemon / Agent 投递 race 检查、core 六项、views 175 项、core / views 类型检查、相关服务端和入口 Go vet 通过。前端定向 lint 无错误，执行日志页保留两条原有 useMemo 依赖警告。迁移契约测试的 13 条既有失败与原始基线一致，新迁移真实应用成功；不宣称迁移测试全包通过。

以上为本地代码、真实数据库和已记录的适配器证据；浏览器、常驻进程、完整业务对照、int 和生产验收仍未完成。

## 第十一阶段：控制结果未知与原生执行收尾

- Codex 在 Goal 变更回执缺失后查询真实状态，不重放用户命令；查询期间的较新原生事件优先于快照。变更没有发送时保留普通执行，状态仍无法确认时关闭控制入口并报告失败。
- Codex 退出在关闭 stdin 前串行收拢控制请求，必要时查询并暂停未知 Goal；已经完成的 Goal 不会被清理操作改成暂停。未知的新 Goal 状态不能被当作成功。
- CodeBuddy 同样对不明变更读取原生快照。确认暂停或清除后使用实际状态收尾，历史失败回顾不能污染本次人工停止；无法确认时结束执行并执行原生暂停与进程回收。
- ACP 普通提示词写入成功后才开放运行中控制，防止用户新目标抢在本轮输入之前进入协议。
- 创建、恢复 Goal 必须走完整执行与计量生命周期，不能以纯控制模式提前返回成功。Codex 空线程的只读控制也不发布未物化的恢复指针。

验证：相关协议与生命周期 race 检查通过（9.534s）；两种目标运行时的定向检查通过（67.991s），相关 Go vet 通过。Agent 全包只有无关 Zeroclaw 别名测试的临时脚本 `text file busy` 失败，保留原日志并单独复核，不宣称全包通过。

真实 Codex 0.164.0 的查询、清除和计划模式控制通过。独立测试账号的故障注入丢弃一次用户 Goal 变更回执及其 active 事件，随后 Goal 原生运行至 complete，流式正文包含要求的标记，实际用量已归属；用户变更只发送一次，读取两次状态，未出现平台重复续跑（29.88s）。目标要求输出标记，检查按实际流式语义执行，最终答复仍采用 CLI 标注的 final_answer。

早期一次模型等待超时、一次直接调用的明确模型容量拒绝，以及校正前的断言失败均保留取证，不计入通过样本。本阶段是原生适配器层故障验证，不能替代平台的重启、浏览器或发布验收。

## 第十二阶段：后台角色的禁止审批策略

- daemon 已有的 `ApprovalPolicy=never` 由 Codex 新建和恢复会话显式下发；
  命令、文件、权限扩展、用户提问及 MCP elicitation 请求均拒绝，外部回答器不能覆盖。
- CodeBuddy 普通流和 ACP 使用 `dontAsk`，计划模式退出和前台子调用沿用禁止审批策略；
  后置自主模式参数不能覆盖，未知控制审批也返回拒绝。
- 普通编码任务沿用已配置的交互行为；本阶段不把“禁止审批”等同于文件只读或专用 MCP 隔离。
- 协议与新建 / 恢复参数的 race 检查通过（1.063s）；实际 Codex 0.164.0 与
  CodeBuddy 2.176.0 纯控制检查通过（4.09s），没有模型生成或用量上报。
- 扩大的 provider 检查仅 `TestCodexInterruptCompletesNearConfiguredDeadline` 失败：
  该检查要求总取消耗时低于 250ms；候选单独复查为 372.5ms，原始基线为 362.9ms，
  两者均失败。原生中断回执在约 123ms 到达；该耗时检查不能记为通过。
  agent / daemon Go vet 通过。证据为私有目录的 `phase12-*`。

常驻前的权限调查同时确认：完成和失败处理器已有提交后的 best-effort 任务令牌撤销，
不能称为“没有撤销”；鉴权查询仍仅检查令牌期限，终态事务与撤销之间的窗口需在常驻隔离中处理。
完整角色文件隔离、专用 MCP 与真实后台业务验收仍待完成。

## 第十三阶段：凭据轮次校验与常驻原生探测

- 公共任务令牌鉴权同时检查期限、任务处于 `dispatched` / `running`、令牌创建时间
  不早于当前 `dispatched_at`。终态提交后的撤销窗口、重新派发和运行时配置切换均不能
  恢复旧令牌权限；已有的提交后撤销与过期清理继续处理存储行。
- Codex、CodeBuddy 与共享鉴权回归用例通过真实 PostgreSQL 和 HTTP 中间件，覆盖
  终态、重排、重新领取、旧 / 新凭据、过期、配置变化和事务回滚；运行中输入、认领
  最终化和工作区选择器的关联检查通过。race 结果为 handler 2.298s、middleware
  1.053s、service 1.386s，证据为 `phase13-credential-db-tests.log`。无需数据库结构迁移。
- Codex 0.164.0 源码与真实协议确认：线程设置 `thread_unload_delay_secs=0` 后，
  `thread/unsubscribe` 会卸载空闲线程并回收 MCP。探测在新建且暂停的自有目标上执行，
  同一 app-server PID 两次恢复得到不同 MCP 环境与子 PID，两个子进程均在卸载后退出，
  没有继承任务令牌，也没有请求模型回合。
- 该探测只证明原生原语；尚未接入平台常驻池、shell 环境切换或崩溃恢复，不能算作
  三轮热会话、性能改善或业务验收。私有证据为 `phase13-codex-resident-rebind-probe.json`。

## 第十四阶段：Codex 子代理关闭策略

- 新建与恢复线程在请求配置中关闭原生子代理。0.164.0 及以上同时关闭
  `features.multi_agent_v2` 和 `agents.enabled`；已探测版本用于选择对应配置项。
  请求配置在原生加载顺序中高于启动参数，不能被用户 `--enable multi_agent` 覆盖。
- 任务 home 的环境变量开启通道已移除，受管配置始终关闭子代理；用户全局配置不写入。
  CodeBuddy 普通任务已有前台子调用能力按原范围处理。
- 新建 / 恢复请求与审批 race 检查通过（1.067s），任务 home 的环境覆盖、TOML 布局、
  幂等与共存检查通过（1.063s）；真实原生纯控制检查通过（4.28s），没有模型用量上报。
  此检查证明配置被 CLI 接受；完整模型任务矩阵仍待执行。
- agent / middleware / handler Go vet 通过，证据为私有 `phase13-*` 文件。
- 扩大的 Codex race 检查通过（71.775s）；明确排除第十二阶段已在原始基线复现的
  `TestCodexInterruptCompletesNearConfiguredDeadline`，未把它计入通过结果。

## 第十五阶段：CodeBuddy 常驻原语与轮次上下文验证

CodeBuddy 2.176.0 同一 ACP 进程连续 `session/new`、`session/load`，向同一会话传入
不同的 MCP 环境标记。第二轮启动新 MCP 子进程，旧 MCP 子进程已退出；进程保留同一
PID，MCP 均未继承任务令牌。探测只加载自有空工具 MCP，未发送模型 prompt，结束后
自有进程已退出。私有证据为 `phase15-codebuddy-resident-rebind-probe.json`。

该结果证明 MCP 配置可切换；当前每任务启动实现尚未替换为常驻池，也不构成冷 / 热性能结论。

后续真实模型验证：

- 同一进程修改 settings.env 后重新 load，第二轮 Bash 仍读到第一轮环境值。
  此路径验证失败，不能作为轮次凭据更新方式；证据为
  `phase15-codebuddy-resident-shell-env-probe.json`。
- 原生 `PreToolUse` 钩子通过 `updatedInput` 为 Bash 加载当前轮次的环境快照，
  不增加 `permissionDecision=allow`。客户端使用原生要求的 UUIDv7
  `conversationRequestId`，钩子核对相同的 `generation_id`。
- 同一 PID / 会话三个实际模型 prompt，分别输出第一轮值、第二轮值、未设置；
  随机命名的独立 shell 证明文件逐轮核对通过，避免把输入路径当成输出。
  旧 / 缺失 generation 和 idle 状态拒绝检查使用协议夹具通过。
  证据为 `phase15-codebuddy-resident-shell-hook-probe.json`。
- 原生 `UserPromptSubmit.additionalContext` 同一 PID / 会话连续两轮传入不同的随机
  简报标记，两个实际模型答复均匹配当前轮，未调用工具。证据为
  `phase15-codebuddy-resident-prompt-hook-probe.json`。
- 纯控制集成测试补上实际版本探测后，Codex 的 v2 / agents.enabled 配置也由真实 CLI
  接受；两种 CLI 共 4.07s，通过且没有模型用量上报，见
  `phase15-native-versioned-role-smoke.log`。

这些结果提供了可用的原生上下文入口；任务凭据、MCP、技能、简报、前台子调用和 Goal
必须一起纳入平台 lease 生命周期。常驻池接入、Goal 自动轮次与子调用的 generation
绑定、完整隔离恢复、业务对照、int / 生产验收仍未完成。

## 第十六阶段：Codex 常驻进程与任务轮次隔离

内置 Codex 0.164+ 在 Linux daemon 中使用有界原生进程池，容量沿用任务并发设置，
空闲两分钟回收。每轮独立 WebSocket 连接，线程配置携带当前任务的 shell 环境和
MCP 凭据；常驻进程环境与默认配置排除任务身份、令牌和临时目录。用户配置或账号
变化会替换进程。自定义 runtime、未知或旧版本及其他平台沿用原有执行路径。

归还租约前确认 Goal 已暂停、当前 turn 已结束、后台终端已关闭、线程卸载完成且
loaded 集合为空；未知状态回收进程，不把它交给下一轮。任务工具进程按随机轮次
标识回收。进程记录绑定 UID、PID 启动时刻与随机所有权，daemon 重启只清理本池
遗留进程；缓存期间固定相关环境和存储，防止 GC 回收正在引用的资源。

验证证据位于控制仓库 `.run/evidence/codex-codebuddy-integration/`：

- `phase16-resident-controls-ws.log`：原生控制路径通过，前两轮同 PID，配置改变后
  替换 PID，无模型调用。
- `phase16-resident-model-environments.log`：真实模型三轮通过（27.51s），同一原生
  PID；逐轮验证 shell 与测试 MCP 环境为 FIRST、SECOND、EMPTY，并确认 MCP 进程
  收尾与实际 usage。测试 MCP 只证明环境切换，未覆盖真实知识库业务。
- `phase16-resident-goal-persistence.log`：原任务 home 内，关闭并重建池后恢复原生
  Goal 通过。`phase16-codex-cross-home-goal-probe.json` 另证实：只共享 rollout、
  换任务 home 会丢失 Goal，不能用前一条通过掩盖该缺口。
- 池的并发、容量、取消、空闲回收、崩溃遗留恢复和环境配置 race 检查通过；daemon
  GC 固定检查通过。Codex 回归通过（73.645s），排除已在原始基线复现的截止时间
  用例；agent/daemon vet 通过。原生 Unix 入口是 WebSocket，初次 raw proxy 探测
  失败保留取证。

当前复用仍要求相同任务 home；跨任务 home 的稳定 SQLite、rollout 路径衔接与资源
配置归一化尚待实施。CodeBuddy 平台常驻池、完整性能对照、固定 int、生产和浏览器
业务验收也未完成，本阶段不代表完整计划交付。


## 第十七阶段：跨任务 home 的 Codex 原生状态

领取响应通过已选会话 ID 解析来源任务，限制 Agent、Runtime、Issue / 聊天及聊天
代际；手动重试限定原任务。取最新已结束的同会话 producer，不能按任务创建时间
错误地恢复较旧的 Goal 计量。daemon 通过归属索引定位私有任务目录并取得占用，
不用工作目录推测来源。原池进程先收尾；等待短暂的辅助进程退出，仍声明使用该 home 的外部进程会阻止迁移。

默认 Linux 内置 Codex 的 SQLite 固定到会话存储下、按 Runtime 隔离的位置。
使用纯 Go SQLite 驱动的 [Backup API](https://www.sqlite.org/backup.html) 生成一致性
快照，包含 WAL 已提交数据，保留 rowid；原数据库保留。原 sessions 真目录保留为
备份，当前路径链接到持久 rollout 存储，后续任务也直接使用该存储。人工指定的
`sqlite_home` / `CODEX_SQLITE_HOME` 沿用用户配置。

0.164 的分页历史不能仅从 rollout 重建；直接指定迁移后的新 path 会被原生拒绝，
只传 thread ID 也不会修复失效的旧路径。因此在尚未发布的 `state_5` 快照中校验每个
活跃线程的 rollout 元数据，再调整索引的文件位置；Goal、分页内容和原生 schema
均不重写。未知的新索引版本拒绝迁移。原生恢复按 ID 发起，并核对回包线程身份与
持久文件身份；缺失、冲突或恢复失败时保留现场，不自动换新会话绕过。

池以稳定 SQLite 位置绑定会话；新任务 home 不再单独决定 PID。账号、静态配置、
指令、技能资源、插件和规则的变化参与失效检查。绝对资源路径或工作目录变化仍可
触发合法的进程替换，完整热会话矩阵尚待验收。

本阶段证据仍位于控制仓库 `.run/evidence/codex-codebuddy-integration/`：

- `phase17-session-source-tests.log`：隔离数据库的来源范围、重试与聊天代际检查。
- `phase17-native-state-final-tests.log`：race 检查覆盖 WAL、非零夹具数据、rowid、
  原目录删除后恢复、分页索引与内容、冲突拒绝及外部占用拒绝。非零计量是 SQLite
  夹具数据，不能冒充真实模型产生的原生 Goal 用量。
- `phase17-native-goal-owned-source-final.log`：真实 0.164 创建暂停 Goal 后迁移，删除原
  任务目录，两轮新 home 保留同一原生 Goal 和同一 PID，通过（4.13s，无模型调用）。
- `phase17-codex-regression.log`：Codex race 回归通过（73.776s），排除已在原始
  基线复现的截止时间用例。`phase17-daemon-final-tests.log`：daemon race 通过
  （45.305s），排除基线同样失败的缓冲时间戳用例。execenv 全包 race 通过
  （36.898s）；新增备份路径另有定向检查。
- 全量 daemon 最初另有一项旧 MCP 转录断言失败：新增原生耗时事件占据 seq=2。
  已更新该验收，明确检查 tool_use、原生耗时和 tool_result 的顺序与数据；定向通过。
- `phase17-native-paged-history-model.log` 的恢复模型回合超时；随后
  `phase17-native-paged-history-model-recheck.log` 连迁移前的冷启动模型回合也超时。
  不能把超时归因为迁移，也不能把这些样本计为模型上下文恢复通过或性能通过。

真实模型读取迁移后分页上下文仍待通过；压缩 rollout 的迁移兼容、active Goal 的
无加载纯查询、完整业务与性能矩阵也需继续处理。CodeBuddy 平台常驻池、角色与知识库
监督优化、固定 int、生产与浏览器验收尚未完成。

## 第十八阶段：Goal 查询与停止操作不加载线程

已物化会话的 Goal 查询、暂停、完成和清除直接调用原生 Goal API，不经过
`thread/resume`；后者会续跑 active Goal。控制结束仅确认 loaded 集合为空，
不把读取状态当成需要暂停原目标的执行。激活目标仍走完整执行与计量生命周期。

- `phase18-stored-goal-tests.log`：协议与 race 检查确认查询没有 thread/resume，
  清理没有隐式暂停，激活命令与普通执行不会进入纯控制路径。
- `phase18-native-unloaded-goal-controls.log`：真实 0.164 查询后 Goal 仍为 active，
  随后的暂停、完成和清除逐项核对原生数据库；turn 数始终为 0，usage 条目为 0，
  原生进程保留，通过（2.99s）。该样本没有模型回合。
- `phase18-codex-regression.log`：Codex race 回归通过（74.138s），排除此前已在
  原始基线复现的截止时间用例。

压缩历史兼容、真实模型分页上下文恢复、CodeBuddy 常驻池、完整隔离和业务对照、
固定 int 与生产验收仍待继续完成。

## 第十九阶段：压缩 rollout 的恢复兼容

迁移接受原生 `.jsonl.zst`，通过有界流解码校验会话元数据，保留压缩原始字节。
Codex 恢复后可以生成同目录、同名的可写 `.jsonl`；身份核对接受这两种编码之间的
转换，同时仍核对线程 ID、文件名和实际父目录，拒绝别的线程或目录。

- `phase19-compressed-unit-tests.log`：压缩字节保留、元数据与线程身份检查通过。
- `phase19-compression-identity-tests.log`：格式转换、原编码删除后的验证，以及
  跨目录和跨文件名拒绝的 race 检查通过。
- `phase19-native-compressed-next-task.log`：真实 0.164 在原目录删除后恢复压缩
  会话，更新计划模式并确认可写文件；下一个新任务再次准备该会话成功，通过
  （4.36s，无模型调用）。首次严格按物理文件核对而拒绝格式转换的证据保留。

该样本验证原生加载与模式更新；真实模型读取迁移后分页上下文仍需完成，不能替代
完整业务对照、CodeBuddy 常驻池或固定 int / 生产验收。

## 第二十阶段：CodeBuddy ACP 父子请求归属

真实 2.176 的前台子调用工具名为 `Agent`。隔离账号中的一次真实调用完成了
Agent → Bash → 父请求收尾；父 CID 为客户端指定的 UUIDv7，子 CID 为 UUIDv4。
`SubagentStart/Stop` 提供同一原生会话和独立 agent_id；ACP 子消息提供
`codebuddy.ai/parentToolCallId`。子调用早期工具帧可能仍携带父 CID，不能只凭
CID 变化识别父子，也不能要求子 CID 必须为 UUIDv7。

ACP 接入现在依据父调用字段隔离根请求：子消息继续写入执行记录与计量候选，
但不替换根 CID、不清空父答复，也不结束父请求的补充指令入口。

- `phase20-codebuddy-agent-generation.json`：真实前台子调用通过，原生进程退出。
  初次只开放 `Task` 的探测未调用子代理，保留为 PARTIAL，不能算通过。
- `phase20-codebuddy-regression.log`：CodeBuddy race 回归通过（11.532s）。新增协议
  场景验证子调用结束后仍向父请求投递 steer，父输出和根 CID 正确，子正文仍可见。

该样本验证前台子调用协议；常驻池仍需登记子代关联、解除旧凭据并验证 Goal 自动
回合的代际归属，尚不能据此宣称平台常驻隔离或业务验收完成。

## 第二十一阶段：原生前台约束和 Goal 自动回合

ACP 启动采用与 stream-json 相同的受管环境，强制前台执行；继承环境和任务配置
中的 `CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS=0` 均不能覆盖该约束。前台 Agent
仍可用，不能把禁用后台误实现为删除全部子调用能力。

- `phase21-codebuddy-foreground-tests.log`：CodeBuddy race 回归通过。子调用夹具在
  环境和任务配置均要求后台时，核对原生进程实际收到的前台约束。
- `phase21-native-foreground-child.log`：通过当前 Go ACP 适配器和真实 2.176 / Flash
  完成前台 Agent → Bash → 父答复，通过（14.94s）。子正文可见，最终输出仅归父
  请求，父子 SDK 用量有独立轨迹（1 个模型、1 条额外轨迹）。使用隔离账号及专用
  工作目录，配置故意要求后台，适配器仍执行前台约束。
- `phase21-codebuddy-goal-generation-contract.json`：真实 Goal 自动执行通过；
  `UserPromptSubmit` 提供 `is_internal_continuation=true` 和原生 generation，
  与 ACP 根请求及后续 Bash hook 对应。Goal 的 CID 由 CLI 生成；原始探测结果中
  未使用的随机客户端 CID 不能当成原生父 CID。

这些证据补齐前台生命周期和自动回合协议，CodeBuddy 常驻凭据换发与旧代际拒绝仍
需实现。完整业务、性能、固定 int 和生产验收状态仍为未完成。

## 第二十二阶段：原生 hook 的执行代际与计量

Linux 内置 CodeBuddy 2.176+ 的 ACP 执行接入受管 hook；旧版、自定义协议和其他
系统仍走原执行路径。当前每次执行各有独立的私有 Unix 入口、随机凭据和不可变
环境文件，结束后关闭入口并移除文件，不复用进程。

- 普通 prompt 显式登记原生 UUIDv7 CID；Goal 仅接受已启用目标循环的原生内部
  continuation。错误会话、未知根代际、已结束子代、旧 CID 重放和取消后的请求拒绝。
- 前台子代只能从已知 Agent 调用登记，按 CID 与 agent_id 校验；结束后解除关联，
  已用过的 CID / agent_id 不能重新取得环境。兼容原生子 CID 为 UUIDv4 的行为。
- Bash hook 更新真实输入，先读取本轮环境快照，清除继承的旧任务变量；不设置
  `permissionDecision=allow`。原生系统指令通过启动参数注入，既有权限和技能设置、
  已有 hook 正文保留；本阶段未接入常驻进程的动态简报换发。
- CLI 提供仅供原生 hook 使用的内部转发入口，校验私有入口、代际与任务状态。
  非零退出阻止未归属的工具继续执行；它不调用业务 HTTP API。

真实 Goal 检查同时暴露了计量缺陷：同一 CID 可以跨多个 SDK trace，单 trace 的
严格解析会丢弃已知用量。原生计量现在逐 trace 校验，跨 trace 去重模型响应，
冲突数据拒绝计费。多个 SDK trace 的 CID 不填写单一 SDK 溯源字段；逐调用运行时
事件保存各自的 SDK trace、完整源文件 SHA256 与用量，不任意选一条冒充全部。

证据位于控制仓库私有目录：

- `phase22-codebuddy-hook-tests.log`：CodeBuddy / 子调用 race 回归通过（10.652s），
  覆盖归属拒绝、目标内部回合、子代解除、旧代际重放、取消、入口认证与收尾，
  以及 shell 字面值、旧变量清理、用户设置保留、多 trace 计量与冲突拒绝。
- `phase22-native-managed-hooks.log`：真实 2.176 / Flash 的前台 Agent 子调用通过
  （17.64s），Goal 自动回合通过（48.56s），有实际逐调用用量。启动包装器先删除
  随机标记，模型不知道标记内容，Bash 证明文件中的值来自受管 hook 快照。
- `phase22-native-managed-hooks-first.log`：保留首次 Goal 已完成但计量缺失的失败
  证据；本阶段修复后通过，不把首次结果计作完整通过。
- CLI 构建与 Agent / daemon / CLI 的 Go vet 通过。

当前原生根进程仍按单次执行持有环境，不能宣称已完成常驻凭据隔离。
CodeBuddy 有界常驻池、MCP / 简报换发、常驻收尾与崩溃恢复、角色和知识库监督、
真实任务与性能矩阵、固定 int 和生产业务验收仍待完成。

## 第二十三阶段：每轮简报与空闲 MCP 解除

受管 hook 可用的 CodeBuddy 原生执行，从当前执行的 `UserPromptSubmit` 取得任务
简报。daemon 与 backend 共用原生分派判断；普通 stream-json、旧版和自定义 profile
沿用文件路径。原生路径从 CODEBUDDY.md 中精确移除平台拥有的简报块，保留人工
正文的原始字节，不创建空的受管记忆文件。`SystemPrompt` 通过原生参数投递。

冷会话重试重新生成 hook 简报及来源指纹；无法准备当前简报时停止重试并保留已知
用量与链路。来源记录标识 `delivery=native_hook`，不把不存在的简报文件当成证据，
也不在诊断中复制指令正文。

- `phase23-native-hook-brief-tests.log`：CodeBuddy 与上下文文件的 race 检查通过
  （Agent 10.722s，execenv 1.178s），覆盖旧代际拒绝获取新简报、人工正文和尾部
  字节保留、旧平台块解除、hook 投递指纹及原生系统指令不重复注入。
- `phase23-native-hook-brief-model.log`：真实 2.176 / Flash 两次冷执行通过
  （8.29s、6.69s），第二次恢复同一 SID，取得新随机简报标记，使用新 CID 且有
  独立逐调用用量；工作目录没有简报文件。该样本不证明同 PID 热复用。
- `phase23-codebuddy-idle-detach-probe.json`：同一 PID 的两轮协议检查通过。
  每轮切到不带受管 MCP 的空闲会话，旧 MCP 确认退出；随后恢复原 SID 并传入新
  环境成功。此处 MCP 是启动/退出探针，没有 prompt，不是知识库业务验收。
- `phase23-daemon-brief-tests.log`：daemon 的监督、提示词、恢复及链路相关 race
  检查通过（1.322s）；CLI 构建与相关 Go vet 通过。

有界进程池、租约间的完整凭据和配置换发、常驻收尾与崩溃恢复仍需实现与验收。
角色和知识库监督、完整业务与性能对照、固定 int 和生产验收仍未完成。

## 第二十四阶段：CodeBuddy 常驻租约与真实三轮复用

Linux 内置 CodeBuddy 2.176 及以上的聊天和任务会话使用有界 ACP 进程池。
逻辑会话按工作区、Runtime、Agent 和聊天或任务隔离；同一会话串行执行。
根进程剥离任务环境，每轮由受管 hook 换发 Bash 环境与简报，由 ACP 换发受管
MCP 环境。RPC 编号跨租约递增，输入和输出按当前 SID、CID 与租约归属过滤。
原生用户、项目、本地或已安装插件的 MCP 配置需要完整进程环境时，使用池内
独占的一次性进程；显式最大回合数也按一次性进程处理。

任务完成后在原 SID 上执行不带受管 MCP 的 `session/load`，确认解除后才释放
租约。原生 load 回执可以省略 SID，按恢复协议核对其身份，不能将省略误判为失败。
空闲进程到期回收；daemon 关闭、取消与异常路径回收所属进程树。私有目录、锁、
owner 和 execution 标记用于恢复清理，环境目录在进程存活期间受 GC pin 保护。
配置指纹区分账号与组织身份、认证配置和 SDK 自行刷新票据；派生产品缓存和
时间戳不会单独触发换进程，技能、设置和有效能力变化会使旧进程失效。

控制仓库私有证据：

- `phase24-native-same-session-fixed.log`：真实 2.177 / Flash 三轮通过，均为
  同一 PID、同一 SID。各轮随机环境值和简报标记正确，MCP 的任务凭据摘要
  正确，任务结束后 MCP 退出，根进程未持有任务凭据，每轮有实际模型用量。
  三轮耗时分别为 9.603s、4.518s、5.432s；这不是完整性能对照矩阵。
- `phase24-native-same-session-fixed-trace.jsonl`：对应的 ACP 路由元数据。
  早期缓存指纹、空闲会话切换和 load 回执处理失败记录均保留，未计作通过。
- `phase24-agent-regression-final.log`：CodeBuddy、Hermes 与 DSH 相关 race
  回归通过（17.333s），覆盖租约串行与容量、旧回合隔离、原生 MCP 范围、
  账号切换、票据轮换、进程归属恢复和反向请求的字符串编号。
- `phase24-daemon-regression.log`、`phase24-daemon-ownership.log`：daemon、
  execenv 相关回归与会话归属、环境 GC pin 检查。
- `phase24-build.log`、`phase24-vet.log`：CLI 构建及相关 Go vet 通过。

自定义 settings 参数与受管 hooks 的完整合并、原生子 Agent 恢复能力仍需核对。
角色工具权限与知识库监督、完整业务和性能对照、固定 int 及生产验收仍未完成。

## 第二十五阶段：人工 settings 与受管 hooks 合并

原生 hook 设置接收用户 `--settings <file-or-json>` 和 `--settings=...`，按 CLI
参数顺序选取最后一次设置，再叠加任务策略。文件路径以任务工作目录解析；
对象递归合并，hooks 和权限等数组去重合并，任务的技能禁用选择优先。
合并内容只写入私有 hook 设置，不修改人工文件或任务策略文件；无效来源明确报错。
有效设置内容参与常驻指纹，内容变化会使旧进程失效；设置中含原生 MCP 时使用
池内一次性进程，避免遗漏其环境要求。

- `phase25-settings-tests.log`：CodeBuddy race 回归通过（16.980s）。
- `phase25-settings-final-tests.log`：人工 hook、权限、环境、任务技能禁用、
  inline JSON、路径解析、无效来源拒绝及设置内容指纹检查通过。
- `phase25-native-human-settings.log`：真实 2.177 / Flash 同 PID、同 SID 三轮
  执行通过（10.375s、4.778s、5.027s）。人工 Bash hook 每轮真实执行一次，
  受管环境和简报仍为当前轮，MCP 凭据与回收检查通过，逐调用用量存在。
  人工与任务设置各包含独立权限拒绝项；合并结构由定向测试核对。

原生子 Agent 恢复、角色工具权限、知识库监督、完整性能矩阵和环境验收仍待完成。

## 第二十六阶段：原生子 Agent 恢复与回合计量

受管 hook 允许当前前台 Agent 调用通过显式 `resume` 恢复已知子 Agent，要求
新的回合 CID，且每个前台调用只登记一个子回合；旧 CID 重放、无恢复意图的旧
Agent ID 和已结束子回合仍被拒绝。恢复执行继续从当前租约取得环境。

恢复后的子会话文件包含历史回合。ACP 计量按当前执行实际观察到的 CID 分组，
核对当前父调用的子 Agent 归属，只采集本轮调用并保留完整文件的来源指纹。
历史回合不重复计费，冲突调用不部分采纳，多 SDK trace 的规则同时适用于子调用。

- `phase26-native-child-resume.log`：保留首次失败；实际恢复与 Bash 环境正确，
  但旧解析器拒绝多回合子会话，缺少子链路，未计为通过。
- `phase26-native-child-resume-fixed.log`：真实 2.177 / Flash 两轮通过，
  首次前台子调用 15.439s，恢复原子 Agent 11.595s。同一父 PID 和 SID，
  每轮子 Bash 证明当前环境，父输出独立，实际逐调用用量和子链路存在。
- `phase26-scoped-accounting-tests.log`：hook 的恢复授权与旧代际拒绝、
  子会话历史排除、冲突用量拒绝和原生多 trace 定向 race 检查通过。
- `phase26-final-regression.log`：CodeBuddy、Hermes 和 DSH 相关 race 回归通过
  （17.961s）；`phase26-build.log`、`phase26-vet.log` 的 CLI 构建和 Go vet 通过。

角色工具权限、知识库监督、完整业务和性能矩阵、固定 int 和生产验收仍未完成。

## 第二十七阶段：CodeBuddy 监督专用工具

CodeBuddy 监督使用私有任务配置目录，保留模型定义、默认模型和变体选择，账号
认证沿用原 HOME。人工启动 hooks、插件加载目录和工具扩展不进入该执行配置。
监督采用一次性 stream-json 进程，关闭内置工具和用户 MCP；专用监督 MCP 设置
`alwaysLoad=true`，使其无需内置 ToolSearch 即可使用。原生 Goal 与常驻租约不能
进入这条路径。普通任务和交付审查继续使用各自的工具与人工设置。

- `phase27-codebuddy-project-hook-surface.log`：真实 2.177 CLI 将请求发到自有
  本地采集端，主提示词存在，实际工具列表只有指定 MCP 工具，人工与项目启动
  hook 均未执行。模型端是本地 HTTP 采集器，没有真实推理。
- `phase27-native-supervision-mcp.log`：真实 Flash 推理和 Multica 自身的监督
  MCP 通过（10.348s）。模型使用配置中的默认模型，仅调用一次监督读取工具，
  返回授权文件中未出现在提示词的随机内容；MCP 保存了读取审计，人工启动
  hook 未运行，有实际模型用量。来源及审计为本任务自有离线清单，没有调用
  业务服务端的监督保存或结果校验 API，不等同于线上监督闭环。
- `phase27-agent-regression.log`：CodeBuddy、Hermes 和 DSH 相关 race 回归通过
  （18.001s）。`phase27-daemon-final-tests.log` 的监督、常驻与简报相关检查通过
  （daemon 1.278s、execenv 1.078s）。模型设置保留、人工规则隔离及工具参数
  不被自定义参数扩大由定向测试核对。
- `phase27-build.log`、`phase27-vet.log`：CLI 构建与相关 Go vet 通过。

该阶段只观察了 Codex 请求顶层的 tools 字段，不能据此证明监督隔离；后续改用
原生注册工具元数据和实际调用结果核对，见第三十阶段。
Codex 角色权限、知识库和监督平台闭环、完整业务与性能矩阵、固定 int 和生产
业务验收仍未完成。

## 第二十八阶段：CodeBuddy 启动对照与模型配置失效

使用相同 2.177 / Flash 配置，串行执行五次新进程冷启动和三轮同 PID、同 SID
热会话。每次均有真实模型输出与用量；从实际 process 遥测读取准备和握手耗时，
不从模型耗时倒推。对照案例为精确回复当前随机文本，不调用工具，不代表六类
业务或原基线 stream-json 与新 ACP 之间的完整对照。

`phase28-codebuddy-startup-comparison.json` 保存逐次样本，日志对应
`phase28-codebuddy-startup-comparison.log`（47.02s，PASS）：

| 中位数 | 五次冷启动 | 三轮热会话 | 变化 |
| --- | ---: | ---: | ---: |
| 准备和握手 | 1,446 ms | 17 ms | -98.8% |
| 整轮耗时 | 6,003 ms | 2,758 ms | -54.1% |
| 已观察 token，含缓存 | 21,898 | 22,094 | +0.9% |

准备和握手下降超过 20%，该样本的总耗时与 token 没有超过 10% 的退步。
这不是账号扣额或不可观察内部调用的完整成本比较，也不外推为所有任务的收益。

账号及项目 `models.json` 同时纳入常驻配置指纹，模型定义变化会换进程。
`phase28-model-config-tests.log` 的模型配置变化、设置合并、常驻租约及监督相关
race 检查通过；`phase28-vet.log` 的 Agent Go vet 通过。

Codex 监督工具范围、知识库和监督平台闭环、两种运行时六类任务对照、固定 int
和生产业务验收仍未完成。

## 第二十九阶段：实际 CLI 身份与固定制品复验

现场版本复核发现安装路径仍为 `versions/2.172.1/codebuddy`，文件在 2026-10-27
00:31 更新，实际报告 **2.177.0**。第二十四至二十八阶段的新测试在此之后执行，
测试配置中的 `CLIVersion=2.176.0` 是过时声明。相关文字已纠正；原始日志与
第二十八阶段 JSON 保留，其 `cli` 字段不能作为实际版本证明。

真实测试统一解析实际执行路径，调用版本检测，记录 SHA-256，并在结束时核对
二进制未变。测试使用任务私有的固定二进制副本，并关闭该测试子进程的原生自动
更新；不依赖可变安装路径或目录名推断版本。

固定版本为 2.177.0，SHA-256 为
`a246ed8113ae5d49ac57665a2a577dec36e9e330f90255a5039ea0c07bf324b7`。
`phase29-codebuddy-pinned-validation.log` 六项真实检查全部通过：

- 原生 Goal 循环和计划审批。审批回答来自测试回调，不代表真实用户批准。
- 三轮同 PID、同 SID 任务，当前环境和简报、人工 hook、MCP 凭据与回收。
- 同一父进程恢复原前台子 Agent、换发当前环境和主子计量。
- 专用监督 MCP 读取授权来源、保存本地审计，并隔离人工启动 hook。
- 五次冷启动与三轮热会话，实际模型结果和进程身份对账。

固定制品的启动对照见 `phase29-codebuddy-pinned-startup-comparison.json`：
准备与握手中位数 **1,483 → 18 ms**，整轮耗时 **5,982 → 2,798 ms**，
已观察 token **21,894 → 22,019**（+0.6%）。该固定版本结果作为当前启动小样的
验收依据；完整业务矩阵、Codex 监督隔离、固定 int 和生产验收仍待完成。

## 第三十阶段：Codex 监督的原生执行边界

Codex 0.164.0 使用独立会话和显式空执行环境，业务读取只经专用监督 MCP。
关闭 shell、插件、应用、外部工具和交互入口，保留模型与 provider 配置；原生
Code Mode 计算桥、时钟、当前 MCP 的资源查询仍存在。文件补丁工具仍会注册，
但在获取执行环境时被拒绝，不能用 approval=never 替代这一边界。

配置同时应用于进程、已有 profile 和当前线程。自定义参数不能重新开启工具，
内联整表 profile 返回明确错误，避免丢弃模型配置后误用其他账号。监督不复用
历史线程，不进入常驻池，也不开放运行中控制或追加输入。当前实现只接受已验证
的内置 0.164.0，启动声明和原生 initialize 回执分别核对；普通执行的子代理约束
使用握手取得的实际版本。

监督角色及绑定的 `delivery-supervision` 技能通过 thread/start 的开发者指令
直接投递，关闭工作目录指令与宿主技能发现。任务来源记录对应原生投递及正文
指纹，不复制正文；清理旧托管简报时保留人工文件的原始字节。

- `phase30-native-instruction-boundary.log`：通过 Multica 适配器驱动真实
  0.164.0 CLI 和真实 Multica 监督 MCP，两个对照均通过（1.65s）。基线的受控
  补丁写入自有文件；监督模式同一调用收到原生“无可用执行环境”错误、文件未
  创建；随后专用 MCP 成功读取授权来源并保存本地审计。监督控制和追加输入
  不被声明为可用；请求中的角色正文仅出现一次，工作目录人工指令未混入。
  模型端是脚本化 Responses 服务，不是真实模型推理或平台
  监督结果保存 / 校验 API 验收。
- `phase30-final-unit.log`：模型路由保留、工具覆盖拒绝、profile 及新会话约束、
  原生线程设置的定向 race 检查通过。
- `phase30-final-runtime-regression.log`：Codex、CodeBuddy、Hermes 和 DSH 相关
  race 回归通过（89.371s），只排除已在原始基线复现的
  `TestCodexInterruptCompletesNearConfiguredDeadline`。
- `phase30-daemon-regression.log`：监督、常驻与简报相关 race 检查通过
  （daemon 1.255s，execenv 1.059s）；CLI 构建及相关 Go vet 通过。
- 原生指令投递变更后的定向 race 检查再次通过（agent 1.152s、execenv 1.106s、
  daemon 1.315s），涵盖人工文件保留和来源指纹；相关 Go vet 通过。

早期只看 API 顶层 tools 字段得出的“未暴露工具”不能作为结论。原生注册工具
元数据和受控调用证明基线确有能力，配置后的文件拒绝与 MCP 可用性分别核对。
真实模型监督、CodeBuddy 特殊配置兼容、知识库与六类业务 / 性能对照、固定 int
和生产业务验收仍未完成。

后续真实模型检查 `phase30-real-supervision.log` 通过：Codex 0.164.0 与
`gpt-6.1-sol` 实际调用一次监督 MCP，返回与授权文件完全一致的随机内容，
本地读取审计和用量均已收集（8.729s）。平台监督结果保存 / 校验和知识库业务
验收仍需单独完成。

## 第三十一阶段：CodeBuddy 关闭 hooks 的兼容执行

账号、项目、局部或显式设置关闭 hooks 时，普通任务使用独立 stream-json
进程和文件简报，完整任务环境直接下发；不领取常驻租约，不改写人工设置。
无法解析的原生设置同样由独立 CLI 处理。原生计划控制可使用独立 ACP，Goal
在 hooks 不可用时返回明确错误，不替用户开启 hooks。daemon 简报选择与执行
分派使用相同的配置目录、自定义参数和任务设置。

- `phase31-real-ordinary-compatibility.log`：固定 CodeBuddy 2.177.0、真实模型，
  自定义参数、项目和账号三种关闭方式均通过（27.78s）；模型通过 Bash 读取
  当前任务随机环境变量，并带上只存在于文件简报中的角色后缀；三个执行均未
  进入常驻池，用量已收集。测试没有修改原账号设置。
- `phase31-compatibility-race.log`：CodeBuddy、监督及简报的定向 race 检查通过
  （agent 17.194s、daemon 1.300s、execenv 1.097s）；覆盖局部与上级目录、任务
  设置、无法解析配置及人工内容保留。
- 首轮 `phase31-real-compatibility.log` 使用 dontAsk 权限，Bash 被 CLI 拒绝，
  因此失败；后续按普通任务的既有权限运行，原失败记录保留。

发布现场记录位于 `phase31-gong99-live.json` 与 `phase31-environment-live.log`。
GONG-99 的 int 六类任务 Agent `e47aaeed` 实际绑定 Codex 运行时 `b6ea8b7e`，
模型为 `gpt-6.1-sol`；验收 JSON 的 codebuddy 标签与真实绑定不符。该组结果
不能作为 CodeBuddy CLI 的六类业务对照，仍需补验。
GONG-98 的 `gong98-int2/acceptance.json` 也将 codebuddy 项记录为同一 Codex
运行时和 `gpt-6.1-sol`；其中 CodeBuddy 六类结果及 -28.0% 计时不作为真实
CodeBuddy 验收依据。两组公开任务评论的表述与留存证据存在这一范围差异。

任务分支已合入生产主线 `07531af70`，普通角色审批策略沿用主线，不夹带此前
未发布的角色级 never 策略。合并后 `phase31-main-integration-race.log` 通过
（agent 17.572s、daemon 1.589s、execenv 1.157s），涵盖 Codex 监督、CodeBuddy
及 DSH 定向回归；`phase31-main-build.log` CLI 构建通过。此次增量尚未提交 int。

## 第三十二阶段：真实平台业务补验与历史过程修复

固定 int `07531af70` 上新建两个独立 Agent，逐轮核对服务版本、实际 runtime
绑定和模型；六类任务均通过：调查、修复、小功能、澄清、80 行完整输出、知识库
检索。知识库问题仅含标题，随机答案只存在于测试资源；两个运行时均经工具读回
正确答案。`phase32-business-summary.json` 保存任务、进程、二进制指纹和用量；
每轮任务均为 completed，实际用量 provider 分别为 codebuddy / codex。

int 的测试默认策略会在创建事务中覆盖显式运行时。首次创建发现不符后未提交
模型任务；按既有控制入口仅为本次 CodeBuddy Agent 设置 Flash 覆盖，由发布器
应用，环境默认值和其他覆盖项未变。保留 `configure-own-agent.json` 与原测试
Agent、会话及知识库资源，不清理留存样本。

- CodeBuddy 2.177.0，固定指纹 `a246ed81`，实际模型用量为
  `deepseek-v4.1-flash-ioa`。当前账号存在人工 MCP 配置，六轮使用独立进程；
  本次业务结果不作为常驻性能证据。
- Codex `gpt-6.1-sol` 六轮复用 PID 3332241。此次对照仍是当前 int 程序，
  不代表原始基线与新版本六类性能比较已完成。
- CodeBuddy 澄清回复额外请求用户提供路径，逐字比较首轮失败；工具轨迹确认
  未执行任何操作、问题含义准确，单独保留行为复核与原失败判定。

真实浏览器发现“查看过程”加载后为空：服务端消息含 runtime 遥测行，前端
数组 schema 拒绝该类型，整批历史降级为空数组。新增回归在修复前失败，修复后
通过；类型与 schema 接受 runtime，对话过程和审查中的模型输出跳过遥测行，
运行上下文继续独立读取。浏览器对原始 int 响应做受控回放、仅去掉遥测行后，
两个运行时的过程均可展开，刷新回复仍在，无页面错误或 HTTP 错误。该回放证明
因果关系，不作为源码修复已部署的验收。

核心定向 147 项通过；过程与实录测试通过，审查定向 37 项通过；core / views
类型检查通过。聊天组件另有 5 项缺工作区夹具而失败，在固定生产源码
`07531af70` 同样复现，不能记为全量测试通过。第三十阶段及之后增量仍待固定
候选的 int 与生产业务验收。

## 第三十三阶段：CodeBuddy 原生问答的能力声明与答案投递

`phase33-native-question.log` 记录真实 int 问答失败：模型找不到
AskUserQuestion，任务虽正常结束，但没有产生可回答的问题。固定 2.177.0
二进制实现确认，ACP 初始化须声明 `_meta["codebuddy.ai"].question=true`；
适配器此前发送空能力表。

交互运行现声明该能力，无回答器或 never 策略不声明。原生 AskUserQuestion 的
标准权限请求转换为平台问题；回答通过 `_codebuddy.ai/resolveInterruption`
先交给原生 AskService，确认 resolved 后才结束权限请求。拒答不传递选项内容，
原生请求失效或投递失败不会伪装为回答成功。另一路 `_codebuddy.ai/question`
使用原生要求的顶层 outcome / answers 格式。

- `phase33-real-question-decisions.log`：实际 2.177.0、固定指纹 `a246ed81`、
  Flash 模型，回答与拒答两个对照均通过（23.59s）。每例只产生一个问题；模型
  分别准确回复“简短”与 DECLINED。回答器为自动测试，尚非平台浏览器问答验收。
- 定向 race 检查 19.840s；最后的能力声明、答案、拒答与失效回执检查 4.264s
  通过。相关 Go vet 与 CLI 构建通过。
- 原 int 失败、握手修正后的普通审批误识别失败均保留。普通 SDK stream-json
  路径还缺同类初始化能力声明；该路径与新候选的真实浏览器闭环继续补验。

本轮代码及证据已形成可审阅增量，尚未登记新 int 候选。发布流程所需的原任务
用户评论来源已向用户询问，不能把已发布固定提交的旧登记当作本轮已获平台批准。
