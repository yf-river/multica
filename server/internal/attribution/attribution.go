// Package attribution 实现 agent 任务运行的「可追责人类解析」契约（MUL-4302，
// "Human Attribution"）。每一条进入 agent_task_queue 的运行都必须能追溯到一个确切的
// 可追责人类，而且归因必须「可解释」：它不只记录是谁，还记录这个人类是在瀑布的哪一层
// 被解析出来的（直接成员动作、跨 agent 一跳的委托复制、评论来源链、autopilot 规则
// 所有者，或降级的 owner 兜底）。
//
// 本包拥有词汇表（Source、EvidenceKind、TriggerKind）与「纯」分类规则。收集事实的
// 数据库读取留在调用方（service.TaskService）；调用方把已取到的事实喂给 Classify*
// 系列函数，使规则保持无副作用、无需数据库即可完整单测。
//
// 硬性不变式（MUL-4302 §1.3）：归因是「代表某人(on behalf of)」，绝不是追责、也绝不
// 是授权。本包没有任何东西参与权限决策——它只给可见性、审计与成本打上来源标签。特别
// 地，这里盖章的 accountable-human 值镜像既有的 originator_user_id（由调用方计算）；
// 本包绝不会扩大或收窄那个人是谁。
package attribution

import "github.com/jackc/pgx/v5/pgtype"

// Source 是「解析出该运行可追责人类」的瀑布层级。原样存进
// agent_task_queue.originator_source。保持为自由字符串（无 DB CHECK），让新建模的
// 触发路径无需 schema 迁移即可引入新来源（MUL-4302 §7）。
type Source string

const (
	// SourceDirectHuman —— 成员自己的动作入队了运行（评论、@提及、指派、升职、手动
	// 触发、重跑）。该成员即可追责人类。
	SourceDirectHuman Source = "direct_human"
	// SourceDelegation —— 代表某人类运行的 agent 引起了入队（agent @别的 agent、
	// agent 建子 issue、阶段完成唤醒）。父任务的可追责人类被「复制」而不是「串联」，
	// 因此委托环保持无害（MUL-4302 §3.2）。
	SourceDelegation Source = "delegation"
	// SourceCommentSource —— issue 的常设 assignee 对 agent/系统写的评论做出了反应；
	// 人类经 comment.source_task_id 解析（委托的特例，MUL-4302 §3.3）。
	SourceCommentSource Source = "comment_source"
	// SourceTriggerOwner —— autopilot 的 schedule/webhook 触发器入队了运行；可追责
	// 人类是「创建了那个具体触发器」的成员（设了调度 / 注册了 webhook）。优先于
	// rule_owner：运行应对「武装了触发它的触发器」的人负责，而不是对「最后发布规则」
	// 的人负责（MUL-4302；Bohan 的细化）。originator 保持 NULL——自主触发不带人类
	// 授权——所以这与 rule_owner 一样是 authz 安全的、仅供审计的分叉。
	SourceTriggerOwner Source = "trigger_owner"
	// SourceRuleOwner —— autopilot 触发器入队了运行，但无法恢复其创建者（创建于
	// 「记录逐触发器创建者」之前）；可追责人类降级为该规则活跃版本的发布者
	// （MUL-4302 §3.4）。准确，但比 trigger_owner 粗。
	SourceRuleOwner Source = "rule_owner"
	// SourceOwnerFallback —— 上面一切都解析不到人类，归因降级到 agent owner。
	// 这是「降级」，不是合规级，必须被显著地区分出来（MUL-4302 §3.5）。
	SourceOwnerFallback Source = "owner_fallback"
	// SourceBackfill —— 由 backfill 命令事后补上的历史行；绝不应冒充实时归因。
	SourceBackfill Source = "backfill"
	// SourceUnattributed —— 解析不到人类、也没应用任何兜底。它区别于 NULL
	// （迁移前）来源：是显式的「我们查了，链上确实没有人」标记。
	SourceUnattributed Source = "unattributed"
)

