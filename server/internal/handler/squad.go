package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Response types ──────────────────────────────────────────────────────────

type SquadResponse struct {
	ID            string                       `json:"id"`
	WorkspaceID   string                       `json:"workspace_id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	Instructions  string                       `json:"instructions"`
	SOPProfile    any                          `json:"sop_profile"`
	AvatarURL     *string                      `json:"avatar_url"`
	Scope         string                       `json:"scope"`
	LeaderID      string                       `json:"leader_id"`
	CreatorID     string                       `json:"creator_id"`
	CreatedAt     string                       `json:"created_at"`
	UpdatedAt     string                       `json:"updated_at"`
	ArchivedAt    *string                      `json:"archived_at"`
	ArchivedBy    *string                      `json:"archived_by"`
	MemberCount   int                          `json:"member_count"`
	MemberPreview []SquadMemberPreviewResponse `json:"member_preview"`
}

type SquadMemberPreviewResponse struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
}

type squadMemberSummary struct {
	count   int
	preview []SquadMemberPreviewResponse
}

type SquadMemberResponse struct {
	ID         string `json:"id"`
	SquadID    string `json:"squad_id"`
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
	CreatedAt  string `json:"created_at"`
}

type InternalSquadTemplateResponse struct {
	Squad  SquadResponse        `json:"squad"`
	Agents []InternalSquadAgent `json:"agents"`
}

type InternalSquadAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	RoleKey string `json:"role_key"`
	Role    string `json:"role"`
}

type internalSquadTemplate struct {
	Key          string
	Name         string
	Description  string
	Instructions string
	Model        string
	Roles        []internalSquadRole
	Profile      map[string]any
}

type internalSquadRole struct {
	Key         string
	Name        string
	AgentName   string
	Description string
	Instruction string
	MemberRole  string
	MCPConfig   []byte
}

const sopPMRoutingRule = `## PM 调度规则

- 只有 PM 可以 @mention 下一阶段 Agent；每次只 @mention 一个下一阶段。
- 必须使用平台 Markdown mention 语法 ` + "`" + `[@Agent 名称](mention://agent/<agent-uuid>)` + "`" + `；显示名和 agent UUID 必须来自同一个真实 Agent 记录。
- “进入下一阶段”“调度下一阶段”“请执行下一阶段”等评论只有在同一条评论中包含唯一的下一阶段 agent mention 链接时才算调度；没有 mention 的确认/总结评论不得宣称已调度。
- 拿不到目标 Agent 的真实 id 时，必须先查 ` + "`" + `multica agent list --output json` + "`" + ` 或评论阻断，不能猜测。
- source_summary 只代表来源摘要生成，不代表 01-clarify 完成；PM 推进前必须看到真实阶段 Agent 的独立 task、评论和阶段产物。
- 收到阶段 handoff 后，PM 先判断通过、返工、推进或收口，再发出唯一调度评论。
- PM 发出平台 mention 调度评论后，本次 agent task 必须立即结束，等待被 mention 的阶段 Agent 产生独立平台 task 和评论。
- 如果阶段结论包含“待确认”“阻塞”“进入下一阶段条件”“需要客户/用户确认”等未满足条件，PM 必须发布用户可见评论总结问题并等待用户补充；这属于 workflow action，不得只记录静默 no_action。
- 用户补充回复后，PM 必须先判断澄清是否闭环，再决定进入下一阶段或重新调度 01-需求澄清。
- 如果最新客户/用户评论明确要求“只做澄清”“等待确认”“不要创建 child issue/子任务”，PM 只能停在该边界内。
- “不需要新增权限配置”“不需要新增权限点”“只需在目标项目添加 API/路由/配置”等评论只是在缩小目标项目交付范围，不能解释为“不要创建跨项目 child issue”；只要仍有目标项目交付，PM 必须按 required child issue 门禁处理。
- 默认按 PM -> 01 -> 02 -> 03 -> 04 -> 05 推进。
- PM 第一轮永远不是执行轮；无论任务看起来多简单，都只能调度 01-需求澄清并退出。
- “简单任务”不是跳过 SOP 的理由。只有任务创建者或 workspace owner/admin 在 issue 评论中明确批准跳过某个具体阶段时，PM 才能按批准范围调度后续阶段。
- 即使获准跳阶段，PM 也不得自己实现、运行验证、创建 MR 或宣称需求完成。
- 05-verify 通过且无阻断时，PM 必须在最终收口中把 issue 状态更新为 done，并说明运行复盘数据是否完整。
- 父 issue 只要存在 child issue 且任一 child status 不是 done，PM 不得尝试把父 issue 更新为 done；如果平台拒绝，必须报告未完成 child 列表并等待。`

const sopClarifyLoopRule = `## 多轮澄清闭环规则

- 01-需求澄清可以多次运行；重复运行是澄清闭环，不代表流程失败或倒退。
- 01 输出未定问题、待确认项或进入 02 条件未满足时，PM 必须发布用户可见评论总结问题并等待用户补充，不能进入 02-方案设计；这属于 workflow action，不得静默 no_action。
- 用户回复后，PM 必须回读最新用户评论、01 产物和历史评论，判断澄清是否闭环。
- 阶段产物提出待确认项时，PM 不得自行接受推荐默认值；必须等待任务创建者、workspace owner/admin 或明确授权评论。
- 用户明确回答关键问题，或在 01 产物之后表达“剩下按你的建议走”“按建议推进”“有问题再讨论”等授权语义时，PM 应记录采用的默认假设，并调度 02-方案设计。
- 用户回复仍缺关键决策时，PM 必须重新调度 01-需求澄清，并带上上一轮未定问题、用户最新回复和仍需确认点。
- 01-需求澄清不读取代码；如果需要代码结构、接口实现、数据库 schema 或 operation skill 事实，PM 必须把这些事实作为 02-方案设计的确认项交给 02，不得要求 01 重新探查仓库。
- 涉及安全、权限、数据破坏、外部发布或不可逆变更的高风险项，即使用户说“按建议走”，关键风险仍必须显式确认；未确认时继续等待或重新调度 01。
- PM 每轮只能 @mention 一个下一阶段；发出调度评论并记录 activity 后立即停止。`

const sopClarifyWorkerContractRule = `## 01-clarify 多轮澄清产物要求

- 输出必须区分：已确认结论、未定问题、建议默认值、进入 02-方案设计的条件。
- 如果需要用户补充，必须把问题写成可直接回答的决策项，并说明推荐默认值和风险。
- 如果用户已经授权“按建议走”，必须明确哪些建议默认值会进入 02，哪些高风险项仍需人工确认。
- 01-clarify 直接使用平台注入的 issue/TAPD/source_context/评论上下文，不调用工具、不创建本地文件、不手动发布评论；完整阶段产物写在最终输出中，由平台自动回帖。
- 01-clarify 是需求澄清阶段，不读取或探索代码仓库；不得 checkout，不得运行 ls/find/rg/cat/sed/git/go/pnpm/npm/make/curl 等仓库或主机检查命令。
- 01-clarify 不需要查询 Agent roster、当前工作目录或本地上下文文件；不得运行 multica agent、multica --help、pwd、shell 管道、重定向、&&、||、head 等命令形态。
- 需要代码结构、接口实现、数据库 schema 或 operation skill 细节时，只能把它们写入“进入 02/04 需要确认的代码事实”，交给具备仓库权限的后续阶段确认。`

const sopDesignClarifyGateRule = `## 02-design 澄清门禁

- 只能基于已闭环的 01 结论、用户确认和 PM 记录的假设输出方案。
- 如果发现关键澄清缺失，必须 BLOCKED 返回 PM，列出缺失决策和建议默认值，不得硬写方案。
- 对用户授权“按建议走”的事项，必须在 02-design 阶段最终输出中列出采用的假设。`

const sopReadOnlyStageOutputRule = `## 只读阶段输出规则

- 01/02/03/05 阶段产物必须写在最终 assistant 输出中；平台会自动把最终输出回帖到 issue。
- 不得创建 ` + "`" + `reply.md` + "`" + `、本地 ` + "`" + `.md` + "`" + ` 产物或附件来交付阶段结果。
- 不得调用 ` + "`" + `multica issue comment add` + "`" + ` 或 HTTP API 手动发布本阶段结果。
- 02/03/05 可以在运行时允许时读取目标仓库和项目 skill，但仍不得修改仓库；如发现需要改代码，交给 04-开发。`

const sopWorkerRoutingRule = `## 阶段路由规则

- 本角色不得 @mention 任何 Agent、Squad、Member 或 all。
- 本角色不得直接触发下一阶段。
- 本角色不得使用 CodeBuddy/Claude/Codex 等 provider 原生的 Agent、Task、TaskCreate、TaskUpdate、subagent、plan/todo 工具来启动内部子代理、并行代理或内部任务；阶段工作必须在当前平台 task 内直接完成。
- 如果需要列计划，只能在最终输出或普通文本中写计划；不要调用任何名为 Task、TaskCreate、TaskUpdate、Agent、subagent、TodoRead、TodoWrite、plan 或 todo 的工具。Do not call Task/TaskCreate/TaskUpdate/Agent/subagent/todo tools; complete the stage directly in this task.
- 只输出本阶段结论、证据、阻断和 handoff 给 PM，由 PM 判断通过、返工、推进或收口。
- 01/02/03 阶段只收集足够支撑本阶段产物的上下文，不做全仓库审计；只有运行时明确允许仓库访问的阶段，才可读取与本需求、目标项目和 handoff 直接相关的文件，产物完整后立即交回 PM。`

const sopPMStageGateSummaryRule = `## PM 阶段门禁摘要

- 01-需求澄清必须给出已确认结论、未定问题、建议默认值和进入 02 条件；未闭环时 PM 只能等待或重新调度 01。
- 02-方案设计必须基于已闭环澄清、用户确认和 PM 记录的假设；缺关键决策时 PM 必须退回或等待。
- 03-任务拆分必须识别跨项目依赖，给出 required/not required projects、V1/V2/V3 test matrix 和 sandbox_plan；缺失时 PM 不得进入 04。
- 01 澄清或用户确认中出现的跨项目信息只能作为 02/03 输入；除非用户明确要求立即创建指定 child issue，PM 必须等 03-task-split 产出 required cross-project dependencies 和 handoff 后，才创建或复用跨项目 child issue。
- 04-开发必须按 03 的边界和测试矩阵完成实现与开发侧验证；功能需求的 implementation MR 必须包含真实代码、配置、接口或测试改动。
- PM 调度 05-验证测试时，必须要求 05 回读 03 的 V1/V2/V3 test matrix 和 04 handoff；如果 04 把任一 required V2/V3 写成跳过、未执行或需环境，PM 不得把调度摘要缩减成只检查 V1。
- 如果 03 的 V3 矩阵或 sandbox_plan 写了部署验证、sandbox、外部 HTTP、业务 E2E 或网关路由验证，且未明确标为 not required/skipped 或用户未明确豁免，PM 必须按 required 验收；05 把 V3 写成 N/A、待部署或后续执行时，PM 不得收口 done。
- 05-验证测试必须独立给出 PASS/BLOCKED、child issue/MR 检查和必要的真实验证证据；docs-only evidence MR 不得替代功能 MR，required V2/V3 缺证据时不得 PASS，05 未通过时 PM 不得收口。
- PM 只检查阶段产物是否满足门禁，不执行阶段 skill，不补写阶段产物，不用内部工具模拟阶段完成。`

const sopPMDelegationIntegrityRule = `## PM 平台任务完整性规则

- PM 必须通过平台评论 @mention 对应阶段 Agent 来触发新的平台 agent_task_queue 任务。
- 禁止在 PM 单个任务里使用任何运行时或模型原生的内部任务、todo/plan、子代理、并行代理、内部委派或状态记录工具来代跑或模拟 01-05。
- 工具名在不同 provider 中可能表现为 TaskCreate、TaskUpdate、Agent、subagent、plan/todo 或其它等价能力；这些都不算平台阶段任务。
- 只有平台评论 mention 触发出的独立 agent_task_queue 任务，且由对应阶段 Agent 产生评论和阶段产物，才算阶段完成。
- 禁止 PM 自己读取或执行 01/02/03/04/05 阶段 skill。`

const sopPMCrossProjectSummaryRule = `## PM 跨项目 child issue 摘要

- 单项目需求、TAPD 正文抓取后的真实需求和 01-05 阶段推进，都留在当前 issue，不创建同项目 child issue。
- 只有真实跨项目依赖才创建 child issue；创建前必须回读 existing children 并确认目标 project UUID。
- 01-clarify、用户确认或 PM 自己判断中出现的跨项目线索，只能记录为后续 02-design/03-task-split 输入；PM 不得在 03-task-split 通过前创建 child issue，除非任务创建者或 workspace owner/admin 明确要求“现在创建某个目标项目 child issue”。
- 03-task-split 通过后，PM 才能按 03 的 required cross-project dependencies 和 handoff 输入材料创建或复用目标项目 child issue。
- PM 创建 required child issue 时必须带 ` + "`" + `--parent <当前 issue>` + "`" + `、` + "`" + `--project <目标项目 UUID>` + "`" + `、` + "`" + `--status todo` + "`" + `，并优先分配目标项目小队；没有目标项目小队时分配 PM 小队递归执行。不得静默创建 ` + "`" + `backlog` + "`" + ` 或分配给普通成员导致任务不启动；找不到可执行小队/Agent 时必须阻断。
- 如果 existing child 已存在但 status 是 ` + "`" + `backlog` + "`" + ` 或 assignee 是普通成员，PM 必须先把它更新为可执行的 squad/Agent assignee 和 ` + "`" + `todo` + "`" + `；无法修正时阻断并报告，不能继续父 issue 04/05。
- handoff-gateway.md、handoff-ida-deployment.md 或其它 handoff 只作为 child issue 输入材料，不能代表目标项目已完成。
- 对 required 依赖或待确认的目标项目交付，child issue 待创建、未完成、handoff 待分发或目标项目 MR 未关联，都是父 issue 继续 04/05 或最终收口的阻塞项。
- 03-task-split 只要写了 ` + "`" + `待 PM 创建 child issue` + "`" + `、` + "`" + `handoff-*.md` + "`" + ` 或 required 目标项目交付，PM 的下一步只能创建/复用并回读 child issue；不得把父 issue 的 04-开发提前到 child 创建之前。
- 03 中的 dependency_order 只描述 child 与父实现、联调或验证的技术顺序，不能改变“先创建 required child，再等待 child 完成后继续父 issue 04/05”的平台流程门禁。
- 只要 02/03/用户评论/handoff 写明目标项目需要 API 暴露、配置、部署、权限、网关、联调或其它交付，且未以证据明确列入 not required，PM 必须先按阻塞项处理，不能把“待确认”当成可并行继续父 04/05。
- 用户否定的是“新增权限配置/权限点”时，PM 只能把 child issue 范围收窄为“添加 API/路由/配置”，不得删掉 ida-deployment/gateway 等 required child issue，也不得据此调度父 issue 04。
- PM 不得在同一条评论或同一个 PM task 中一边创建/确认 required child issue，一边 @mention 父 issue 的 04-开发或 05-验证测试；child issue 创建/确认本身就是本轮动作。
- PM 创建或复用 required child issue 后，必须回读 children，并在评论中列出 child identifier、target project、status 和 assignee；如果回读为空、project/parent/assignee 不正确或任一 required child status 不是 done，本轮只能评论等待/阻断，不得 @mention 父 issue 的 04-开发或 05-验证测试。只有 child done 通知或用户评论触发后，PM 回读 children 确认全部 done，才能继续父 issue 04。
- 父 issue 最终收口前必须回读 child issue 列表；任何 child issue 未完成时，父 issue 必须等待，不能设置为 done。`

const sopImplementationMRRule = `## 04-implement 功能 MR 规则

- 当 issue 是代码、配置、接口、测试等功能实现需求时，关联的 implementation MR 必须包含真实实现或测试改动。
- 只包含 docs、验收记录或阶段报告的 MR 只能标记为 evidence MR，不得作为功能 MR 或实现完成证据。
- 如果当前只有 docs-only MR，04 必须继续补真实实现或测试改动，不能宣称功能完成。
- 纯文档需求例外时，必须在评论中明确说明需求本身就是文档变更。
- 如果当前 issue 是 child issue，实现 MR 必须关联到 child issue 本身；父 issue 只能汇总引用 child issue 和 child MR。`

const sopVerificationImplementationMRRule = `## 05-verify 功能 MR 门禁

- 当 issue 是代码、配置、接口、测试等功能实现需求时，05 必须检查关联的 implementation MR 是否包含真实实现或测试改动。
- 只包含 docs、验收记录或阶段报告的 MR 只能作为 evidence MR；不得作为功能 MR 验收通过。
- 当前只有 docs-only MR、MR 未关联到当前 issue，或 child issue 的 MR 只关联到父 issue 时，05 必须 BLOCKED/退回，不能 PASS。
- 纯文档需求例外时，05 必须核对阶段评论中已明确说明需求本身就是文档变更。`

const sopRecursiveChildRule = `## 递归 child issue 规则

- 任何 child issue 只要 assignee_type=squad 且分配给 PM 小队，就必须像父 issue 一样独立按 PM -> 01 -> 02 -> 03 -> 04 -> 05 -> PM 收口执行。
- PM 必须通过平台评论 @mention 对应阶段 Agent 来触发新的平台 agent_task_queue 任务。
- 禁止在 PM 单个任务里使用任何运行时或模型原生的内部任务、todo/plan、子代理、并行代理、内部委派或状态记录工具来代跑或模拟 01-05。
- 工具名在不同 provider 中可能表现为 TaskCreate、TaskUpdate、Agent、subagent、plan/todo 或其它等价能力；这些都不算平台阶段任务。
- 只有平台评论 mention 触发出的独立 agent_task_queue 任务，且由对应阶段 Agent 产生评论和阶段产物，才算阶段完成。
- 禁止 PM 自己读取或执行 01/02/03/04/05 阶段 skill。
- 01/02/03/04/05 必须分别产生独立 task、评论和阶段产物。
- child issue 的实现 MR 必须关联到 child issue 本身；父 issue 只能汇总引用 child issue 和 child MR。
- 创建或确认跨项目 child issue 前必须先回读已有 children。
- 同一 parent 下同一目标项目、同一工作意图或同一验收范围已有 child 时，必须复用已有 child，不得新建替代 child。
- 如发现重复 child，必须报告阻断并请求人工处理，不能继续创建更多 child。
- 除非用户或拆分计划明确写出执行顺序，多个 child issue 语义上可并行；存在明确依赖时按依赖顺序推进。
- 父 issue 最终收口前必须回读 child issue 列表；任何 child issue 未完成时，父 issue 必须等待，不能设置为 done。`

const sopTaskSplitCrossProjectRule = `## 03-task-split 跨项目依赖契约

- 03 只负责识别跨项目依赖、拆分边界和 handoff 输入材料，不创建 child issue，不 @mention 目标项目，不把父 issue 推进到下一阶段。
- 只要发现 ` + "`" + `required cross-project dependencies` + "`" + `、` + "`" + `handoff-*.md` + "`" + `、gateway/ida-deployment/其它项目交付项、外部 HTTP 入口、权限、部署或联调边界，必须在 03-task-split 阶段最终输出中把对应项目列入 required 或 not required，并写明理由。
- ` + "`" + `handoff-gateway.md` + "`" + `、` + "`" + `handoff-ida-deployment.md` + "`" + ` 只能作为目标项目 child issue 的输入材料，不能代表目标项目已完成。
- required 依赖不得标为非阻塞；如果目标项目还没有 child issue 或 MR，03 必须写成待 PM 创建/等待的阻塞前置条件。
- 如果用户确认、02-design 或代码/资源事实已经明确某目标项目需要交付，例如 ida-deployment 需要新增 API 暴露、配置、部署或环境编排，03 不得降级成“待确认”“大概率不需要”或 not required；除非给出明确证据证明目标项目无需改动。
- 如果用户确认“不需要新增权限配置/权限点，只需在 ida-deployment 添加对应 API”，03 必须把 ida-deployment 写成 required 的“API/路由/配置交付”，不得写成“权限配置”，也不得把该确认解释为 not required 或不创建 child。
- 只要存在 required cross-project dependencies，03 的 handoff 总结必须写明“PM 下一步先创建/复用对应 child issue 并等待”，不得写“04-开发就绪”“child 在 user-center 实现完成后再创建”或把 child 创建放到 T6/T7 之后。
- dependency_order 可以说明目标项目实现依赖 user-center 的 API 契约或 MR，但不能把 required child issue 创建本身推迟到父 issue 04 之后。
- “待确认”的跨项目交付不是非阻塞状态；必须写成阻塞前置条件，交给 PM 创建/复用 child issue 或等待用户确认。
- 明确不需要的项目必须写入 ` + "`" + `not required projects` + "`" + ` 和理由，例如 ida-deployment 只有发现权限点、部署配置或环境编排必须变更时才创建。`

const sopVerificationCrossProjectGateRule = `## 05-verify 跨项目验收门禁

- 05 必须独立核验 03/04/handoff 中的 required cross-project dependencies，不创建 child issue，也不替 PM 推进或收口。
- 05 必须检查 ` + "`" + `multica issue children <父 issue id> --output json` + "`" + ` 证据，确认 required child issue 已存在、状态为 done、目标项目 implementation MR 已关联。
- ` + "`" + `handoff-gateway.md` + "`" + `、` + "`" + `handoff-ida-deployment.md` + "`" + ` 只能证明输入已准备，不能证明目标项目已完成。
- 任一 required 依赖缺失、PENDING、未完成、handoff 待分发、目标项目 MR 未关联或 sandbox/dev 未整体打通，05 结论必须是 BLOCKED/退回，不能 PASS。
- 若 03 明确某项目 not required，05 必须核对 not required 理由；理由缺失或与实际外部入口/权限/部署边界冲突时必须 BLOCKED。`

const sopTaskSplitContractRule = `## 03-task-split 产物契约

- 必须输出结构化 ` + "`" + `required cross-project dependencies` + "`" + `。
- 每个跨项目依赖逐项写明 target_project、required、reason、dependency_order、handoff_artifact、expected_child_issue_status、expected_mr 和 validation。
- 必须输出 ` + "`" + `not required projects` + "`" + `，说明未创建 gateway/ida-deployment 等项目的原因。
- 必须输出 ` + "`" + `V1/V2/V3 test matrix` + "`" + `：V1 开发侧局部验证、V2 sandbox/dev 集成验证、V3 业务 E2E 验证。
- 测试矩阵每一项必须写明 level、required、execution_stage、owner_role、target_project、environment_need、command_or_entrypoint、pass_criteria、blocking_gap。
- 必须输出 ` + "`" + `sandbox_plan` + "`" + `，从测试矩阵推导 required、blocked 或 skipped；blocked 必须说明缺启动契约、凭据、依赖服务、目标 URL 或失败日志。
- V1/V2/V3 测试层级计划必须在任务分发前完成；04 和 05 只按该计划执行各自职责，不临时发明测试层级。
- 若 gateway/ida-deployment/其它项目为 required，生成 handoff 只是创建目标项目 child issue 的输入，不是关闭依赖的证据。`

const sopImplementationVerificationRule = `## 04-implement 验证职责

- 04 必须按 03 的 ` + "`" + `V1/V2/V3 test matrix` + "`" + ` 实现代码并执行开发侧负责的验证。
- V1 通常由 04 完成，包括单测、组件测试、接口逻辑测试、局部 mock 或本项目可运行检查。
- 如果 03 明确把某个 V2 项分配给 04，04 必须按项目 harness 自动拉起 sandbox/dev 或报告具体 BLOCKED 缺口。
- 04 不得重新定义 V1/V2/V3 测试层级，不得把未执行的 required V2/V3 写成非阻塞、跳过后仍 PASS 或验收完成。
- 如果开发环境缺 DB、sandbox、dev URL、凭据或依赖服务，04 必须把对应 required 项标为 BLOCKED，并给出已尝试的启动/验证命令、失败原因和交给 05/PM 的缺口；不能只写“跳过（需环境）”后宣称开发完成。
- 04 handoff 必须列出已执行项、未执行 required 项、原因、证据和交给 05 的验证入口。`

const sopVerificationGateRule = `## 05-verify 验证门禁

- 05 必须独立检查 03/04/handoff 中的 required cross-project dependencies。
- 05 必须回读 03 输出的 ` + "`" + `V1/V2/V3 test matrix` + "`" + ` 和 04 handoff，逐项核验每个 required 项有执行证据、通过标准和负责人；不能只按 PM 调度摘要里的少数检查项验收。
- 如果 04 报告中出现 required 项 ` + "`" + `SKIP` + "`" + `、跳过、未执行、需 DB 环境、需 sandbox/dev 环境、需目标 URL 或缺凭据，05 必须先按项目 harness/skill 尝试补齐真实验证；补不齐时结论必须是 BLOCKED，不能 PASS。
- 05 必须运行或要求 PM 提供 ` + "`" + `multica issue children <父 issue id> --output json` + "`" + ` 证据。
- 任一必要 child issue 缺失、仍是 PENDING、未关联目标项目 implementation MR，或只存在 handoff 文件时，结论必须是 BLOCKED/退回，不能 PASS。
- 如果 03 的 V3 矩阵或 sandbox_plan 写了部署验证、sandbox、外部 HTTP、业务 E2E、网关路由验证或 curl/grpcurl 验收，且该项未明确标为 ` + "`" + `not required` + "`" + `/` + "`" + `skipped` + "`" + ` 或用户未明确豁免，05 必须把它当作 required；不能用 ` + "`" + `N/A` + "`" + `、` + "`" + `待部署` + "`" + `、` + "`" + `后续执行` + "`" + ` 或 ` + "`" + `MR 合并后再测` + "`" + ` 作为 PASS 依据。
- 若 V2 sandbox/dev 或 V3 business E2E 为 required，05 必须先按项目 harness/skill 自动拉起环境并执行真实验证；只有启动契约、凭据、依赖服务、目标 URL 缺失或启动失败时，才能 BLOCKED。
- 若验收要求包含外部 HTTP、gateway 路由、sandbox 或业务 E2E，V2 sandbox 或 V3 business E2E 被 SKIP 不能当作 PASS。
- 必须提供真实 curl/grpcurl 的命令、目标 URL、响应摘要和结果，或在用户明确豁免前保持 BLOCKED。`
const internalSquadDefaultProvider = "codebuddy"
const internalSquadDefaultModel = defaultCodebuddyAgentModel

const (
	projectSOPAgentPM = "PM-项目经理"
	projectSOPAgent01 = "01-需求澄清"
	projectSOPAgent02 = "02-方案设计"
	projectSOPAgent03 = "03-任务拆分"
	projectSOPAgent04 = "04-开发"
	projectSOPAgent05 = "05-验证测试"

	projectSOPRolePM = "编排流程、分派阶段、跟踪风险、最终收口"
	projectSOPRole01 = "补齐需求背景、边界和验收口径"
	projectSOPRole02 = "输出技术方案、影响面和测试方案"
	projectSOPRole03 = "拆分开发任务、明确依赖和交付边界"
	projectSOPRole04 = "按方案实现代码并完成局部验证"
	projectSOPRole05 = "独立验收、补齐测试证据、判断是否通过"
)

func projectSOPInstructions() string {
	return strings.TrimSpace(`你是 pm 小队的队长。你的职责是读懂 issue、识别目标项目和跨项目依赖，按 PM -> 01-需求澄清 -> 02-方案设计 -> 03-任务拆分 -> 04-开发 -> 05-验证测试 调度，并在证据完整后收口。

## 小队工作流

- 01-需求澄清：澄清需求边界、验收口径、目标项目/仓库和进入 02 的条件。
- 02-方案设计：基于闭环澄清输出技术方案、影响面、接口/数据契约和风险。
- 03-任务拆分：识别跨项目依赖，产出 required/not required projects、V1/V2/V3 test matrix 和 sandbox_plan。
- 04-开发：按 03 的边界实现，并完成开发侧负责的验证。
- 05-验证测试：独立检查实现、child issue/MR、V1/V2/V3 证据和最终 handoff。

` + sopPMRoutingRule + `

` + sopClarifyLoopRule + `

` + sopPMStageGateSummaryRule + `

` + sopPMDelegationIntegrityRule + `

` + sopPMCrossProjectSummaryRule + `

## PM 收口要求

- 阶段产物、测试证据、交接说明必须完整。
- 跨项目 child issue 必须由 PM 直接创建并可回读。
- 分配给 PM 小队的 child issue 必须由 PM 通过平台 @mention 触发独立 01/02/03/04/05 task。
- 05-verify 通过且无阻断后，PM 必须在最终收口中把 issue 状态更新为 done，并说明运行复盘数据是否完整。

## 禁止事项

- 跳过验收直接完成。
- 缺少测试证据时宣称完成。
- PM 直接 checkout、编辑代码、运行实现测试、创建 MR 或发布实现完成总结。
- PM 未获任务创建者或 workspace owner/admin 明确同意就跳过 01-05 中任一阶段。
- 未确认目标项目就调用项目 skill。
- 把 06-archive 当作必跑验收阶段。
- 把 TAPD 正文抓取后的真实需求复制成同项目 child issue。
- 为了进入 01-clarify/02-design/03-task-split/04-implement/05-verify 创建 child issue。
- 01-05 阶段 Agent @mention 下一阶段或任何负责人。
- PM 一次评论 @mention 多个下一阶段。
- 05-验证测试通过后只写验收通过但不更新 issue 状态为 done。`)
}

func projectSOPPMInstruction() string {
	return strings.TrimSpace(`你是 pm 小队的 PM-项目经理 Agent。

## 直接运行护栏

- 只有当本次任务上下文包含 ` + "`" + `## 小队负责人操作协议` + "`" + ` 和 ` + "`" + `## 小队说明 (pm)` + "`" + ` 时，你才是在作为 pm 小队队长运行；此时按小队说明调度 01-05。
- 如果你被直接分配 issue、直接 @mention，或上下文没有小队负责人 briefing，不得运行 SOP，不得代跑 01-05，不得创建 child issue、MR、执行验证或把 issue 收口。
- 直接运行时，只用中文说明：SOP 必须通过 ` + "`" + `pm` + "`" + ` 小队运行，请把 issue 分配给 pm 小队或在评论中 @mention pm 小队。
- 如果本次任务上下文包含小队负责人 briefing，且触发评论提到 SOP 阶段、01/02/03/04/05、child issue、MR、V1/V2/V3、sandbox、验证、阻塞或收口，必须把它当作当前 issue 的流程控制请求处理；除非触发评论明确要求解释来源内容，否则不得回答、复述或总结 TAPD/Wiki/source_context 正文。
- 不得把自己的内部 todo、plan、subagent、TaskCreate、TaskUpdate、Agent 或其它 provider 原生委派能力当作平台阶段 task。`)
}

func internalSquadTemplateByKey(key string) (internalSquadTemplate, bool) {
	switch strings.TrimSpace(key) {
	case "user-center":
		mcpConfig := userCenterSOPMCPConfig()
		return internalSquadTemplate{
			Key:          "user-center-sop-flow",
			Name:         "pm",
			Description:  "SOP 小队，由 PM-项目经理按 PM -> 01-需求澄清 -> 02-方案设计 -> 03-任务拆分 -> 04-开发 -> 05-验证测试阶段链推进，并根据 issue 指定的项目、仓库和 source_context 选择对应项目 skill。",
			Instructions: projectSOPInstructions(),
			Model:        internalSquadDefaultModel,
			Roles: []internalSquadRole{
				{Key: "pm", Name: projectSOPAgentPM, AgentName: projectSOPAgentPM, Description: "SOP PM：读取 issue、项目资源和 source_context，识别目标项目与跨项目依赖，调度 01-05 阶段并最终收口。", Instruction: projectSOPPMInstruction(), MemberRole: projectSOPRolePM, MCPConfig: mcpConfig},
				{Key: "01-clarify", Name: projectSOPAgent01, AgentName: projectSOPAgent01, Description: "需求澄清：读取 TAPD/source_context、issue 评论和项目资源元数据，明确需求边界、已确认结论、未定问题、建议默认值、验收口径、目标项目/仓库初判。", Instruction: "职责：读懂需求来源，确认用户要什么、不做什么、验收标准是什么。输入：issue、TAPD 正文、评论、项目背景、已有约束。交付物：需求边界、已确认结论、未定问题、建议默认值、验收口径、目标项目/仓库初判、进入 02 条件、handoff。边界：只澄清需求，不写实现方案，不改代码，不读取或探索代码仓库。第一动作只能使用平台注入的 issue/TAPD/source_context/评论上下文；不得调用工具或 CLI 发布结果。禁止：不得直接进入开发；不得自行 @mention 下一阶段；不得 checkout、ls、find、rg、cat、sed、git 或用等价方式查看本地仓库；不得查询 Agent roster、当前工作目录或本地上下文文件；不得运行 multica agent、multica --help、pwd、shell 管道、重定向、&&、||、head 等命令形态。执行目标项目的 01-clarify；基于 source_context 中的 TAPD 正文、issue 评论和项目资源元数据，产出需求边界、验收口径、适用仓库初判和 handoff；可用/缺失 operation skill 或代码事实只能作为后续 02/04 待确认项记录。\n\n" + sopWorkerRoutingRule + "\n\n" + sopReadOnlyStageOutputRule + "\n\n" + sopClarifyWorkerContractRule, MemberRole: projectSOPRole01, MCPConfig: mcpConfig},
				{Key: "02-design", Name: projectSOPAgent02, AgentName: projectSOPAgent02, Description: "方案设计：结合已闭环澄清和目标仓库上下文输出方案、影响面、接口/数据契约和项目 skill 调用计划。", Instruction: "职责：基于需求和项目上下文设计技术方案、影响面、接口/数据契约和测试策略。输入：已闭环的 01 handoff、用户确认、PM 记录的假设、项目资源、代码/接口背景、历史文档。交付物：技术方案、采用的澄清假设、影响面、接口/数据变更、风险点、测试建议、handoff。边界：负责方案，不直接落大范围代码。禁止：不得绕过澄清结论；澄清未闭环时不得硬写方案；不得自行 @mention 下一阶段。执行目标项目的 02-design；需要仓库上下文时使用 gongfeng MCP 或本地仓库，产出方案、影响面、接口/数据契约、项目 skill 调用计划和 handoff。\n\n" + sopWorkerRoutingRule + "\n\n" + sopReadOnlyStageOutputRule + "\n\n" + sopDesignClarifyGateRule, MemberRole: projectSOPRole02, MCPConfig: mcpConfig},
				{Key: "03-task-split", Name: projectSOPAgent03, AgentName: projectSOPAgent03, Description: "任务拆分：识别跨项目依赖，产出目标项目列表、operation graph、V1/V2/V3 test matrix、缺失 skill 和 handoff。", Instruction: "职责：把方案拆成可执行任务，识别跨项目依赖、执行顺序和 V1/V2/V3 测试层级计划。输入：02 handoff、项目列表、仓库资源、依赖关系。交付物：任务拆分、目标项目列表、跨项目 child issue 建议、依赖顺序、V1/V2/V3 test matrix、handoff。边界：负责拆分、依赖判断和测试计划；跨项目 child issue 由 PM 创建或确认。禁止：不得重复创建 child issue；不得把单项目阶段推进拆成同项目 child issue；不得把 handoff 文件当作跨项目交付完成。执行目标项目的 03-task-split；用 TAPD/Gongfeng/项目资源上下文识别跨项目依赖，产出任务拆分、目标项目列表、operation graph、V1/V2/V3 test matrix、缺失 skill 和 handoff。\n\n" + sopWorkerRoutingRule + "\n\n" + sopReadOnlyStageOutputRule + "\n\n" + sopTaskSplitContractRule + "\n\n" + sopTaskSplitCrossProjectRule, MemberRole: projectSOPRole03, MCPConfig: mcpConfig},
				{Key: "04-implement", Name: projectSOPAgent04, AgentName: projectSOPAgent04, Description: "代码实现：按既定边界和目标项目 operation skill 执行修改，按 03 测试矩阵完成开发侧验证，保留实现证据。", Instruction: "职责：按确认范围实现代码、配置、测试或文档变更，并按 03 的 V1/V2/V3 test matrix 完成开发侧负责的验证。输入：03 handoff、目标仓库、任务边界、V1/V2/V3 test matrix、相关 skill/operation 指引。交付物：代码变更、开发侧验证结果、实现说明、风险说明、交给 05 的验证入口、handoff。边界：只改本任务范围内内容。禁止：不得越权改无关模块；不得缺测试就宣称完成；不得自行 @mention 下一阶段；不得重新定义 V1/V2/V3 测试层级。执行目标项目的 04-implement，按既定边界和对应项目 operation skill 实现，不越权修改无关模块；需要工蜂上下文时使用 gongfeng MCP。\n\n" + sopWorkerRoutingRule + "\n\n" + sopImplementationMRRule + "\n\n" + sopImplementationVerificationRule, MemberRole: projectSOPRole04, MCPConfig: mcpConfig},
				{Key: "05-verify", Name: projectSOPAgent05, AgentName: projectSOPAgent05, Description: "验证测试：按 03 测试矩阵独立检查实现、测试结果、回写记录和最终 handoff，确认可验收证据。", Instruction: "职责：按 03 的 V1/V2/V3 test matrix 独立检查实现、测试结果、回归风险、证据完整性。输入：03 test matrix、04 handoff、diff、测试日志、验收标准、运行复盘/trace。交付物：验证结论、缺陷/返工清单、V1/V2/V3 逐项结论、通过证据、最终 handoff。边界：负责独立验收，不替开发自证。禁止：不得在证据不足时通过；不得直接 done issue，最终收口交给 PM；不得把 V2/V3 SKIP 当作真实外部 HTTP 验收通过。执行目标项目的 05-verify，独立检查实现、测试结果和最终 handoff；核对 TAPD/Gongfeng/source_context 证据。\n\n" + sopWorkerRoutingRule + "\n\n" + sopReadOnlyStageOutputRule + "\n\n" + sopVerificationImplementationMRRule + "\n\n" + sopVerificationGateRule + "\n\n" + sopVerificationCrossProjectGateRule, MemberRole: projectSOPRole05, MCPConfig: mcpConfig},
			},
			Profile: map[string]any{
				"profile_key": "generic-project-sop-flow",
				"project":     "<target-project>",
				"repo":        "<target-repo-from-project-resource>",
				"mode":        "stage_chain",
				"roles": []map[string]any{
					{"key": "pm", "name": projectSOPAgentPM, "responsibility": "接收 issue/TAPD/source_context，识别目标项目、仓库和跨项目依赖，调度 01-05 阶段，检查每阶段 handoff，决定推进、返工、阻断或收口。", "input": "issue 标题/正文/评论、TAPD 来源、项目资源、代码仓库信息、上一阶段 handoff、运行复盘/trace。", "output": "阶段调度评论、跨项目 child issue、返工说明、最终验收摘要、issue done 状态。", "boundary": "PM 负责调度和判断，不代替 01-05 完成专业阶段；不得 checkout、编辑代码、运行实现测试或发布实现完成总结；单项目需求留在当前 issue；只有真实跨项目依赖才创建 child issue。", "forbidden": "不得直接实现需求；不得未获任务创建者或 workspace owner/admin 明确同意就跳过阶段；不得一次 @mention 多个下一阶段；不得让阶段 Agent 自己调下一阶段；不得为了推进阶段创建同项目 child issue；05 未通过不得 done。"},
					{"key": "01-clarify", "name": projectSOPAgent01, "responsibility": "读懂需求来源，确认用户要什么、不做什么、验收标准是什么。", "input": "issue、TAPD 正文、评论、项目背景、已有约束。", "output": "需求边界、已确认结论、未定问题、建议默认值、验收口径、目标项目/仓库初判、进入 02 条件、handoff。", "boundary": "只澄清需求，不写实现方案，不改代码。", "forbidden": "不得直接进入开发；不得自行 @mention 下一阶段。"},
					{"key": "02-design", "name": projectSOPAgent02, "responsibility": "基于已闭环澄清和项目上下文设计技术方案、影响面、接口/数据契约和测试策略。", "input": "已闭环的 01 handoff、用户确认、PM 记录的假设、项目资源、代码/接口背景、历史文档。", "output": "技术方案、采用的澄清假设、影响面、接口/数据变更、风险点、测试建议、handoff。", "boundary": "负责方案，不直接落大范围代码。", "forbidden": "不得绕过澄清结论；澄清未闭环时不得硬写方案；不得自行 @mention 下一阶段。"},
					{"key": "03-task-split", "name": projectSOPAgent03, "responsibility": "把方案拆成可执行任务，识别跨项目依赖、执行顺序和 V1/V2/V3 测试层级计划。", "input": "02 handoff、项目列表、仓库资源、依赖关系。", "output": "任务拆分、目标项目列表、跨项目 child issue 建议、依赖顺序、handoff；必须包含 required cross-project dependencies、not required projects、V1/V2/V3 test matrix 和 sandbox_plan。", "boundary": "负责拆分、依赖判断和测试计划；跨项目 child issue 由 PM 创建或确认。", "forbidden": "不得重复创建 child issue；不得把单项目阶段推进拆成同项目 child issue；不得把 handoff 文件当作跨项目交付完成。"},
					{"key": "04-implement", "name": projectSOPAgent04, "responsibility": "按确认范围实现代码、配置、测试或文档变更，并按 03 测试矩阵完成开发侧验证。", "input": "03 handoff、目标仓库、任务边界、V1/V2/V3 test matrix、相关 skill/operation 指引。", "output": "代码变更、开发侧验证结果、实现说明、风险说明、交给 05 的验证入口、handoff。", "boundary": "只改本任务范围内内容。", "forbidden": "不得越权改无关模块；不得缺测试就宣称完成；不得自行 @mention 下一阶段；不得重新定义 V1/V2/V3 测试层级。"},
					{"key": "05-verify", "name": projectSOPAgent05, "responsibility": "按 03 测试矩阵独立检查实现、测试结果、回归风险、证据完整性。", "input": "03 test matrix、04 handoff、diff、测试日志、验收标准、运行复盘/trace。", "output": "验证结论、缺陷/返工清单、V1/V2/V3 逐项结论、通过证据、最终 handoff；必须明确 child issue/MR 检查和真实 curl/grpcurl 证据或 BLOCKED 原因。", "boundary": "负责独立验收，不替开发自证。", "forbidden": "不得在证据不足时通过；不得直接 done issue，最终收口交给 PM；不得把 V2/V3 SKIP 当作真实外部 HTTP 验收通过。"},
				},
				"steps": []map[string]any{
					{"key": "pm", "name": projectSOPAgentPM, "role_key": "pm"},
					{"key": "01-clarify", "name": projectSOPAgent01, "role_key": "01-clarify", "skill": "<target-project>/01-clarify"},
					{"key": "02-design", "name": projectSOPAgent02, "role_key": "02-design", "skill": "<target-project>/02-design"},
					{"key": "03-task-split", "name": projectSOPAgent03, "role_key": "03-task-split", "skill": "<target-project>/03-task-split"},
					{"key": "04-implement", "name": projectSOPAgent04, "role_key": "04-implement", "skill": "<target-project>/04-implement"},
					{"key": "05-verify", "name": projectSOPAgent05, "role_key": "05-verify", "skill": "<target-project>/05-verify"},
				},
				"stage_skills":     []string{"<target-project>/01-clarify", "<target-project>/02-design", "<target-project>/03-task-split", "<target-project>/04-implement", "<target-project>/05-verify"},
				"operation_skills": []string{"<target-project>/<operation-skill>"},
				"mcp_servers":      []string{"mcp-server-tapd", "gongfeng"},
				"clarify_loop_policy": []string{
					"01-需求澄清可以多次运行；重复运行是澄清闭环，不代表流程失败或倒退。",
					"01 输出未定问题、待确认项或进入 02 条件未满足时，PM 只能总结问题并等待用户补充，不能进入 02-方案设计。",
					"用户明确回答关键问题，或表达“剩下按你的建议走”“按建议推进”“有问题再讨论”等授权语义时，PM 记录采用的默认假设并调度 02-方案设计。",
					"用户回复仍缺关键决策时，PM 重新调度 01-需求澄清，并带上上一轮未定问题、用户最新回复和仍需确认点。",
					"涉及安全、权限、数据破坏、外部发布或不可逆变更的高风险项，即使用户说“按建议走”，关键风险仍必须显式确认。",
				},
				"source_context": map[string]any{
					"tapd":     "从 task.source_context.tapd 获取 workspace_id/resource_type/resource_id/fetch_status；状态为 blocked_missing_profile 时必须阻断并要求用户配置账号级 TAPD profile。",
					"gongfeng": "从 project_resources.gongfeng_repo 或 git.code.tencent.com 链接解析项目、仓库、分支、提交和文件上下文；需要账号级 Gongfeng profile。",
					"project":  "目标项目必须来自 issue.project、project_resources、source_context 或用户明确输入；不得假设固定三仓。",
				},
				"acceptance": []string{"阶段产物完整", "测试证据完整", "交接说明明确", "03-task-split 明确 required cross-project dependencies、not required projects、V1/V2/V3 test matrix 和 sandbox_plan", "跨项目子 issue 由 PM 直接创建并可回读", "分配给 PM 小队的 child issue 必须由 PM 通过平台 @mention 触发独立 01/02/03/04/05 task", "05-verify 通过前必须检查必要 child issue、目标项目 MR、V1/V2/V3 test matrix 和真实 curl/grpcurl 证据", "05-verify 通过后 issue 状态为 done"},
				"cross_project_policy": map[string]any{
					"creation_owner":          "pm",
					"required_initial_status": "todo",
					"required_assignee_type":  "squad",
					"delegation_rule":         "03-task-split 只识别依赖和准备 handoff；PM 必须亲自创建或复用 required child issue，创建时带 parent、target project、status=todo，并分配目标项目小队；没有目标项目小队时分配 PM 小队递归执行。不得用评论、等待 03 或 handoff 文件代替创建 child issue。",
					"existing_child_repair":   "matching required child 已存在但为 backlog 或普通成员 assignee 时，PM 必须先更新为可执行 squad/Agent assignee 和 todo；无法修正则阻断。",
					"task_split_contract":     "03-task-split 必须输出 required cross-project dependencies、not required projects、V1/V2/V3 test matrix 和 sandbox_plan；handoff 文件不是目标项目完成证据。",
					"parent_wait_gate":        "任一 required child issue 未 done 时，父 issue 必须等待；PM 不得调度父 issue 的 04/05，不得把 child 创建成功当作依赖完成。",
					"verification_gate":       "05-verify 必须检查必要 child issue、目标项目 MR、V1/V2/V3 test matrix 和真实 curl/grpcurl 证据；必要依赖缺失、PENDING、handoff 待分发或 V2/V3 SKIP 时必须 BLOCKED。",
					"completion_gate":         "PM 完成前必须通过公开 issue children 回读确认所有必要目标项目子 issue 都存在，且 parent_issue_id、project_id、assignee_id 与本轮拆分计划一致；必要 child issue 完成并关联目标项目 MR 前不得收口。",
				},
				"cross_project_child_issues": []map[string]any{
					{
						"target_project": "<target-project>",
						"trigger":        "需求影响多个项目、仓库、服务、权限、部署或联调边界时，为每个目标项目创建对应 child issue；单项目阶段推进、TAPD 正文抓取和同项目澄清设计不得触发 child issue。",
						"assignee":       "目标项目负责人或对应小队",
						"title":          "为父 issue 补充目标项目交付项",
						"body":           "说明父 issue、目标项目、仓库路径、相关 operation skill、接口/配置/数据/验证要求和期望交付物。",
					},
				},
				"archive_policy":    "06-archive 不作为必跑阶段；最终结论、证据摘要和 handoff 状态由 05-verify 输出。",
				"forbidden_actions": []string{"跳过验收直接完成", "缺少测试证据时宣称完成", "PM 直接 checkout、编辑代码、运行实现测试或发布实现完成总结", "PM 未获任务创建者或 workspace owner/admin 明确同意就跳过 01-05 中任一阶段", "未确认目标项目就调用项目 skill", "把 06-archive 当作必跑验收阶段", "只评论或委派 03 代替 PM 创建跨项目子 issue", "把 TAPD 正文抓取后的真实需求复制成同项目 child issue", "为了进入 01-clarify/02-design/03-task-split/04-implement/05-verify 创建 child issue", "把 handoff-gateway.md 当作 gateway 交付完成", "必要跨项目 child issue 缺失、PENDING、handoff 待分发或未关联目标项目 MR 时继续父 issue 04/05 或最终收口", "把 V2 sandbox/V3 business E2E 的 SKIP 当作真实外部 HTTP 验收通过", "PM 在单个任务里使用任何运行时或模型原生的内部任务、todo/plan、子代理、并行代理、内部委派或状态记录工具代跑 child issue 的 01-05", "PM 把 source_summary 当作 01-clarify 完成", "父 issue 存在未完成 child issue 时更新为 done", "PM 发出平台 mention 调度评论后继续执行下一阶段 skill", "01-05 阶段 Agent @mention 下一阶段或任何负责人", "PM 一次评论 @mention 多个下一阶段", "05-verify 通过后只写验收通过但不更新 issue 状态为 done"},
			},
		}, true
	case "multica-coding":
		roles := []internalSquadRole{
			{Key: "captain", Name: "队长", Instruction: "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。", MemberRole: "队长"},
			{Key: "designer", Name: "方案设计者", Instruction: "编写技术方案、影响面、任务拆解、测试方案；重大开发前先给人确认。", MemberRole: "方案设计者"},
			{Key: "developer", Name: "开发者", Instruction: "只按分配范围改代码，包括前端、后端、测试或部署中的一块。", MemberRole: "开发者"},
			{Key: "acceptor", Name: "验收者", Instruction: "独立检查代码、测试结果、漏改和回归风险。", MemberRole: "验收者"},
			{Key: "spec-maintainer", Name: "规约维护者", Instruction: "判断是否同步流程文档、测试数据说明、接口索引、技能说明。", MemberRole: "规约维护者"},
			{Key: "operator", Name: "部署运行者", Instruction: "负责端口、环境变量、数据库、启动服务、健康检查、部署验证；不能泄露密钥。", MemberRole: "部署运行者"},
		}
		return internalSquadTemplate{
			Key:          "multica-coding",
			Name:         "Multica 编码小队",
			Description:  "用于开发 Multica 自身的生产级编码小队，包含队长、方案设计者、开发者、验收者、规约维护者和部署运行者。",
			Instructions: "队长先澄清需求和验收口径，再按角色分派；开发者不得越界；验收者必须独立给出证据；所有指标和输出使用中文。",
			Model:        internalSquadDefaultModel,
			Roles:        roles,
			Profile: map[string]any{
				"profile_key": "multica-coding",
				"project":     "multica",
				"repo":        "/data/ida/goal-test",
				"mode":        "coding_squad",
				"roles": []map[string]any{
					{"key": "captain", "name": "队长", "responsibility": "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。"},
					{"key": "designer", "name": "方案设计者", "responsibility": "编写技术方案、影响面、任务拆解、测试方案。"},
					{"key": "developer", "name": "开发者", "responsibility": "按分配范围改代码，不能越界。"},
					{"key": "acceptor", "name": "验收者", "responsibility": "独立检查代码、测试结果和漏改。"},
					{"key": "spec-maintainer", "name": "规约维护者", "responsibility": "同步流程文档、测试数据说明、接口索引、技能说明。"},
					{"key": "operator", "name": "部署运行者", "responsibility": "负责环境、启动、日志和健康检查。"},
				},
				"steps": []map[string]any{
					{"key": "receive", "name": "接收需求", "role_key": "captain"},
					{"key": "design_review", "name": "方案设计与确认", "role_key": "designer"},
					{"key": "implementation", "name": "分工开发", "role_key": "developer"},
					{"key": "independent_acceptance", "name": "独立验收", "role_key": "acceptor"},
					{"key": "spec_sync", "name": "规约同步", "role_key": "spec-maintainer"},
					{"key": "deploy_verify", "name": "部署运行验证", "role_key": "operator"},
					{"key": "final_report", "name": "证据汇总", "role_key": "captain"},
				},
				"stage_skills":      []string{},
				"operation_skills":  []string{},
				"acceptance":        []string{"方案经确认", "代码范围清晰", "验收者独立给结论", "测试证据完整", "规约同步或说明无需同步", "运行验证完成"},
				"forbidden_actions": []string{"泄露密钥", "开发者越权改范围外代码", "未独立验收就完成", "跳过测试证据", "文档接口语义停留在旧版本"},
			},
		}, true
	default:
		return internalSquadTemplate{}, false
	}
}

func userCenterSOPMCPConfig() []byte {
	return mustJSONBytes(map[string]any{
		"mcpServers": map[string]any{
			"mcp-server-tapd": map[string]any{
				"command": "uvx",
				"args": []string{
					"mcp-server-tapd",
					"--api-base-url=https://api.tapd.cn",
					"--tapd-base-url=https://www.tapd.cn",
					"--keep-links=true",
					"--tools-set=lookup_tapd_tool",
				},
			},
			"gongfeng": map[string]any{
				"command": "zsh",
				"args": []string{
					"-lc",
					". /root/.config/gongfeng-mcp/env && exec node /data/ida/gongfeng-mcp-server/dist/index.js",
				},
			},
		},
	})
}

// ── Converters ──────────────────────────────────────────────────────────────

func squadToResponse(s db.Squad) SquadResponse {
	return SquadResponse{
		ID:            uuidToString(s.ID),
		WorkspaceID:   uuidToString(s.WorkspaceID),
		Name:          s.Name,
		Description:   s.Description,
		Instructions:  s.Instructions,
		SOPProfile:    decodeSquadSOPProfile(s.SopProfile),
		AvatarURL:     textToPtr(s.AvatarUrl),
		Scope:         s.Scope,
		LeaderID:      uuidToString(s.LeaderID),
		CreatorID:     uuidToString(s.CreatorID),
		CreatedAt:     timestampToString(s.CreatedAt),
		UpdatedAt:     timestampToString(s.UpdatedAt),
		ArchivedAt:    timestampToPtr(s.ArchivedAt),
		ArchivedBy:    uuidToPtr(s.ArchivedBy),
		MemberPreview: []SquadMemberPreviewResponse{},
	}
}

func decodeSquadSOPProfile(raw []byte) any {
	var profile any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &profile)
	}
	if profile == nil {
		return map[string]any{}
	}
	return profile
}

func squadMemberToResponse(m db.SquadMember) SquadMemberResponse {
	return SquadMemberResponse{
		ID:         uuidToString(m.ID),
		SquadID:    uuidToString(m.SquadID),
		MemberType: m.MemberType,
		MemberID:   uuidToString(m.MemberID),
		Role:       m.Role,
		CreatedAt:  timestampToString(m.CreatedAt),
	}
}

func addSquadMemberPreview(summary *squadMemberSummary, memberType string, memberID pgtype.UUID, role string) {
	summary.count++
	if len(summary.preview) >= 3 {
		return
	}
	summary.preview = append(summary.preview, SquadMemberPreviewResponse{
		MemberType: memberType,
		MemberID:   uuidToString(memberID),
		Role:       role,
	})
}

func applySquadMemberSummary(resp *SquadResponse, summary *squadMemberSummary) {
	if summary == nil {
		return
	}
	resp.MemberCount = summary.count
	resp.MemberPreview = summary.preview
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// loadSquadInWorkspace loads a squad scoped to the current workspace.
func (h *Handler) loadSquadInWorkspace(w http.ResponseWriter, r *http.Request) (db.Squad, string, bool) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squadID := chi.URLParam(r, "id")
	squadUUID, ok := parseUUIDOrBadRequest(w, squadID, "squad id")
	if !ok {
		return db.Squad{}, "", false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.Squad{}, "", false
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          squadUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return db.Squad{}, "", false
	}
	return squad, workspaceID, true
}

func (h *Handler) requireSquadVisible(w http.ResponseWriter, r *http.Request, squad db.Squad, workspaceID string) bool {
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if h.canUseSquad(r.Context(), squad, actorType, actorID, workspaceID) {
		return true
	}
	writeError(w, http.StatusNotFound, "squad not found")
	return false
}

func (h *Handler) requireSquadManager(w http.ResponseWriter, r *http.Request, squad db.Squad, workspaceID string) (db.Member, bool) {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Member{}, false
	}
	if !memberCanManageSquad(squad, member) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) loadSquadMemberSummary(ctx context.Context, squadID pgtype.UUID) (*squadMemberSummary, error) {
	rows, err := h.Queries.ListSquadMemberPreviewRowsBySquad(ctx, squadID)
	if err != nil {
		return nil, err
	}
	summary := &squadMemberSummary{}
	for _, row := range rows {
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}
	return summary, nil
}

func (h *Handler) squadToResponseWithPreview(ctx context.Context, squad db.Squad) (SquadResponse, error) {
	resp := squadToResponse(squad)
	summary, err := h.loadSquadMemberSummary(ctx, squad.ID)
	if err != nil {
		return resp, err
	}
	applySquadMemberSummary(&resp, summary)
	return resp, nil
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (h *Handler) ListSquads(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	var squads []db.Squad
	var err error
	if includeArchived {
		squads, err = h.Queries.ListAllSquads(r.Context(), wsUUID)
	} else {
		squads, err = h.Queries.ListSquads(r.Context(), wsUUID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squads")
		return
	}

	summaries := make(map[string]*squadMemberSummary, len(squads))
	if includeArchived {
		previewRows, err := h.Queries.ListAllSquadMemberPreviewRows(r.Context(), wsUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
			return
		}
		for _, row := range previewRows {
			squadID := uuidToString(row.SquadID)
			summary := summaries[squadID]
			if summary == nil {
				summary = &squadMemberSummary{}
				summaries[squadID] = summary
			}
			addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
		}
	} else {
		previewRows, err := h.Queries.ListSquadMemberPreviewRows(r.Context(), wsUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
			return
		}
		for _, row := range previewRows {
			squadID := uuidToString(row.SquadID)
			summary := summaries[squadID]
			if summary == nil {
				summary = &squadMemberSummary{}
				summaries[squadID] = summary
			}
			addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
		}
	}

	resp := make([]SquadResponse, 0, len(squads))
	for _, s := range squads {
		if !memberCanUseSquad(s, member) {
			continue
		}
		item := squadToResponse(s)
		applySquadMemberSummary(&item, summaries[uuidToString(s.ID)])
		resp = append(resp, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		LeaderID    string          `json:"leader_id"`
		AvatarURL   *string         `json:"avatar_url"`
		Scope       string          `json:"scope"`
		SOPProfile  json.RawMessage `json:"sop_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.LeaderID == "" {
		writeError(w, http.StatusBadRequest, "leader_id is required")
		return
	}
	scope, validScope := normalizeSquadScope(req.Scope)
	if !validScope {
		writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
		return
	}

	leaderUUID, ok := parseUUIDOrBadRequest(w, req.LeaderID, "leader_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Validate leader is an agent in this workspace.
	leader, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          leaderUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
		return
	}
	if !h.canAccessPersonalAgent(r.Context(), leader, "member", uuidToString(member.UserID), workspaceID) {
		writeError(w, http.StatusForbidden, "cannot use personal leader agent")
		return
	}
	if err := validateSquadLeaderScope(scope, member.UserID, leader); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	avatarURL := pgtype.Text{}
	if req.AvatarURL != nil {
		avatarURL = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	var sopProfile []byte
	if len(req.SOPProfile) > 0 {
		if !json.Valid(req.SOPProfile) {
			writeError(w, http.StatusBadRequest, "sop_profile must be valid JSON")
			return
		}
		sopProfile = req.SOPProfile
	}

	squad, err := h.Queries.CreateSquad(r.Context(), db.CreateSquadParams{
		WorkspaceID:  wsUUID,
		Name:         req.Name,
		Description:  req.Description,
		LeaderID:     leaderUUID,
		CreatorID:    member.UserID,
		AvatarUrl:    avatarURL,
		Scope:        pgtype.Text{String: scope, Valid: true},
		Instructions: pgtype.Text{},
		SopProfile:   sopProfile,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create squad")
		return
	}

	// Auto-add leader as a member with role "leader".
	h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: "agent",
		MemberID:   leaderUUID,
		Role:       "leader",
	})

	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publish(protocol.EventSquadCreated, workspaceID, "member", uuidToString(member.UserID), map[string]any{"squad": resp})
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.SquadCreated(
		uuidToString(member.UserID),
		workspaceID,
		uuidToString(squad.ID),
		1,
	))
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) EnsureInternalSquadTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		TemplateKey     string `json:"template_key"`
		RuntimeProvider string `json:"runtime_provider"`
		Model           string `json:"model"`
		Scope           string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	template, ok := internalSquadTemplateByKey(req.TemplateKey)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_key must be user-center or multica-coding")
		return
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		template.Model = model
	}
	provider := normalizeProvider(req.RuntimeProvider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	scope, validScope := normalizeSquadScope(req.Scope)
	if !validScope {
		writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
		return
	}

	agentScope := internalSquadAgentScope(scope)
	runtime, ok := h.selectInternalSquadRuntime(w, r, wsUUID, member, provider, agentScope)
	if !ok {
		return
	}
	if err := validateAgentRuntimeScope(agentScope, member.UserID, runtime); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agents, err := h.ensureInternalSquadAgents(r.Context(), wsUUID, member.UserID, runtime, template, scope)
	if err != nil {
		slog.Warn("ensure internal squad agents failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad agents")
		return
	}
	squad, err := h.ensureInternalSquad(r.Context(), wsUUID, member.UserID, template, scope, agents)
	if err != nil {
		slog.Warn("ensure internal squad failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load internal squad")
		return
	}
	writeJSON(w, http.StatusOK, InternalSquadTemplateResponse{Squad: resp, Agents: agents})
}

