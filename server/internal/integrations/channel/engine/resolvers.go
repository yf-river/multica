package engine

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：Router 跑入站流水线所依赖的"可插拔缝"的类型定义——每个平台要实现的
// resolver 接口、以及流水线各步传递的结果结构体。所有平台特有逻辑都藏在这些接口后面；
// 一个平台注册一个 ResolverSet，channel-agnostic 的 Router 保持不动。飞书实现是第一个
// ResolverSet。

// Outcome 分类 Router 决定对一条入站消息做什么。取值与旧 lark 的 outcome 一一对应，
// 行为与看板都能原样迁移。
type Outcome string

const (
	OutcomeDropped       Outcome = "dropped"
	OutcomeNeedsBinding  Outcome = "needs_binding"
	OutcomeIngested      Outcome = "ingested"
	OutcomeFreshPending  Outcome = "fresh_pending"
	OutcomeChatStarted   Outcome = "chat_started"
	OutcomeIssueUsage    Outcome = "issue_usage"
	OutcomeAgentOffline  Outcome = "agent_offline"
	OutcomeAgentArchived Outcome = "agent_archived"
)

// DropReason 枚举丢弃审计的分类。取值与旧 lark 的 drop reason 一一对应。
type DropReason string

const (
	DropReasonUnboundUser         DropReason = "unbound_user"
	DropReasonNonWorkspaceMember  DropReason = "non_workspace_member"
	DropReasonNotAddressedInGroup DropReason = "not_addressed_in_group"
	DropReasonDuplicate           DropReason = "duplicate"
	DropReasonRevokedInstallation DropReason = "revoked_installation"
	DropReasonInvalidEvent        DropReason = "invalid_event"
)

// Result 是 Router 对一条入站消息产出的类型化判定，出站侧（OutboundReplier / typing）
// 消费它。对应旧 lark.DispatchResult。
type Result struct {
	Outcome              Outcome
	DropReason           DropReason
	InstallationID       pgtype.UUID
	ChatSessionID        pgtype.UUID
	ChannelBindingID     pgtype.UUID
	ChannelRouteRevision int64
	// Sender 是平台原生发送者 id（如飞书 open_id），replier 据此把绑定提示定位回发送者。
	Sender          string
	IssueID         pgtype.UUID
	IssueNumber     int32
	IssueIdentifier string
	IssueTitle      string
	// IssueDuplicate 标记一条"因共享重复防护发现上面 IssueID 已存在的活跃 issue 而未新建"
	// 的 /issue 命令。Replier 把它当业务冲突渲染，绝不当作内部错误。
	IssueDuplicate bool
	// IssueUsageHadMedia 标记一条无标题 /issue，且其当前入站消息还带了可下载媒体。
	// Replier 用它提示发送者"修正命令时把那件媒体再带一次"。
	IssueUsageHadMedia bool
	// runScheduled 记录这次入库是否安排了正常聊天运行。属 Router 内部状态：
	// replier 必须继续用 Outcome 判断，别读它。
	runScheduled bool
}

// ResolvedInstallation 是路由完成后 Router 需要的渠道无关安装上下文。Platform 字段把
// 适配器自己的安装值不透明地捎带上，set 的其它端口（binder、replier、typing）复用它、
// 无需再查一次库；Router 从不读 Platform。
type ResolvedInstallation struct {
	ID              pgtype.UUID
	WorkspaceID     pgtype.UUID
	AgentID         pgtype.UUID
	InstallerUserID pgtype.UUID
	Active          bool
	Platform        any
}

// ResolvedIdentity 是发送者映射到某个 Multica 用户后的结果。
type ResolvedIdentity struct {
	UserID pgtype.UUID
}

// EnsureSessionParams 携带 SessionBinder.EnsureSession 的入参。Sender 是解析出来的会话
// 创建者（私聊是唯一那个人，群聊是安装者——Router 决定是谁并传进来）。
type EnsureSessionParams struct {
	Installation ResolvedInstallation
	Sender       pgtype.UUID
	Message      channel.InboundMessage
}

// StartSessionParams 携带一次 /new 路由轮换。Creator 拥有新 Chat；Sender 是发出命令并
// 发起其首段上下文的已认证用户。两者在群聊里不同：安装者拥有 Chat，群成员仍是这轮对话
// 的发起人。PersistMessage 只在"裸控制命令"时为 false；带媒体的 /new 是真正首轮对话。
type StartSessionParams struct {
	Installation           ResolvedInstallation
	Creator                pgtype.UUID
	Sender                 pgtype.UUID
	Message                channel.InboundMessage
	ClaimToken             pgtype.UUID
	MediaPendingSeconds    float64
	PersistMessage         bool
	HistoryBoundaryPending bool
	BeforeCommit           func(context.Context, pgx.Tx, db.ChatSession) error
}