// Precise 报告 src 是否是合规级（非降级）归因。owner_fallback、backfill、
// unattributed 都是降级的，会计入归因覆盖率健康指标（MUL-4302 §9）。
func (src Source) Precise() bool {
	switch src {
	case SourceDirectHuman, SourceDelegation, SourceCommentSource, SourceTriggerOwner, SourceRuleOwner:
		return true
	default:
		return false
	}
}

// String 返回原始来源标签（空值时回退成 unattributed），让调用方永远不会盖一个零值。
func (src Source) String() string {
	if src == "" {
		return string(SourceUnattributed)
	}
	return string(src)
}

// EvidenceKind 标记一次运行的直接成因，使每条归因都能跳到它的证据行。自由字符串，
// 与一个证据引用 id 配对。
type EvidenceKind string

const (
	EvidenceComment         EvidenceKind = "comment"
	EvidenceIssueAssignment EvidenceKind = "issue_assignment"
	EvidenceAutopilotRun    EvidenceKind = "autopilot_run"
	EvidenceRuleVersion     EvidenceKind = "rule_version"
	EvidenceRerun           EvidenceKind = "rerun"
	// EvidenceDelegatedFailure 指向「把控制权交还给来源协调器」的那个终态 worker 任务。
	EvidenceDelegatedFailure EvidenceKind = "delegated_failure"
	// EvidenceChat 让统一的证据对指向「触发运行的那个聊天会话」——它是
	// autopilot_run / issue_assignment 的聊天对等物。专门的 chat_session_id 列仍为
	// 自己的消费者保留；这里让归因 UI 的「跳到证据」路径保持统一（MUL-4302 §2）。
	EvidenceChat EvidenceKind = "chat"
)

// TriggerKind 枚举每一条可能入队运行的路径。刻意做成显式分类法，让「新增触发路径」成
// 为一个可见、慎重的改动，必须声明自己的归因规则（MUL-4302 §2 架构不变式：没有声明
// 归因的入队路径不允许存在）。
type TriggerKind string

const (
	KindMemberComment     TriggerKind = "member_comment"
	KindMemberMention     TriggerKind = "member_mention"
	KindMemberAssign      TriggerKind = "member_assign"
	KindAgentMention      TriggerKind = "agent_mention"
	KindAgentComment      TriggerKind = "agent_comment"
	KindSubIssueCreate    TriggerKind = "sub_issue_create"
	KindStageWakeup       TriggerKind = "stage_wakeup"
	KindQuickCreate       TriggerKind = "quick_create"
	KindChat              TriggerKind = "chat"
	KindAutopilotSchedule TriggerKind = "autopilot_schedule"
	KindAutopilotWebhook  TriggerKind = "autopilot_webhook"
	KindAutopilotManual   TriggerKind = "autopilot_manual"
	KindRetry             TriggerKind = "retry"
	KindRerun             TriggerKind = "rerun"
	KindDeferredFallback  TriggerKind = "deferred_fallback"
)

// Result 是盖到一条已入队运行上的归因。
//
//   - UserID 是调用方写进 originator_user_id 的「授权」人类。canInvokeAgent 与
//     Composio overlay 读的就是它；没有人类授权运行时为合法无效（NULL）。
//   - AccountableUserID 是写进 accountable_user_id 的「审计」人类。单向不变式
//     （由 finalizeAttribution 强制）：UserID 有效时 AccountableUserID 等于它；
//     UserID 为 NULL（无人类授权该运行）时两者可不同——rule_owner 点名规则发布者、
//     owner_fallback 点名 agent owner 作为可追责人类，而授权一侧正确地保持为空。
//
// 其余字段是写进 Phase 1 provenance 列的审计元数据。请通过 ClassifyComment /
// ClassifyDirect / DirectHumanRun / Unattributed / RuleOwner 构造 Result，保证
// AccountableUserID 总被终态化；绝不要把手工拼的字面量盖到队列上。
type Result struct {
	UserID              pgtype.UUID
	AccountableUserID   pgtype.UUID
	Source              Source
	DelegatedFromTaskID pgtype.UUID
	RuleVersionID       pgtype.UUID
	RetryOfTaskID       pgtype.UUID
	RerunOfTaskID       pgtype.UUID
	EvidenceKind        EvidenceKind
	EvidenceRefID       pgtype.UUID
}