func (h *Handler) selectInternalSquadRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member, provider string, agentScope string) (db.AgentRuntime, bool) {
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return db.AgentRuntime{}, false
	}
	checkedAt := time.Now().UTC()
	provider = normalizeProvider(provider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	providerName := provider
	if len(providerName) > 0 {
		providerName = strings.ToUpper(providerName[:1]) + providerName[1:]
	}
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, provider) || !canUseRuntimeForAgent(member, runtime) {
			continue
		}
		if !agentRuntimeScopeCompatible(agentScope, member.UserID, runtime) {
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		writeError(w, http.StatusServiceUnavailable, "当前 workspace 没有可用于"+internalSquadRuntimeScopeLabel(agentScope)+"小队的 "+providerName+" runtime，无法创建真实可执行的内部小队。请先启动 multica daemon，并确认 /api/runtimes 出现 provider="+provider+" 且范围匹配的在线 runtime。")
		return db.AgentRuntime{}, false
	}
	if best.Status != "online" || !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		writeError(w, http.StatusServiceUnavailable, providerName+" runtime 当前未就绪，无法创建真实可执行的内部小队。请启动 daemon 并等待 runtime 心跳刷新。")
		return db.AgentRuntime{}, false
	}
	return *best, true
}

func internalSquadRuntimeScopeLabel(agentScope string) string {
	if agentScope == scopePersonal {
		return "个人"
	}
	return "工作区"
}