type StartSessionResult struct {
	SessionID     pgtype.UUID
	BindingID     pgtype.UUID
	RouteRevision int64
	Append        AppendResult
}

// AppendParams 携带 SessionBinder.AppendMessage 的入参。ClaimToken 是去重的属主围栏
// token；binder 在它自己的 chat_message+session 事务**内部**执行去重 Mark，让持久化
// 写入与 Mark 一起原子提交。MediaPendingSeconds 持久化占位符的兜底预算；追加事务把它
// 转成 DB 时钟上的截止时刻（now() + budget），所有基于 now() 的消费者读到的是同一个
// 写它的时钟。
type AppendParams struct {
	SessionID           pgtype.UUID
	Sender              pgtype.UUID
	InstallationID      pgtype.UUID
	Message             channel.InboundMessage
	ClaimToken          pgtype.UUID
	MediaPendingSeconds float64
}

// AppendResult 报告 AppendMessage 做了什么决定。
type AppendResult struct {
	// MessageID 是 AppendMessage 创建的持久化 chat_message 行。解耦的媒体处理在连接器
	// ACK 路径完成后凭它挂附件。
	MessageID pgtype.UUID
	// IssueCommand 在消息是 /issue 命令时非 nil。
	IssueCommand *IssueCommand
	// DedupMarked 为 true 表示 AppendMessage 在它自己的事务里完结了去重认领；
	// Router 随即跳过流水线结束时的 finalize。
	DedupMarked bool
	// ContextRevision 是分配给这条消息的、agent 可见的持久化上下文代。去抖器键和任务
	// 快照用它当边界。
	ContextRevision int64
	// PendingContexts 包含每一个"有持久化未认领输入 + 其已认证发送者快照"的代。进程
	// 崩溃后用它重挂去抖窗口，且不会借后一代码的发送者身份。
	PendingContexts []PendingContext
	InitialTitle    string
	// BecameVisible 仅当这条已提交的普通轮次把一条隐式渠道会话从"隐藏编排状态"变成
	// "公开 Chat"时才为 true。
	BecameVisible bool
	BindingID     pgtype.UUID
	RouteRevision int64
}

// PendingContext 标识一份需要运行定时器的"持久化未认领输入"。发起者只有在遗留或损坏
// 数据里才会缺失；对这种较老代，恢复采用"失败即关闭"，绝不冒充新发送者。
type PendingContext struct {
	Revision        int64
	InitiatorUserID pgtype.UUID
}

// ChannelChatLifecycle 把已提交的渠道 Chat 变化桥接给服务端既有的实时与尽力而为标题
// 服务，engine 包因此不直接依赖 HTTP handler。
type ChannelChatLifecycle interface {
	ChannelChatStarted(event ChannelChatStartedEvent)
	ChannelChatTitleInitialized(workspaceID, creatorID, sessionID pgtype.UUID, title string)
	GenerateChannelChatTitle(workspaceID, creatorID, sessionID pgtype.UUID, currentTitle, sourceText string)
}

// ChannelChatStartedEvent 携带足够多的已提交元数据，客户端据此新增/失效一条渠道创建的
// Chat，而不需要改动导航逻辑。
type ChannelChatStartedEvent struct {
	WorkspaceID    pgtype.UUID
	CreatorID      pgtype.UUID
	AgentID        pgtype.UUID
	SessionID      pgtype.UUID
	InstallationID pgtype.UUID
	ChannelType    channel.Type
	RouteRevision  int64
	Title          string
}

// BindMediaParams 把已存储的媒体引用带给"追加后"的附件事务。MessageID 是持久化
// chat_message——binder 清的是它的待处理占位标记。IssueID 为 /issue 轮次选定 issue
// 归属；否则引用绑到 MessageID。IssueDescriptionBase 只在该轮创建 issue 时有效，让
// binder 仅在"描述尚未被别人编辑过"的前提下替换行内占位符。媒体下载**绝不能**在
// 这个事务里执行。
type BindMediaParams struct {
	MessageID            pgtype.UUID
	SessionID            pgtype.UUID
	WorkspaceID          pgtype.UUID
	Sender               pgtype.UUID
	IssueID              pgtype.UUID
	IssueDescriptionBase pgtype.Text
	IssueCommandText     string
	Body                 string
	MediaRefs            []channel.MediaRef
}

// BindMediaResult 报告"标题只在首个媒体附件提交后才初始化"。Router 据此发布实时状态、
// 并跑与文本优先 Chat 相同的尽力而为 LLM/CAS 标题流程。
type BindMediaResult struct {
	InitialTitle string
	TitleSource  string
}

// IssueCommand 是解析出来的 /issue 命令。
type IssueCommand struct {
	Title       string
	Description string
}