// finalizeAttribution 强制 Phase 1 的单向问责不变式（MUL-4302 §11）：解析出的
// originator 就是可追责人类，即
// `originator_user_id IS NOT NULL ⟹ accountable_user_id = originator_user_id`。
// 每条 Result 都流经这里。UserID 有效时把 UserID 镜像到 AccountableUserID；UserID
// 为 NULL（无人类授权运行）时，保持调用方设置的 AccountableUserID 原样——这正是
// rule_owner（规则发布者）与 owner_fallback（agent owner）为审计点名一个可追责人类、
// 而授权一侧正确为空的那唯一分叉点。入队调用点从不特判这一逻辑。
func finalizeAttribution(r Result) Result {
	if r.UserID.Valid {
		r.AccountableUserID = r.UserID
	}
	return r
}

// CommentFacts 是关于触发评论的「已取到」事实，由调用方从 DB 读取后传入，分类保持纯。
type CommentFacts struct {
	CommentID  pgtype.UUID
	AuthorType string // "member" | "agent" | other
	AuthorID   pgtype.UUID

	// 对 agent 写的评论：评论是从哪个源任务写的（comment.source_task_id），以及那个
	// 任务的 originator_user_id。调用方通过加载源任务解析 ParentOriginator；源任务缺失
	// 或它本身未被归因时保持无效。
	SourceTaskID     pgtype.UUID
	ParentOriginator pgtype.UUID

	// ParentAccountable 是源任务的 accountable_user_id（MUL-4302 §3.2）。它让一条
	// autopilot 为根的委托链——父任务没有授权人类（ParentOriginator NULL），但对某人
	// 可追责（触发器创建者 / 规则发布者）——能把那个负责人复制下来沿着委托走，而不是
	// 把链根掉到 unattributed。由调用方与 ParentOriginator 一起加载；父任务同样没有
	// 可追责人类时无效。
	ParentAccountable pgtype.UUID
}

// ClassifyComment 从已取到的评论事实解析一次「评论触发」运行的归因。
// agentAuthoredSource 选择触发评论为 agent 所写时用的标签：issue-assignee-反应路径用
// SourceCommentSource，显式 @提及 / 线程父 / squad 队长路径用 SourceDelegation。返回的
// UserID 与旧版 originator 解析逐字节一致，因此授权行为不变。
func ClassifyComment(f CommentFacts, agentAuthoredSource Source) Result {
	switch f.AuthorType {
	case "member":
		return finalizeAttribution(Result{
			UserID:        f.AuthorID,
			Source:        SourceDirectHuman,
			EvidenceKind:  EvidenceComment,
			EvidenceRefID: f.CommentID,
		})
	case "agent":
		r := Result{EvidenceKind: EvidenceComment, EvidenceRefID: f.CommentID}
		if !f.SourceTaskID.Valid {
			// agent 评论但没有源任务：无法沿链回溯。
			r.Source = SourceUnattributed
			return finalizeAttribution(r)
		}
		r.DelegatedFromTaskID = f.SourceTaskID
		if f.ParentOriginator.Valid {
			r.UserID = f.ParentOriginator
			r.Source = agentAuthoredSource
		} else if f.ParentAccountable.Valid {
			// 父任务没有授权人类（autopilot 为根的链：originator 为 NULL，
			// accountable = 触发器创建者 / 规则发布者），但对某人可追责。把那个
			// accountable 复制下来，让责任链根在任意深度都稳定（MUL-4302 §3.2）；
			// originator 保持 NULL 让授权不变，fail-closed workspace 也不会拒绝一个
			// 拥有精确负责人的扇出。
			r.AccountableUserID = f.ParentAccountable
			r.Source = agentAuthoredSource
		} else {
			// 源任务存在，但它自己链顶没有人类。
			r.Source = SourceUnattributed
		}
		return finalizeAttribution(r)
	default:
		return finalizeAttribution(Result{Source: SourceUnattributed, EvidenceKind: EvidenceComment, EvidenceRefID: f.CommentID})
	}
}