func (h *Handler) ensureInternalSquadAgents(ctx context.Context, workspaceID pgtype.UUID, ownerID pgtype.UUID, runtime db.AgentRuntime, template internalSquadTemplate, squadScope string) ([]InternalSquadAgent, error) {
	existing, err := h.Queries.ListAllAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]InternalSquadAgent, 0, len(template.Roles))
	agentScope := internalSquadAgentScope(squadScope)
	for _, role := range template.Roles {
		name := strings.TrimSpace(role.AgentName)
		if name == "" {
			name = template.Name + " · " + role.Name
		}
		runtimeConfig := internalSquadAgentRuntimeConfig(runtime, template, role, squadScope, agentScope, ownerID)
		instructions := "你是" + template.Name + "小队的" + role.Name + "。" + role.Instruction + "所有输出必须使用中文，并保留可验收证据。"
		description := internalSquadRoleDescription(template, role)
		model := pgtype.Text{String: template.Model, Valid: true}
		agentRow, ok := findInternalSquadAgent(existing, name, template, role, squadScope, agentScope, ownerID)
		if !ok {
			agentRow, err = h.Queries.CreateAgent(ctx, db.CreateAgentParams{
				WorkspaceID:        workspaceID,
				Name:               name,
				Description:        description,
				Instructions:       instructions,
				RuntimeMode:        runtime.RuntimeMode,
				RuntimeConfig:      runtimeConfig,
				RuntimeID:          runtime.ID,
				Scope:              agentScope,
				MaxConcurrentTasks: defaultAgentMaxConcurrentTasks,
				OwnerID:            ownerID,
				CustomEnv:          []byte("{}"),
				CustomArgs:         []byte("[]"),
				McpConfig:          role.MCPConfig,
				Model:              model,
			})
			if err != nil {
				return nil, err
			}
		} else {
			if agentRow.ArchivedAt.Valid {
				agentRow, err = h.Queries.RestoreAgent(ctx, agentRow.ID)
				if err != nil {
					return nil, err
				}
			}
			if internalSquadAgentNeedsSync(agentRow, runtime, template, role, runtimeConfig, instructions, description, model, agentScope) {
				agentRow, err = h.Queries.UpdateAgent(ctx, db.UpdateAgentParams{
					ID:                 agentRow.ID,
					Description:        pgtype.Text{String: description, Valid: true},
					RuntimeConfig:      runtimeConfig,
					RuntimeMode:        pgtype.Text{String: runtime.RuntimeMode, Valid: true},
					RuntimeID:          runtime.ID,
					Scope:              pgtype.Text{String: agentScope, Valid: true},
					MaxConcurrentTasks: pgtype.Int4{Int32: defaultAgentMaxConcurrentTasks, Valid: true},
					Instructions:       pgtype.Text{String: instructions, Valid: true},
					CustomArgs:         []byte("[]"),
					McpConfig:          role.MCPConfig,
					Model:              model,
				})
				if err != nil {
					return nil, err
				}
			}
		}
		result = append(result, InternalSquadAgent{
			ID:      uuidToString(agentRow.ID),
			Name:    agentRow.Name,
			RoleKey: role.Key,
			Role:    role.Name,
		})
	}
	return result, nil
}