// resolvers 返回的哨兵错误：Router 靠它们映射到正确的产品 outcome，而不是当成基础设施
// 故障。
var (
	// ErrInstallationNotFound：没有安装匹配消息的路由键 → invalid_event 丢弃。
	ErrInstallationNotFound = errors.New("engine: installation not found")
	// ErrSenderUnbound：发送者没有身份绑定 → needs_binding。
	ErrSenderUnbound = errors.New("engine: sender unbound")
	// ErrSenderNotMember：发送者有绑定但不是工作区成员 → non_workspace_member 丢弃。
	ErrSenderNotMember = errors.New("engine: sender not a workspace member")
	// ErrRouteChanged 请 Router 重新解析平台路由、在写任何持久化之前用**同一条已认领**
	// 消息重试。
	ErrRouteChanged = errors.New("engine: route changed")
	// ErrDuplicate：Claim 发现消息已处理/正在处理 → duplicate 丢弃。
	ErrDuplicate = errors.New("engine: duplicate message")
	// ErrClaimLost：处理途中一次并发回收把去重 token 轮换掉了 → 按重复处理。
	ErrClaimLost = errors.New("engine: dedup claim lost")
)

// InstallationResolver 把一条入站消息路由到它的安装。适配器从消息（Source 或 Raw）里
// 读自己需要的平台路由键。没有匹配的返回 ErrInstallationNotFound；存在但已吊销的返回
// Active=false 的 ResolvedInstallation。
type InstallationResolver interface {
	ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (ResolvedInstallation, error)
}

// IdentityResolver 在安装范围内把消息发送者映射成 Multica 用户，并复查工作区成员资格。
// 产品语义场景返回 ErrSenderUnbound 或 ErrSenderNotMember。
type IdentityResolver interface {
	ResolveSender(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage) (ResolvedIdentity, error)
}

// Deduper 是两阶段幂等缝。Claim 铸造一个属主围栏 token（已处理/处理中返回
// ErrDuplicate）；Mark/Release 都以 token 为围栏（token 不匹配时空操作、不算错误）。
type Deduper interface {
	Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (claimToken pgtype.UUID, err error)
	Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error
	Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error
}