// DirectFacts 是一次「没有触发评论」运行的事实：直接的 issue 指派/创建，或带
// quick-create 来源的 agent 创建 issue。
type DirectFacts struct {
	IssueID     pgtype.UUID
	CreatorType string
	CreatorID   pgtype.UUID

	// ActorUserID 是「执行了让本次运行入队的那个动作」的成员（指派了 issue、升职了
	// backlog 子项、带 assignee 创建）。有效时它就是可追责人类（MUL-4302 §4，
	// "执行 assign / promote 的成员"），并优先于 issue 创建者：动手的人，而不是恰好
	// 建了 issue 的人，才是责任方。非 actor 路径（评论链、rerun、autopilot）在别处
	// 解析人类、并回退到创建者，故此处保持无效。因为直接动作是人类「授予权威」，actor
	// 同时成为 originator（授权）与 accountable——finalizeAttribution 让它们相等，
	// 遵守不变式。
	ActorUserID pgtype.UUID

	// OriginType / OriginTaskID 描述一条 agent 创建 issue 的来源
	// （"quick_create" 或 "agent_create"）；OriginOriginator 是该来源任务的
	// originator_user_id，由调用方加载。OriginType 为空表示没有来源。
	OriginType       string
	OriginTaskID     pgtype.UUID
	OriginOriginator pgtype.UUID

	// OriginAccountable 是来源任务的 accountable_user_id——DirectFacts 对
	// CommentFacts.ParentAccountable 的对等物（MUL-4302 §3.2）。agent 创建的子 issue
	// 若其来源任务是 autopilot 为根（OriginOriginator NULL、accountable 有值），会经
	// 委托继承那个 accountable，而不是掉到 unattributed。
	OriginAccountable pgtype.UUID
}

// ClassifyDirect 解析「没有触发评论」运行的归因。
func ClassifyDirect(f DirectFacts) Result {
	// 直接指派/升职 issue 的成员是可追责人类，先于 issue 创建者（MUL-4302 §4）。
	// 证据指向动作针对的那个 issue。
	if f.ActorUserID.Valid {
		return finalizeAttribution(Result{
			UserID:        f.ActorUserID,
			Source:        SourceDirectHuman,
			EvidenceKind:  EvidenceIssueAssignment,
			EvidenceRefID: f.IssueID,
		})
	}
	if f.CreatorType == "member" && f.CreatorID.Valid {
		return finalizeAttribution(Result{
			UserID:        f.CreatorID,
			Source:        SourceDirectHuman,
			EvidenceKind:  EvidenceIssueAssignment,
			EvidenceRefID: f.IssueID,
		})
	}
	switch f.OriginType {
	case "quick_create", "agent_create":
		r := Result{
			DelegatedFromTaskID: f.OriginTaskID,
			EvidenceKind:        EvidenceIssueAssignment,
			EvidenceRefID:       f.IssueID,
		}
		if f.OriginOriginator.Valid {
			r.UserID = f.OriginOriginator
			r.Source = SourceDelegation
		} else if f.OriginAccountable.Valid {
			// autopilot 为根的来源任务：没有授权人类，但对触发器创建者 / 规则发布者
			// 可追责。复制 accountable 下来，让链根保持稳定；originator 保持 NULL
			// （MUL-4302 §3.2）。
			r.AccountableUserID = f.OriginAccountable
			r.Source = SourceDelegation
		} else {
			r.Source = SourceUnattributed
		}
		return finalizeAttribution(r)
	default:
		return finalizeAttribution(Result{Source: SourceUnattributed, EvidenceKind: EvidenceIssueAssignment, EvidenceRefID: f.IssueID})
	}
}