func internalSquadAgentScope(squadScope string) string {
	if squadScope == squadScopePersonal {
		return scopePersonal
	}
	return scopeWorkspace
}

func internalSquadAgentRuntimeConfig(runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) []byte {
	scopeOwnerID := ""
	if squadScope == squadScopePersonal {
		scopeOwnerID = uuidToString(ownerID)
	}
	return mustJSONBytes(map[string]any{
		"provider": runtime.Provider,
		"用途":       template.Name,
		"角色":       role.Name,
		"模板":       template.Key,
		"internal_squad": map[string]any{
			"template_key": template.Key,
			"role_key":     role.Key,
			"squad_scope":  squadScope,
			"agent_scope":  agentScope,
			"owner_id":     scopeOwnerID,
		},
	})
}

func findInternalSquadAgent(agents []db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) (db.Agent, bool) {
	var archivedMatch db.Agent
	for _, agent := range agents {
		if !matchesInternalSquadAgent(agent, name, template, role, squadScope, agentScope, ownerID) {
			continue
		}
		if !agent.ArchivedAt.Valid {
			return agent, true
		}
		if uuidToString(archivedMatch.ID) == "" || agent.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = agent
		}
	}
	if uuidToString(archivedMatch.ID) != "" {
		return archivedMatch, true
	}
	return db.Agent{}, false
}