// SessionBinder 保证 chat_session 存在并追加消息（事务内完成去重 Mark）。
// AppendMessage 在 token 中途被轮换时返回 ErrClaimLost。
type SessionBinder interface {
	EnsureSession(ctx context.Context, p EnsureSessionParams) (pgtype.UUID, error)
	StartSession(ctx context.Context, p StartSessionParams) (StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendMessage(ctx context.Context, p AppendParams) (AppendResult, error)
	BindMedia(ctx context.Context, p BindMediaParams) (BindMediaResult, error)
}

// MediaResolver 在"用户消息与去重 Mark 已持久化"之后解析平台媒体。Router 在连接器
// ACK 路径之外跑它并绑定返回的 MediaRefs；独立调度的任务保持延迟，直到绑定完成或持久化
// 的截止时刻过期。实现是尽力而为的：失败只留存储中的占位文本原地不动，**绝不**内联删
// 任何东西——每个已上传对象都由 PUT 之前写入的意图台账行（见 MediaIntentLedger）覆盖，
// 异步对账器收尾处理没被绑定的部分。
type MediaResolver interface {
	// HasMedia 判断 msg 是否引用 ResolveMedia 会去抓的平台媒体。Router 在连接器 ACK
	// 路径上同步调它，决定要不要持久化媒体截止时刻、排队解析任务——所以实现必须是不做
	// I/O 的纯内存检查。false 让消息走普通入库路径：无占位标记、无延迟运行、不占
	// 信号量槽。
	HasMedia(msg channel.InboundMessage) bool
	// ResolveMedia 下载平台媒体并上传到对象存储。chatMessageID 是拥有"待处理意图"的
	// 持久化 chat_message；由 Router 决定返回的引用属于那条消息、还是属于同一轮创建的
	// issue。
	ResolveMedia(ctx context.Context, inst ResolvedInstallation, sender ResolvedIdentity, sessionID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage
}

// MediaIntentLedger 在对象被写入**之前**持久化上传意图。该行是任何失败路径唯一留下的
// 痕迹：上传报错、解析超时、绑定失败、提交歧义、或崩溃，都只是把它留给对账器——对账器
// 在途中的 PUT 或 COMMIT 早已不可能落地之后才会结算它。这让"我的副作用到底发生没有？"
// 变成一道没人需要当场回答的问题。
type MediaIntentLedger interface {
	// RecordPendingMediaObject upsert 意图行。ok=false 表示该键已离开 'pending'
	//（对账器接管了它）——调用方必须整体跳过上传，绝不能复活这行。
	RecordPendingMediaObject(ctx context.Context, p RecordPendingMediaObjectParams) (ok bool, err error)
}

// RecordPendingMediaObjectParams 标识一个打算上传的对象。StorageURL 是附件行将携带的
// URL（键的纯函数），对账器据此检查是否存在持久化引用。InstallationID 仅作运维诊断。
type RecordPendingMediaObjectParams struct {
	StorageKey     string
	WorkspaceID    pgtype.UUID
	ChatMessageID  pgtype.UUID
	StorageURL     string
	InstallationID pgtype.UUID
}

// NewDBMediaIntentLedger 把 *db.Queries 适配成 MediaIntentLedger。
func NewDBMediaIntentLedger(q *db.Queries) MediaIntentLedger {
	return dbMediaIntentLedger{q: q}
}

type dbMediaIntentLedger struct{ q *db.Queries }

func (l dbMediaIntentLedger) RecordPendingMediaObject(ctx context.Context, p RecordPendingMediaObjectParams) (bool, error) {
	_, err := l.q.RecordChannelMediaPendingObject(ctx, db.RecordChannelMediaPendingObjectParams{
		StorageKey:     p.StorageKey,
		WorkspaceID:    p.WorkspaceID,
		ChatMessageID:  p.ChatMessageID,
		StorageUrl:     p.StorageURL,
		InstallationID: p.InstallationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 状态守卫的 upsert 撞到了 'deleting' 行：该键已归对账器所有，绝不能复活。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Auditor 记录一条被丢弃的入站事件（无消息正文——丢弃审计策略）。没有安装的事件，
// instID 可以是零 UUID。
type Auditor interface {
	RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason DropReason) error
}

// OutboundReplier 投递判定驱动的回复（绑定提示、离线/归档提示、/issue 确认）。可选；
// nil 表示关闭出站回复。由 Router 在 ACK 关键路径之外驱动。
type OutboundReplier interface {
	Reply(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage, res Result)
}

// TypingNotifier 在消息被收入时显示"处理中"指示器，消息到达终态时清掉。可选；nil 关闭。
type TypingNotifier interface {
	// OnIngested 为一条成功入库的消息亮起指示器。
	OnIngested(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID)
	// OnSettled 为"运行触发没产生任何任务"的会话熄灭指示器（agent 离线/归档、或入队
	// 失败）。那种情况下不会有任何任务生命周期事件发布，平台依赖总线驱动的清除
	//（chat-done / task-failed）永远不会触发，指示器会卡住。Router 在去抖 flush 里调
	// 这个方法。幂等：没有指示器的会话是空操作。
	OnSettled(ctx context.Context, sessionID pgtype.UUID)
}

// ResolverSet 是 Router 跑流水线用的"每平台一包"。Installation/Identity/Dedup/Session/
// Audit 必填；Replier/Typing 可选。OriginType 是该渠道 /issue 命令写入的
// issue.origin_type 标签（飞书："lark_chat"）。
type ResolverSet struct {
	Installation InstallationResolver
	Identity     IdentityResolver
	Dedup        Deduper
	Session      SessionBinder
	Media        MediaResolver
	Audit        Auditor
	Replier      OutboundReplier
	Typing       TypingNotifier
	OriginType   string
}

// IssueCreator 是 Router 处理 /issue 命令所需的 service.IssueService 窄子集。各平台共享。
type IssueCreator interface {
	Create(ctx context.Context, p service.IssueCreateParams, opts service.IssueCreateOpts) (service.IssueCreateResult, error)
	PublishAttachmentsChanged(ctx context.Context, issue db.Issue, actorID pgtype.UUID)
}

// TaskEnqueuer 是 Router 触发聊天运行所需的 service.TaskService 窄子集。各平台共享。
type TaskEnqueuer interface {
	EnqueueChannelChatTask(ctx context.Context, session db.ChatSession, initiatorUserID pgtype.UUID, forceFreshSession bool, contextRevision int64, bindingID pgtype.UUID, routeRevision int64) (db.AgentTaskQueue, error)
	PrepareChatTaskEnqueue(ctx context.Context, agentID, initiatorUserID pgtype.UUID) (service.PreparedChatTaskEnqueue, error)
	EnqueuePreparedChannelChatTaskInTx(ctx context.Context, tx pgx.Tx, session db.ChatSession, initiatorUserID pgtype.UUID, forceFreshSession bool, contextRevision int64, prepared service.PreparedChatTaskEnqueue) (db.AgentTaskQueue, error)
	FinalizeChatTaskEnqueue(ctx context.Context, task db.AgentTaskQueue)
	PromoteChannelChatTasksIfMediaReady(ctx context.Context, sessionID pgtype.UUID) error
	PromoteDeferredChannelIssueTask(ctx context.Context, taskID pgtype.UUID) error
}

// SessionReader 读"去抖 flush + /issue 标识"需要的行。各平台共享；由 *db.Queries
// （渠道后台的 store）实现。
type SessionReader interface {
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
}