// DirectHumanRun 为「成员通过一条不带 issue、不带触发评论的路径直接触发」的运行构造
// 归因——聊天消息或 quick-create 请求。userID 是动手的成员（聊天发送者 / quick-create
// 请求者），同时成为 originator 与 accountable。无效的 userID（例如无法解析发送者的
// Lark 群消息）产出显式的 unattributed 结果，而不是走 NULL-source 旁路。
func DirectHumanRun(userID pgtype.UUID, evidenceKind EvidenceKind, evidenceRefID pgtype.UUID) Result {
	if !userID.Valid {
		return finalizeAttribution(Result{Source: SourceUnattributed, EvidenceKind: evidenceKind, EvidenceRefID: evidenceRefID})
	}
	return finalizeAttribution(Result{
		UserID:        userID,
		Source:        SourceDirectHuman,
		EvidenceKind:  evidenceKind,
		EvidenceRefID: evidenceRefID,
	})
}

// Unattributed 为一条「当前没有可追责人类」的入队路径构造显式的「没解析到人」结果——
// 目前只有 autopilot 的 run_only 派发，其精确的 rule_owner 归因（accountable = 活跃
// 规则版本的发布者）将在稍后的 Phase 1 增量里随规则版本快照表落位。带真实证据盖
// SourceUnattributed，让该行避开 NULL-source 旁路，并区分「分类过了、没有人」与
// 迁移前的 NULL；同时 originator/accountable 保持 NULL，让授权仍正确地说「没有人类
// 授权这次运行」。
func Unattributed(evidenceKind EvidenceKind, evidenceRefID pgtype.UUID) Result {
	return finalizeAttribution(Result{Source: SourceUnattributed, EvidenceKind: evidenceKind, EvidenceRefID: evidenceRefID})
}

// RuleOwner 为 autopilot 触发的运行构造归因（MUL-4302 §3.4）。没有人类授权它，所以
// UserID（originator，授权值）保持 NULL，canInvokeAgent / Composio overlay 继续看到
// "no human"；审计侧可追责人类是 publisherUserID——发布该规则活跃版本的成员——这正是
// 双列拆分要表达的差异（accountable 有值、originator 为 NULL）。ruleVersionID 记录
// 由哪个快照解析；证据引用由调用方提供（run_only 用 autopilot_run，create_issue 用
// issue）。发布者缺失（系统发布规则，或还没解析到版本）时降级到 unattributed——我们
// 绝不捏造一个人。
func RuleOwner(publisherUserID, ruleVersionID pgtype.UUID, evidenceKind EvidenceKind, evidenceRefID pgtype.UUID) Result {
	r := Result{
		RuleVersionID: ruleVersionID,
		EvidenceKind:  evidenceKind,
		EvidenceRefID: evidenceRefID,
	}
	if publisherUserID.Valid {
		r.Source = SourceRuleOwner
		r.AccountableUserID = publisherUserID
	} else {
		r.Source = SourceUnattributed
	}
	return finalizeAttribution(r)
}

// TriggerOwner 为「键控到创建了触发触发器的那个成员」的 autopilot schedule/webhook
// 运行构造归因（MUL-4302；Bohan 的细化）。与 RuleOwner 一样，没有人类授权运行，所以
// UserID（originator）保持 NULL，只设审计侧——即触发器的成员创建者 creatorUserID。
// 证据由调用方提供（run_only 用 autopilot_run，create_issue 用 issue）。创建者无效时
// 降级到 unattributed，让失去创建者的调用方回退到 rule_owner 而不是捏造一个人。
func TriggerOwner(creatorUserID pgtype.UUID, evidenceKind EvidenceKind, evidenceRefID pgtype.UUID) Result {
	r := Result{
		EvidenceKind:  evidenceKind,
		EvidenceRefID: evidenceRefID,
	}
	if creatorUserID.Valid {
		r.Source = SourceTriggerOwner
		r.AccountableUserID = creatorUserID
	} else {
		r.Source = SourceUnattributed
	}
	return finalizeAttribution(r)
}