func matchesInternalSquadAgent(agent db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadScope string, agentScope string, ownerID pgtype.UUID) bool {
	if agent.Name != name || agent.Scope != agentScope {
		return false
	}
	if squadScope == squadScopePersonal && uuidToString(agent.OwnerID) != uuidToString(ownerID) {
		return false
	}
	var runtimeConfig map[string]any
	if len(bytes.TrimSpace(agent.RuntimeConfig)) == 0 || json.Unmarshal(agent.RuntimeConfig, &runtimeConfig) != nil {
		return false
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		if stringFromAny(scope["template_key"]) != template.Key ||
			stringFromAny(scope["role_key"]) != role.Key ||
			stringFromAny(scope["squad_scope"]) != squadScope ||
			stringFromAny(scope["agent_scope"]) != agentScope {
			return false
		}
		if squadScope == squadScopePersonal && stringFromAny(scope["owner_id"]) != uuidToString(ownerID) {
			return false
		}
		return true
	}
	return stringFromAny(runtimeConfig["模板"]) == template.Key && stringFromAny(runtimeConfig["角色"]) == role.Name
}

func internalSquadAgentNeedsSync(agent db.Agent, runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, runtimeConfig []byte, instructions string, description string, model pgtype.Text, scope string) bool {
	if agent.Description != description ||
		agent.RuntimeMode != runtime.RuntimeMode ||
		uuidToString(agent.RuntimeID) != uuidToString(runtime.ID) ||
		agent.Scope != scope ||
		agent.MaxConcurrentTasks != defaultAgentMaxConcurrentTasks ||
		agent.Instructions != instructions ||
		!bytes.Equal(bytes.TrimSpace(agent.RuntimeConfig), bytes.TrimSpace(runtimeConfig)) ||
		!bytes.Equal(bytes.TrimSpace(agent.CustomArgs), []byte("[]")) ||
		!bytes.Equal(bytes.TrimSpace(agent.McpConfig), bytes.TrimSpace(role.MCPConfig)) {
		return true
	}
	if model.Valid != agent.Model.Valid {
		return true
	}
	if model.Valid && model.String != agent.Model.String {
		return true
	}
	return false
}

func internalSquadRoleDescription(template internalSquadTemplate, role internalSquadRole) string {
	if strings.TrimSpace(role.Description) != "" {
		return strings.TrimSpace(role.Description)
	}
	return template.Description
}

func (h *Handler) ensureInternalSquad(ctx context.Context, workspaceID pgtype.UUID, creatorID pgtype.UUID, template internalSquadTemplate, scope string, agents []InternalSquadAgent) (db.Squad, error) {
	if len(agents) == 0 {
		return db.Squad{}, pgx.ErrNoRows
	}
	squads, err := h.Queries.ListAllSquads(ctx, workspaceID)
	if err != nil {
		return db.Squad{}, err
	}
	var squad db.Squad
	var archivedMatch db.Squad
	for _, item := range squads {
		if !matchesInternalSquadTemplate(item, template, scope, creatorID) {
			continue
		}
		if !item.ArchivedAt.Valid {
			squad = item
			break
		}
		if uuidToString(archivedMatch.ID) == "" || item.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = item
		}
	}
	if uuidToString(squad.ID) == "" {
		if uuidToString(archivedMatch.ID) != "" {
			squad, err = h.Queries.RestoreSquad(ctx, archivedMatch.ID)
			if err != nil {
				return db.Squad{}, err
			}
		} else {
			sopProfile := mustJSONBytes(template.Profile)
			squad, err = h.Queries.CreateSquad(ctx, db.CreateSquadParams{
				WorkspaceID:  workspaceID,
				Name:         template.Name,
				Description:  template.Description,
				LeaderID:     parseUUID(agents[0].ID),
				CreatorID:    creatorID,
				Scope:        pgtype.Text{String: scope, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   sopProfile,
			})
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	if uuidToString(squad.ID) != "" {
		profileBytes := mustJSONBytes(template.Profile)
		leaderID := parseUUID(agents[0].ID)
		if itemNeedsInternalSquadSync(squad, template, profileBytes, leaderID, scope) {
			params := db.UpdateSquadParams{
				ID:           squad.ID,
				Name:         pgtype.Text{String: template.Name, Valid: squad.Name != template.Name},
				Description:  pgtype.Text{String: template.Description, Valid: true},
				LeaderID:     leaderID,
				Scope:        pgtype.Text{String: scope, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   profileBytes,
			}
			squad, err = h.Queries.UpdateSquad(ctx, params)
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	existingMembers, err := h.Queries.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		return db.Squad{}, err
	}
	desiredAgentMembers := map[string]struct{}{}
	for _, agent := range agents {
		desiredAgentMembers[agent.ID] = struct{}{}
	}
	existingMemberRoles := map[string]string{}
	for _, member := range existingMembers {
		memberID := uuidToString(member.MemberID)
		if member.MemberType == "agent" {
			if _, keep := desiredAgentMembers[memberID]; !keep {
				if _, err := h.Queries.RemoveSquadMember(ctx, db.RemoveSquadMemberParams{
					SquadID:    squad.ID,
					MemberType: member.MemberType,
					MemberID:   member.MemberID,
				}); err != nil {
					return db.Squad{}, err
				}
				continue
			}
		}
		existingMemberRoles[member.MemberType+":"+memberID] = member.Role
	}
	for _, agent := range agents {
		role := "member"
		for _, templateRole := range template.Roles {
			if templateRole.Key == agent.RoleKey {
				role = templateRole.MemberRole
				break
			}
		}
		memberID := parseUUID(agent.ID)
		memberKey := "agent:" + agent.ID
		if existingRole, exists := existingMemberRoles[memberKey]; exists {
			if existingRole != role {
				if _, err := h.Queries.UpdateSquadMemberRole(ctx, db.UpdateSquadMemberRoleParams{
					SquadID:    squad.ID,
					MemberType: "agent",
					MemberID:   memberID,
					Role:       role,
				}); err != nil {
					return db.Squad{}, err
				}
			}
			continue
		}
		if _, err := h.Queries.AddSquadMember(ctx, db.AddSquadMemberParams{
			SquadID:    squad.ID,
			MemberType: "agent",
			MemberID:   memberID,
			Role:       role,
		}); err != nil {
			return db.Squad{}, err
		}
		existingMemberRoles[memberKey] = role
	}
	return squad, nil
}

func matchesInternalSquadTemplate(squad db.Squad, template internalSquadTemplate, scope string, creatorID pgtype.UUID) bool {
	profile := decodeJSONDefault(squad.SopProfile, map[string]any{})
	profileMap, _ := profile.(map[string]any)
	sameTemplate := squad.Name == template.Name || stringFromAny(profileMap["profile_key"]) == template.Key
	sameScope := squad.Scope == scope
	sameCreator := scope != squadScopePersonal || uuidToString(squad.CreatorID) == uuidToString(creatorID)
	return sameTemplate && sameScope && sameCreator
}

func itemNeedsInternalSquadSync(squad db.Squad, template internalSquadTemplate, profileBytes []byte, leaderID pgtype.UUID, scope string) bool {
	return squad.Name != template.Name ||
		squad.Description != template.Description ||
		uuidToString(squad.LeaderID) != uuidToString(leaderID) ||
		squad.Scope != scope ||
		!bytes.Equal(bytes.TrimSpace(squad.SopProfile), bytes.TrimSpace(profileBytes)) ||
		squad.Instructions != template.Instructions
}

func (h *Handler) GetSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UpdateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireSquadManager(w, r, squad, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		Name         *string         `json:"name"`
		Description  *string         `json:"description"`
		Instructions *string         `json:"instructions"`
		LeaderID     *string         `json:"leader_id"`
		AvatarURL    *string         `json:"avatar_url"`
		Scope        *string         `json:"scope"`
		SOPProfile   json.RawMessage `json:"sop_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateSquadParams{ID: squad.ID}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.AvatarURL != nil {
		params.AvatarUrl = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	nextScope := squad.Scope
	if req.Scope != nil {
		scope, validScope := normalizeSquadScope(*req.Scope)
		if !validScope {
			writeError(w, http.StatusBadRequest, "scope must be 'workspace' or 'personal'")
			return
		}
		nextScope = scope
		params.Scope = pgtype.Text{String: scope, Valid: true}
	}
	if len(req.SOPProfile) > 0 {
		if !json.Valid(req.SOPProfile) {
			writeError(w, http.StatusBadRequest, "sop_profile must be valid JSON")
			return
		}
		params.SopProfile = req.SOPProfile
	}
	nextLeaderID := squad.LeaderID
	var nextLeader db.Agent
	haveNextLeader := false
	if req.LeaderID != nil {
		lid, ok := parseUUIDOrBadRequest(w, *req.LeaderID, "leader_id")
		if !ok {
			return
		}
		// Validate new leader is an agent in workspace.
		leader, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: lid, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
			return
		}
		if !h.canAccessPersonalAgent(r.Context(), leader, "member", uuidToString(member.UserID), workspaceID) {
			writeError(w, http.StatusForbidden, "cannot use personal leader agent")
			return
		}
		nextLeader = leader
		haveNextLeader = true
		// Ensure new leader is a squad member; auto-add if not.
		isMember, _ := h.Queries.IsSquadMember(r.Context(), db.IsSquadMemberParams{
			SquadID: squad.ID, MemberType: "agent", MemberID: lid,
		})
		if !isMember {
			h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
				SquadID: squad.ID, MemberType: "agent", MemberID: lid, Role: "leader",
			})
		}
		params.LeaderID = lid
		nextLeaderID = lid
	}
	if req.Scope != nil || req.LeaderID != nil {
		if !haveNextLeader {
			leader, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
				ID: nextLeaderID, WorkspaceID: wsUUID,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
				return
			}
			nextLeader = leader
		}
		if err := validateSquadLeaderScope(nextScope, squad.CreatorID, nextLeader); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	updated, err := h.Queries.UpdateSquad(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}

	resp, err := h.squadToResponseWithPreview(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
		return
	}

	if squad.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "squad is already archived")
		return
	}

	// Transfer issues assigned to this squad to the leader agent.
	if err := h.Queries.TransferSquadAssignees(r.Context(), db.TransferSquadAssigneesParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Warn("transfer squad assignees failed", "squad_id", uuidToString(squad.ID), "error", err)
	}

	// Mirror the issue-assignee transfer for autopilots that target this
	// squad. Without this, autopilot.assignee_id would still point at the
	// archived squad row and every subsequent dispatch would skip with
	// "assignee squad is archived" — visible to ops but useless to the
	// owner. Rewriting to the leader keeps the autopilot semantics
	// unchanged (Path A from MUL-2429 is leader-only execution anyway).
	if err := h.Queries.TransferSquadAutopilotsToLeader(r.Context(), db.TransferSquadAutopilotsToLeaderParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Warn("transfer squad autopilots failed", "squad_id", uuidToString(squad.ID), "error", err)
	}

	userID := requestUserID(r)
	userUUID, _ := parseUUIDOrBadRequest(w, userID, "user_id")

	if _, err := h.Queries.ArchiveSquad(r.Context(), db.ArchiveSquadParams{
		ID:         squad.ID,
		ArchivedBy: userUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive squad")
		return
	}

	h.publish(protocol.EventSquadDeleted, workspaceID, "member", userID, map[string]any{
		"squad_id":  uuidToString(squad.ID),
		"leader_id": uuidToString(squad.LeaderID),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RestoreSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
		return
	}
	if !squad.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "squad is not archived")
		return
	}

	restored, err := h.Queries.RestoreSquad(r.Context(), squad.ID)
	if err != nil {
		slog.Warn("restore squad failed", append(logger.RequestAttrs(r), "error", err, "squad_id", uuidToString(squad.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to restore squad")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), restored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventSquadRestored, workspaceID, "member", userID, map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad Members ───────────────────────────────────────────────────────────

func (h *Handler) ListSquadMembers(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
		return
	}
	members, err := h.Queries.ListSquadMembers(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad members")
		return
	}
	resp := make([]SquadMemberResponse, len(members))
	for i, m := range members {
		resp[i] = squadMemberToResponse(m)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad Member Status ────────────────────────────────────────────────────

// SquadMemberStatus is the per-member entry in the squad member status
// response. Agent members carry a derived working/idle/offline/unstable
// status plus any active issues; human members are returned with member_type
// only so the front-end can render them in the same list without
// reordering.
type SquadMemberStatusResponse struct {
	MemberType   string                  `json:"member_type"`
	MemberID     string                  `json:"member_id"`
	Status       *string                 `json:"status"`
	ActiveIssues []SquadActiveIssueBrief `json:"active_issues"`
	LastActiveAt *string                 `json:"last_active_at"`
}

type SquadActiveIssueBrief struct {
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	IssueStatus string `json:"issue_status"`
}

type SquadMemberStatusListResponse struct {
	Members []SquadMemberStatusResponse `json:"members"`
}

// deriveSquadMemberStatus collapses runtime + task signals into the five
// status buckets used by the squad UI. Mirrors the workload+availability
// split in packages/core/agents/derive-presence.ts: working wins over
// runtime health (an agent that is in the middle of dispatched/running
// work counts as working even if the runtime briefly drops), then
// availability buckets decide between idle / unstable / offline.
//
// Thresholds match deriveRuntimeHealth: any offline runtime whose
// last_seen_at is within the last 5 minutes is reported as "unstable" so
// the squad UI surfaces transient drops the same way the agent dot does.
//
// Archived agents always report `archived` regardless of any leftover
// runtime row or task — they should appear in the list but never look
// like they're still working or merely offline (a leftover online
// runtime row would otherwise read as "offline" and hide the fact that
// the agent has been archived). Per the RFC decision (see MUL-2319), we
// surface archived agents in this endpoint rather than filtering them
// out in the SQL.
func deriveSquadMemberStatus(
	archived bool,
	runtimeStatus pgtype.Text,
	lastSeen pgtype.Timestamptz,
	hasActiveTask bool,
	now time.Time,
) string {
	if archived {
		return "archived"
	}
	if hasActiveTask {
		return "working"
	}
	if !runtimeStatus.Valid {
		return "offline"
	}
	if runtimeStatus.String == "online" {
		return "idle"
	}
	if !lastSeen.Valid {
		return "offline"
	}
	if now.Sub(lastSeen.Time) < 5*time.Minute {
		return "unstable"
	}
	return "offline"
}

// ListSquadMemberStatus returns one entry per squad member with derived
// status, the issues each agent member is currently running, and the last
// observed runtime activity. The endpoint is read-only and inherits the
// workspace-membership guard from the route middleware — any member of the
// workspace can read it.
func (h *Handler) ListSquadMemberStatus(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
		return
	}

	rows, err := h.Queries.ListSquadMemberStatusRows(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member status")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), squad.WorkspaceID)
	now := time.Now()

	// Group rows by member_id while preserving the SQL ORDER BY (squad_member
	// insertion order). One member may appear in multiple rows when they have
	// more than one active task.
	type memberAcc struct {
		response       SquadMemberStatusResponse
		archived       bool
		hasActiveTask  bool
		runtimeStatus  pgtype.Text
		runtimeSeenAt  pgtype.Timestamptz
		latestActiveAt pgtype.Timestamptz
	}
	order := make([]string, 0, len(rows))
	acc := make(map[string]*memberAcc, len(rows))

	for _, row := range rows {
		memberID := uuidToString(row.MemberID)
		entry, exists := acc[memberID]
		if !exists {
			entry = &memberAcc{
				response: SquadMemberStatusResponse{
					MemberType:   row.MemberType,
					MemberID:     memberID,
					ActiveIssues: []SquadActiveIssueBrief{},
				},
				archived:      row.AgentArchivedAt.Valid,
				runtimeStatus: row.RuntimeStatus,
				runtimeSeenAt: row.RuntimeLastSeenAt,
			}
			acc[memberID] = entry
			order = append(order, memberID)
		}

		if row.MemberType != "agent" {
			continue
		}

		// A dispatched/running task occupies an agent slot even when it
		// has no associated issue (chat / quick-create tasks set
		// agent_task_queue.issue_id = NULL). The `working` bucket is
		// defined by task presence, not by whether we can render an
		// issue link, so flag the agent here regardless of issue_id.
		if row.TaskID.Valid {
			entry.hasActiveTask = true

			if row.TaskIssueID.Valid {
				brief := SquadActiveIssueBrief{
					IssueID:    uuidToString(row.TaskIssueID),
					Identifier: prefix + "-" + strconv.Itoa(int(row.IssueNumber.Int32)),
					Title:      row.IssueTitle.String,
					IssueStatus: func() string {
						if row.IssueStatus.Valid {
							return row.IssueStatus.String
						}
						return ""
					}(),
				}
				entry.response.ActiveIssues = append(entry.response.ActiveIssues, brief)
			}

			if row.TaskDispatchedAt.Valid && (!entry.latestActiveAt.Valid ||
				row.TaskDispatchedAt.Time.After(entry.latestActiveAt.Time)) {
				entry.latestActiveAt = row.TaskDispatchedAt
			}
		}
	}

	resp := SquadMemberStatusListResponse{
		Members: make([]SquadMemberStatusResponse, 0, len(order)),
	}
	for _, id := range order {
		entry := acc[id]
		if entry.response.MemberType == "agent" {
			status := deriveSquadMemberStatus(
				entry.archived,
				entry.runtimeStatus,
				entry.runtimeSeenAt,
				entry.hasActiveTask,
				now,
			)
			entry.response.Status = &status
			// last_active_at prefers the freshest active-task dispatch
			// over the runtime heartbeat: a working agent should not
			// look stale because the runtime heartbeat is a few seconds
			// behind. Falls back to runtime last_seen_at otherwise.
			if entry.latestActiveAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.latestActiveAt)
			} else if entry.runtimeSeenAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.runtimeSeenAt)
			}
		}
		resp.Members = append(resp.Members, entry.response)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) AddSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireSquadManager(w, r, squad, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemberType != "agent" && req.MemberType != "member" {
		writeError(w, http.StatusBadRequest, "member_type must be 'agent' or 'member'")
		return
	}
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "member_id is required")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// Validate the member belongs to this workspace.
	if req.MemberType == "agent" {
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: memberUUID, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent not found in this workspace")
			return
		}
		if !h.canAccessPersonalAgent(r.Context(), agent, "member", uuidToString(member.UserID), workspaceID) {
			writeError(w, http.StatusForbidden, "cannot add personal agent")
			return
		}
		if err := validateSquadLeaderScope(squad.Scope, squad.CreatorID, agent); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: memberUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "member not found in this workspace")
			return
		}
	}

	sm, err := h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "member already in squad")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add squad member")
		return
	}

	writeJSON(w, http.StatusCreated, squadMemberToResponse(sm))
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
}

func (h *Handler) RemoveSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// Prevent removing the leader.
	if req.MemberType == "agent" && uuidToString(squad.LeaderID) == req.MemberID {
		writeError(w, http.StatusBadRequest, "cannot remove the squad leader; change leader first")
		return
	}

	rows, err := h.Queries.RemoveSquadMember(r.Context(), db.RemoveSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove squad member")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}

	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UpdateSquadMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	sm, err := h.Queries.UpdateSquadMemberRole(r.Context(), db.UpdateSquadMemberRoleParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}

	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
	writeJSON(w, http.StatusOK, squadMemberToResponse(sm))
}

// ── Squad Leader Evaluation ──────────────────────────────────────────────────

// RecordSquadLeaderEvaluation records a squad leader's evaluation decision
// into the unified activity_log. Called by the leader agent via CLI after
// each trigger to record whether it took action, stayed silent, or failed.
func (h *Handler) RecordSquadLeaderEvaluation(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Outcome string `json:"outcome"` // action | no_action | failed
		Reason  string `json:"reason"`  // short explanation from leader
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Outcome != "action" && req.Outcome != "no_action" && req.Outcome != "failed" {
		writeError(w, http.StatusBadRequest, "outcome must be 'action', 'no_action', or 'failed'")
		return
	}

	// The issue must be assigned to a squad.
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusBadRequest, "issue is not assigned to a squad")
		return
	}

	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}

	// Security: only the squad leader agent can record evaluations.
	workspaceID := uuidToString(issue.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" || actorID != uuidToString(squad.LeaderID) {
		writeError(w, http.StatusForbidden, "only the squad leader agent can record evaluations")
		return
	}

	taskID := r.Header.Get("X-Task-ID")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil || !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest, "task does not belong to issue")
		return
	}

	details, _ := json.Marshal(map[string]string{
		"squad_id": uuidToString(squad.ID),
		"task_id":  util.UUIDToString(taskUUID),
		"outcome":  req.Outcome,
		"reason":   req.Reason,
	})

	activity, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   pgtype.Text{String: "agent", Valid: true},
		ActorID:     squad.LeaderID,
		Action:      "squad_leader_evaluated",
		Details:     details,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record evaluation")
		return
	}

	h.publish(protocol.EventActivityCreated, uuidToString(issue.WorkspaceID), "agent", actorID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(activity.ID),
			"actor_type": "agent",
			"actor_id":   actorID,
			"action":     activity.Action,
			"details":    json.RawMessage(details),
			"created_at": timestampToString(activity.CreatedAt),
		},
	})

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":         uuidToString(activity.ID),
		"action":     activity.Action,
		"created_at": timestampToString(activity.CreatedAt),
	})
}

// ── Squad Trigger Logic ─────────────────────────────────────────────────────

// lastTaskWasLeader returns true when the agent's most recent task on the
// issue was enqueued in the squad-leader role. Used by the self-trigger
// guards to tell apart a comment posted while the agent was acting as
// leader (skip) from one posted while it was acting as a worker (do not
// skip). When the agent has no prior task on this issue the role is
// undetermined and we treat it as non-leader so a brand-new external
// trigger can still reach the leader.
func (h *Handler) lastTaskWasLeader(ctx context.Context, issueID, agentID pgtype.UUID) bool {
	flag, err := h.Queries.GetLatestTaskIsLeaderForIssueAndAgent(ctx, db.GetLatestTaskIsLeaderForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
	if err != nil {
		return false
	}
	return flag
}

// commentMentionsAnyone returns true when the comment body contains at least
// one routing-style mention — [@Name](mention://agent|member|squad|all/<id>).
// Issue cross-references (mention://issue/...) are ignored because they are
// not directed at a participant. Only the current comment is inspected —
// parent (thread root) mentions are NOT inherited here.
func commentMentionsAnyone(content string) bool {
	for _, m := range util.ParseMentions(content) {
		switch m.Type {
		case "agent", "member", "squad", "all":
			return true
		}
	}
	return false
}

// shouldEnqueueSquadLeaderOnAssign returns true when assigning an issue to a
// squad (or creating an issue pre-assigned to a squad) should immediately
// trigger the squad leader. Mirrors shouldEnqueueAgentTask: backlog issues
// are skipped (parking lot), and the leader agent must have a runtime and
// not be archived.
func (h *Handler) shouldEnqueueSquadLeaderOnAssign(ctx context.Context, issue db.Issue) bool {
	if issue.Status == "backlog" {
		return false
	}
	return h.isSquadLeaderReady(ctx, issue)
}

// isSquadLeaderReady returns true when the issue is assigned to a squad whose
// leader agent can accept work right now. Readiness criteria (archived,
// runtime bound, runtime online) are shared with the autopilot admission
// gate via service.AgentReadiness — both paths must move together or one
// will start enqueueing tasks the other refuses (MUL-2429 RFC §4.b B4).
func (h *Handler) isSquadLeaderReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return false
	}
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil {
		return false
	}
	ready, _, err := service.AgentReadiness(ctx, h.Queries, agent)
	if err != nil {
		// Fail closed when we can't tell — same posture as the rest of
		// this function (any error path returns false).
		return false
	}
	return ready
}

// enqueueSquadLeaderTask triggers the squad leader agent for an issue assigned
// to a squad. Assign and backlog-promotion paths use this directly; comment
// paths go through computeCommentAgentTriggers so preview and create share the
// same trigger set.
func (h *Handler) enqueueSquadLeaderTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, authorType, authorID string) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}

	if !h.canEnqueueSquadLeader(ctx, squad.LeaderID, authorType, authorID, uuidToString(issue.WorkspaceID)) {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, triggerCommentID); err != nil {
		slog.Warn("enqueue squad leader task failed",
			"issue_id", uuidToString(issue.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID),
			"error", err)
	}
}