// SubscriptionFacts 是关于一条 agent 创建 issue 的「已取到」事实，用来决定谁继承对它的
// 可见性（MUL-5483）。与 Classify* 输入同款契约：调用方做 DB 读取，规则保持纯。
type SubscriptionFacts struct {
	// CreatorType 是 issue 自己的 creator_type。只有 agent 创建的 issue 才可能携带
	// 委托订阅；成员创建的 issue 已通过普通的 'creator' 规则让人类订阅了。
	CreatorType string

	// OriginType / OriginOriginator 镜像 DirectFacts：issue 的来源印记与来源任务的
	// originator_user_id，由调用方加载。
	OriginType       string
	OriginOriginator pgtype.UUID
}

// DelegatedSubscriber 解析「应被自动订阅到某条 agent 创建 issue 的人类」以及要记录的
// 原因标签。
//
// 它刻意镜像 ClassifyDirect 的来源分支，而不是另造第二个「这代表谁」的概念——本次修复
// 的整个缺陷正是归因与通知在这一点上不一致（MUL-5483）。瀑布比 ClassifyDirect 更窄，
// 是有意的：
//
//   - OriginOriginator 有效 → 订阅。人类授权了这条链，并且因为归因「复制」可追责人类
//     跨过每一次 agent 跳（而不是串联它，见 SourceDelegation），所以在任意深度都能解析
//     出「最初的」人类。无深度上限：深度恰恰是信号丢失最伤人的地方，约束噪音的是投递
//     层级而不是上限。
//
//   - quick_create → 原因 'creator'，agent_create → 原因 'delegated'。quick-create
//     人类是按名字要了那条 issue 的人，属于直接意图、保留完整通知；agent_create 子项
//     是 agent 在更宽授权下自己的决定，因此走降低一档的 delegated 层级。
//
//   - 其它一切 → 不订阅。尤其 origin_type='autopilot' 被排除：autopilot 已有显式配置
//     的 autopilot_subscriber 模板，其 issue 的目标受众是那个列表——而不是碰巧武装了
//     触发器的成员。降级归因（owner_fallback / unattributed）也因同样的理由被排除：
//     我们不会为了通知而捏造一个人。
func DelegatedSubscriber(f SubscriptionFacts) (pgtype.UUID, string, bool) {
	if f.CreatorType != "agent" || !f.OriginOriginator.Valid {
		return pgtype.UUID{}, "", false
	}
	switch f.OriginType {
	case "quick_create":
		return f.OriginOriginator, "creator", true
	case "agent_create":
		return f.OriginOriginator, "delegated", true
	default:
		return pgtype.UUID{}, "", false
	}
}

// OwnerFallback 把一条 UNATTRIBUTED 结果降级成 owner_fallback（MUL-4302 §3.5）：
// agent owner 成为可追责人类，让任何运行都不至于没有可追责人——但这是「降级」标签
// （Source.Precise() == false），报告里必须显著呈现。它只用于审计——originator
// （UserID）保持 NULL，授权不受影响。仅当「解析出的来源是 unattributed 且 workspace
// 未选择 fail-closed」时才在入队边界应用；精确结果或无效 ownerUserID（没有可兜底对象）
// 原样穿透——绝不捏造人类。
func OwnerFallback(r Result, ownerUserID pgtype.UUID) Result {
	if r.Source != SourceUnattributed || !ownerUserID.Valid {
		return r
	}
	r.Source = SourceOwnerFallback
	r.AccountableUserID = ownerUserID
	return r
}
