package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/channelmedia"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 角色定位：所有 IM 适配器复用的、共享的渠道无关聊天会话服务（MUL-3516）。它从飞书
// 专属的 lark.chatSessionService 里抽出来，这样新增一个 IM 就再也不需要重写会话/追加/
// `/issue` 那套机制——平台适配器只贡献：channel_type、会话标题、以及（因为增强是平台
// 特异的）命令解析源。逻辑本身（找或建会话 + 绑定、追加消息 + touch + 回复目标 +
// 事务内去重 Mark、`/issue` 解析）跨平台完全一致，并把 channel_type 判别值一路带过
// 通用 channel_* 表。

const pgSQLStateUniqueViolation = "23505"

// TxStarter 抽象事务创建。*pgxpool.Pool 满足它。收在 engine 本地，集成层就不用反向
// 依赖 internal/service。
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SessionQueries 是 ChatSession 服务需要的生成查询的窄切片。*db.Queries 经
// dbSessionQueries 适配器满足它（其 WithTx 返回接口类型）；测试注入内存假实现。
type SessionQueries interface {
	WithTx(tx pgx.Tx) SessionQueries
	GetChannelChatSessionBinding(ctx context.Context, arg db.GetChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error)
	LockWorkspaceForChatSessionCreate(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error)
	CreateChatSession(ctx context.Context, arg db.CreateChatSessionParams) (db.ChatSession, error)
	CreateChannelChatSessionBinding(ctx context.Context, arg db.CreateChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error)
	CreateChannelChatSessionBindingGeneration(ctx context.Context, arg db.CreateChannelChatSessionBindingGenerationParams) (db.ChannelChatSessionBinding, error)
	LockCurrentChannelChatSessionBinding(ctx context.Context, arg db.LockCurrentChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error)
	LockCurrentChannelChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (db.ChannelChatSessionBinding, error)
	RetireChannelChatSessionBinding(ctx context.Context, arg db.RetireChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error)
	LockChatSessionForAppend(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	ChatSessionHasPublicUserMessage(ctx context.Context, id pgtype.UUID) (bool, error)
	MarkChatSessionExplicitlyCreated(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	InitializeChatSessionTitle(ctx context.Context, arg db.InitializeChatSessionTitleParams) (db.ChatSession, error)
	ReplaceImplicitChatSessionTitle(ctx context.Context, arg db.ReplaceImplicitChatSessionTitleParams) (db.ChatSession, error)
	InitializeChatSessionMediaTitle(ctx context.Context, arg db.InitializeChatSessionMediaTitleParams) (db.ChatSession, error)
	CreateChatMessage(ctx context.Context, arg db.CreateChatMessageParams) (db.ChatMessage, error)
	ListUnownedChannelChatContextRevisions(ctx context.Context, chatSessionID pgtype.UUID) ([]PendingContext, error)
	ClearChatMessageChannelMediaPending(ctx context.Context, arg db.ClearChatMessageChannelMediaPendingParams) error
	LockIssueForChannelMediaBind(ctx context.Context, arg db.LockIssueForChannelMediaBindParams) (pgtype.UUID, error)
	UpdateChatMessageContentForChannelMedia(ctx context.Context, arg db.UpdateChatMessageContentForChannelMediaParams) (int64, error)
	MaterializeIssueChannelMediaMarkdown(ctx context.Context, arg db.MaterializeIssueChannelMediaMarkdownParams) (db.Issue, error)
	CreateAttachment(ctx context.Context, arg db.CreateAttachmentParams) (db.Attachment, error)
	LinkAttachmentsToChatMessage(ctx context.Context, arg db.LinkAttachmentsToChatMessageParams) ([]pgtype.UUID, error)
	ClaimChannelMediaPendingObjectsForBind(ctx context.Context, arg db.ClaimChannelMediaPendingObjectsForBindParams) ([]string, error)
	TouchChatSession(ctx context.Context, id pgtype.UUID) error
	LockChannelChatSessionBindingForContext(ctx context.Context, chatSessionID pgtype.UUID) (db.ChannelChatSessionBinding, error)
	LockChannelChatContextGenerationByRevision(ctx context.Context, arg db.LockChannelChatContextGenerationByRevisionParams) (db.ChannelChatContextGeneration, error)
	AdvanceChannelChatContextGeneration(ctx context.Context, arg db.AdvanceChannelChatContextGenerationParams) (db.AdvanceChannelChatContextGenerationRow, error)
	ResolveChannelChatContextHistoryStart(ctx context.Context, arg db.ResolveChannelChatContextHistoryStartParams) error
	SetChannelChatContextInitiator(ctx context.Context, arg db.SetChannelChatContextInitiatorParams) (pgtype.UUID, error)
	UpdateChannelChatSessionBindingReplyTarget(ctx context.Context, arg db.UpdateChannelChatSessionBindingReplyTargetParams) error
	MarkChannelInboundDedupProcessed(ctx context.Context, arg db.MarkChannelInboundDedupProcessedParams) (int64, error)
}

// dbSessionQueries 把 *db.Queries 适配成 SessionQueries——唯一目的是给 WithTx 一个接口
// 返回类型，让事务路径始终留在 SessionQueries 后面（测试可整体替换）。
type dbSessionQueries struct{ q *db.Queries }

func (a dbSessionQueries) WithTx(tx pgx.Tx) SessionQueries {
	return dbSessionQueries{q: a.q.WithTx(tx)}
}
func (a dbSessionQueries) GetChannelChatSessionBinding(ctx context.Context, arg db.GetChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error) {
	return a.q.GetChannelChatSessionBinding(ctx, arg)
}
func (a dbSessionQueries) LockWorkspaceForChatSessionCreate(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return a.q.LockWorkspaceForChatSessionCreate(ctx, id)
}
func (a dbSessionQueries) CreateChatSession(ctx context.Context, arg db.CreateChatSessionParams) (db.ChatSession, error) {
	return a.q.CreateChatSession(ctx, arg)
}
func (a dbSessionQueries) CreateChannelChatSessionBinding(ctx context.Context, arg db.CreateChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error) {
	row, err := a.q.CreateChannelChatSessionBinding(ctx, arg)
	return db.ChannelChatSessionBinding{
		ID: row.ID, ChatSessionID: row.ChatSessionID, InstallationID: row.InstallationID,
		ChannelType: row.ChannelType, ChannelChatID: row.ChannelChatID, ChatType: row.ChatType,
		LastMessageID: row.LastMessageID, LastThreadID: row.LastThreadID, Config: row.Config,
		CreatedAt: row.CreatedAt, PendingFresh: row.PendingFresh, ContextRevision: row.ContextRevision,
	}, err
}
func (a dbSessionQueries) CreateChannelChatSessionBindingGeneration(ctx context.Context, arg db.CreateChannelChatSessionBindingGenerationParams) (db.ChannelChatSessionBinding, error) {
	row, err := a.q.CreateChannelChatSessionBindingGeneration(ctx, arg)
	return db.ChannelChatSessionBinding{
		ID: row.ID, ChatSessionID: row.ChatSessionID, InstallationID: row.InstallationID,
		ChannelType: row.ChannelType, ChannelChatID: row.ChannelChatID, ChatType: row.ChatType,
		LastMessageID: row.LastMessageID, LastThreadID: row.LastThreadID, Config: row.Config,
		CreatedAt: row.CreatedAt, PendingFresh: row.PendingFresh, ContextRevision: row.ContextRevision,
		RouteRevision: row.RouteRevision, RetiredAt: row.RetiredAt,
		HistoryStartMessageID: row.HistoryStartMessageID, HistoryEndMessageID: row.HistoryEndMessageID,
		HistoryBoundaryPending: row.HistoryBoundaryPending,
	}, err
}
func (a dbSessionQueries) LockCurrentChannelChatSessionBinding(ctx context.Context, arg db.LockCurrentChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error) {
	return a.q.LockCurrentChannelChatSessionBinding(ctx, arg)
}
func (a dbSessionQueries) LockCurrentChannelChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (db.ChannelChatSessionBinding, error) {
	return a.q.LockCurrentChannelChatSessionBindingBySession(ctx, chatSessionID)
}
func (a dbSessionQueries) RetireChannelChatSessionBinding(ctx context.Context, arg db.RetireChannelChatSessionBindingParams) (db.ChannelChatSessionBinding, error) {
	return a.q.RetireChannelChatSessionBinding(ctx, arg)
}
func (a dbSessionQueries) LockChatSessionForAppend(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return a.q.LockChatSessionForAppend(ctx, id)
}
func (a dbSessionQueries) GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error) {
	return a.q.GetChatSession(ctx, id)
}
func (a dbSessionQueries) ChatSessionHasPublicUserMessage(ctx context.Context, id pgtype.UUID) (bool, error) {
	return a.q.ChatSessionHasPublicUserMessage(ctx, id)
}
func (a dbSessionQueries) MarkChatSessionExplicitlyCreated(ctx context.Context, id pgtype.UUID) (db.ChatSession, error) {
	return a.q.MarkChatSessionExplicitlyCreated(ctx, id)
}
func (a dbSessionQueries) InitializeChatSessionTitle(ctx context.Context, arg db.InitializeChatSessionTitleParams) (db.ChatSession, error) {
	return a.q.InitializeChatSessionTitle(ctx, arg)
}
func (a dbSessionQueries) ReplaceImplicitChatSessionTitle(ctx context.Context, arg db.ReplaceImplicitChatSessionTitleParams) (db.ChatSession, error) {
	return a.q.ReplaceImplicitChatSessionTitle(ctx, arg)
}
func (a dbSessionQueries) InitializeChatSessionMediaTitle(ctx context.Context, arg db.InitializeChatSessionMediaTitleParams) (db.ChatSession, error) {
	return a.q.InitializeChatSessionMediaTitle(ctx, arg)
}
func (a dbSessionQueries) CreateChatMessage(ctx context.Context, arg db.CreateChatMessageParams) (db.ChatMessage, error) {
	return a.q.CreateChatMessage(ctx, arg)
}
func (a dbSessionQueries) ListUnownedChannelChatContextRevisions(ctx context.Context, chatSessionID pgtype.UUID) ([]PendingContext, error) {
	rows, err := a.q.ListUnownedChannelChatContextRevisions(ctx, chatSessionID)
	if err != nil {
		return nil, err
	}
	contexts := make([]PendingContext, 0, len(rows))
	for _, row := range rows {
		contexts = append(contexts, PendingContext{
			Revision:        row.ContextRevision,
			InitiatorUserID: row.InitiatorUserID,
		})
	}
	return contexts, nil
}
func (a dbSessionQueries) ClearChatMessageChannelMediaPending(ctx context.Context, arg db.ClearChatMessageChannelMediaPendingParams) error {
	return a.q.ClearChatMessageChannelMediaPending(ctx, arg)
}
func (a dbSessionQueries) LockIssueForChannelMediaBind(ctx context.Context, arg db.LockIssueForChannelMediaBindParams) (pgtype.UUID, error) {
	return a.q.LockIssueForChannelMediaBind(ctx, arg)
}
func (a dbSessionQueries) UpdateChatMessageContentForChannelMedia(ctx context.Context, arg db.UpdateChatMessageContentForChannelMediaParams) (int64, error) {
	return a.q.UpdateChatMessageContentForChannelMedia(ctx, arg)
}
func (a dbSessionQueries) MaterializeIssueChannelMediaMarkdown(ctx context.Context, arg db.MaterializeIssueChannelMediaMarkdownParams) (db.Issue, error) {
	return a.q.MaterializeIssueChannelMediaMarkdown(ctx, arg)
}
func (a dbSessionQueries) CreateAttachment(ctx context.Context, arg db.CreateAttachmentParams) (db.Attachment, error) {
	created, err := a.q.CreateAttachment(ctx, arg)
	return created.Attachment(), err
}
func (a dbSessionQueries) LinkAttachmentsToChatMessage(ctx context.Context, arg db.LinkAttachmentsToChatMessageParams) ([]pgtype.UUID, error) {
	return a.q.LinkAttachmentsToChatMessage(ctx, arg)
}
func (a dbSessionQueries) ClaimChannelMediaPendingObjectsForBind(ctx context.Context, arg db.ClaimChannelMediaPendingObjectsForBindParams) ([]string, error) {
	return a.q.ClaimChannelMediaPendingObjectsForBind(ctx, arg)
}
func (a dbSessionQueries) TouchChatSession(ctx context.Context, id pgtype.UUID) error {
	return a.q.TouchChatSession(ctx, id)
}
func (a dbSessionQueries) LockChannelChatSessionBindingForContext(ctx context.Context, chatSessionID pgtype.UUID) (db.ChannelChatSessionBinding, error) {
	return a.q.LockChannelChatSessionBindingForContext(ctx, chatSessionID)
}
func (a dbSessionQueries) LockChannelChatContextGenerationByRevision(ctx context.Context, arg db.LockChannelChatContextGenerationByRevisionParams) (db.ChannelChatContextGeneration, error) {
	return a.q.LockChannelChatContextGenerationByRevision(ctx, arg)
}
func (a dbSessionQueries) AdvanceChannelChatContextGeneration(ctx context.Context, arg db.AdvanceChannelChatContextGenerationParams) (db.AdvanceChannelChatContextGenerationRow, error) {
	return a.q.AdvanceChannelChatContextGeneration(ctx, arg)
}
func (a dbSessionQueries) ResolveChannelChatContextHistoryStart(ctx context.Context, arg db.ResolveChannelChatContextHistoryStartParams) error {
	return a.q.ResolveChannelChatContextHistoryStart(ctx, arg)
}
func (a dbSessionQueries) SetChannelChatContextInitiator(ctx context.Context, arg db.SetChannelChatContextInitiatorParams) (pgtype.UUID, error) {
	return a.q.SetChannelChatContextInitiator(ctx, arg)
}
func (a dbSessionQueries) UpdateChannelChatSessionBindingReplyTarget(ctx context.Context, arg db.UpdateChannelChatSessionBindingReplyTargetParams) error {
	return a.q.UpdateChannelChatSessionBindingReplyTarget(ctx, arg)
}
func (a dbSessionQueries) MarkChannelInboundDedupProcessed(ctx context.Context, arg db.MarkChannelInboundDedupProcessedParams) (int64, error) {
	return a.q.MarkChannelInboundDedupProcessed(ctx, arg)
}

// SessionTitles 保留在构造函数的签名里，仅为适配器兼容。新建的隐式渠道 Chat 刻意以空的
// 持久化标题开始：其第一条有效用户消息会初始化共享的确定性标题，客户端把这段短暂空档
// 渲染成本地化的 "New chat" 兜底。
type SessionTitles struct {
	Group    string
	Direct   string
	Fallback string
}

// forType 按聊天类型挑标题：群聊/私聊/兜底。
func (t SessionTitles) forType(ct channel.ChatType) string {
	switch ct {
	case channel.ChatTypeGroup:
		return t.Group
	case channel.ChatTypeP2P:
		return t.Direct
	default:
		return t.Fallback
	}
}

// ChatSession 是共享的聊天会话服务。每个 channel_type 构建一个实例（绑定行因此带正确的
// 判别值）；逻辑其余部分平台中立。
type ChatSession struct {
	q           SessionQueries
	tx          TxStarter
	channelType channel.Type
	titles      SessionTitles
}

// NewChatSession 在生成查询之上构建共享服务。tx 必填：AppendUserMessage 把去重 Mark 跑在
// chat_message 事务**内部**，持久化写入与 Mark 一起提交（或一起回滚）。
func NewChatSession(q *db.Queries, tx TxStarter, channelType channel.Type, titles SessionTitles) *ChatSession {
	return &ChatSession{q: dbSessionQueries{q: q}, tx: tx, channelType: channelType, titles: titles}
}

// newChatSessionWith 是测试缝：直接收 SessionQueries，内存假实现就能顶替 *db.Queries。
func newChatSessionWith(q SessionQueries, tx TxStarter, channelType channel.Type, titles SessionTitles) *ChatSession {
	return &ChatSession{q: q, tx: tx, channelType: channelType, titles: titles}
}

// EnsureSessionInput 是 EnsureSession 的渠道无关入参。
//
// BindingKey 是**会话隔离**键（存为 channel_chat_id；每个 (installation_id, BindingKey)
// 一条 chat_session）。它刻意**不是**"要回进哪个聊天"：适配器自己拼它，让不同对话得到
// 不同会话——飞书传 chat id；Slack 的 DM 传 channel id、频道/线程则传 channel id 加线程
// 根 id，这样同一个 Slack 频道里两个 @bot 线程不会塌缩成同一份转写（Hermes 模型：与 IM
// 无关，Slack 群按线程根隔离）。**有线程的平台绝不能**把原始平台 chat id 直接当键传。
//
// BindingConfig 是键单独带不了的不透明平台路由——例如 BindingKey 是复合键时 Slack 的真实
// channel_id——持久化在绑定的 config 上，供出站路径读回。nil 视为 "{}"。
//
// Sender 是已解析的 Multica 用户（会话创建者：私聊是唯一那个人、群聊是安装者——由调用方
// 决定谁是）。
type EnsureSessionInput struct {
	WorkspaceID    pgtype.UUID
	AgentID        pgtype.UUID
	InstallationID pgtype.UUID
	Sender         pgtype.UUID
	BindingKey     string
	BindingConfig  []byte
	ChatType       channel.ChatType
}

// EnsureSession 返回绑定到 (installation, BindingKey) 的 chat_session.id；首次接触时创建
// （连同它的 channel_chat_session_binding）。两条并发首条消息的竞态由
// UNIQUE (installation_id, channel_chat_id) 约束解决：输家重读赢家的行。
func (s *ChatSession) EnsureSession(ctx context.Context, in EnsureSessionInput) (pgtype.UUID, error) {
	lookup := db.GetChannelChatSessionBindingParams{InstallationID: in.InstallationID, ChannelChatID: in.BindingKey}

	existing, err := s.q.GetChannelChatSessionBinding(ctx, lookup)
	if err == nil {
		return existing.ChatSessionID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, fmt.Errorf("lookup chat session binding: %w", err)
	}

	id, err := s.createSessionAndBinding(ctx, in)
	if err == nil {
		return id, nil
	}
	if isUniqueViolation(err) {
		// 输掉了创建竞态：重读赢家的绑定行。
		existing, lookupErr := s.q.GetChannelChatSessionBinding(ctx, lookup)
		if lookupErr == nil {
			return existing.ChatSessionID, nil
		}
		return pgtype.UUID{}, fmt.Errorf("race re-read after unique violation: %w", lookupErr)
	}
	return pgtype.UUID{}, err
}

// createSessionAndBinding 在一个事务里创建 chat_session 与它的绑定行。
func (s *ChatSession) createSessionAndBinding(ctx context.Context, in EnsureSessionInput) (pgtype.UUID, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	// 创建会话前先对工作区行做 FOR KEY SHARE——这是 #5219 delete/create 协议的"创建"
	// 半边，保证渠道会话不会建进"正在删除中的工作区"
	//（见 LockWorkspaceForChatSessionCreate）。
	if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, in.WorkspaceID); err != nil {
		return pgtype.UUID{}, fmt.Errorf("lock workspace for chat session create: %w", err)
	}

	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: in.WorkspaceID,
		AgentID:     in.AgentID,
		CreatorID:   in.Sender,
		Title:       "",
	})
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("create chat session: %w", err)
	}
	bindingConfig := in.BindingConfig
	if len(bindingConfig) == 0 {
		bindingConfig = []byte("{}")
	}
	if _, err := qtx.CreateChannelChatSessionBinding(ctx, db.CreateChannelChatSessionBindingParams{
		ChatSessionID:  session.ID,
		InstallationID: in.InstallationID,
		ChannelType:    string(s.channelType),
		ChannelChatID:  in.BindingKey,
		ChatType:       string(in.ChatType),
		Config:         bindingConfig,
	}); err != nil {
		return pgtype.UUID{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, fmt.Errorf("commit: %w", err)
	}
	return session.ID, nil
}

// AppendInput 是 AppendUserMessage 的渠道无关入参。Body 是完整存储文本（含任何平台增强）；
// CommandText 是用户**亲手**打的文本、供 `/issue` 解析（空则回落用 Body）——因为增强是
// 平台特异的，由适配器提供。ClaimToken 是去重属主围栏：有效时 Mark 在本方法的事务内执行。
//
// MessageID 与 ThreadID 是本条触发的**真实**平台消息 id 与线程 id——即绑定上记录的出站
// 回复目标（last_message_id / last_thread_id），**不是**会话 BindingKey。因为每个隔离会话
// 有自己的绑定行，在兄弟线程间按会话记录真实线程不会撞。
type AppendInput struct {
	SessionID           pgtype.UUID
	Sender              pgtype.UUID
	InstallationID      pgtype.UUID
	Body                string
	CommandText         string
	MessageID           string
	DedupMessageID      string
	ThreadID            string
	ClaimToken          pgtype.UUID
	MediaPendingSeconds float64
	ForceFresh          bool
	// BeforeCommit 追加"必须与这条消息及其上下文代变更原子提交"的工作。原生斜杠命令用它
	// 在消息可见之前给任务拍快照并入队。
	BeforeCommit func(context.Context, pgx.Tx, db.ChatSession, int64, pgtype.UUID, int64) error
}

// StartSessionInput 是 /new 的共享事务实现入参。适配器只提供路由键/配置与可选平台围栏。
type StartSessionInput struct {
	EnsureSessionInput
	// Initiator 是发出 /new 命令的已认证发送者。内嵌 EnsureSessionInput 里的 Sender
	// 仍是新创建 Chat 的属主。
	Initiator              pgtype.UUID
	Body                   string
	MessageID              string
	DedupMessageID         string
	ThreadID               string
	ClaimToken             pgtype.UUID
	MediaPendingSeconds    float64
	PersistMessage         bool
	HistoryBoundaryPending bool
	BeforeWrite            func(context.Context, pgx.Tx) error
	// BeforeCommit 追加"必须与路由轮换和首条消息原子提交"的工作。新会话在 tx 内可见，
	// 但这些写入在 StartSession 提交前对外部都不可观察。
	BeforeCommit func(context.Context, pgx.Tx, db.ChatSession) error
}

// StartSession 原子地完成：退役当前路由代 → 创建"显式可见"的 Chat → 装好下一代 → 可选
// 地把命令正文写成它的第一条普通用户消息。
func (s *ChatSession) StartSession(ctx context.Context, in StartSessionInput) (StartSessionResult, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return StartSessionResult{}, fmt.Errorf("begin start chat tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, in.WorkspaceID); err != nil {
		return StartSessionResult{}, fmt.Errorf("lock workspace for start chat: %w", err)
	}

	lookup := db.GetChannelChatSessionBindingParams{InstallationID: in.InstallationID, ChannelChatID: in.BindingKey}
	current, currentErr := qtx.GetChannelChatSessionBinding(ctx, lookup)
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return StartSessionResult{}, fmt.Errorf("load current chat route: %w", currentErr)
	}
	nextRevision := int64(1)
	if currentErr == nil {
		// 保持 append 用的"chat_session → binding"加锁顺序。
		if _, err := qtx.LockChatSessionForAppend(ctx, current.ChatSessionID); err != nil {
			return StartSessionResult{}, fmt.Errorf("lock prior chat session: %w", err)
		}
		locked, err := qtx.LockCurrentChannelChatSessionBinding(ctx, db.LockCurrentChannelChatSessionBindingParams{
			InstallationID: in.InstallationID,
			ChannelChatID:  in.BindingKey,
		})
		if err != nil || locked.ID != current.ID {
			return StartSessionResult{}, ErrRouteChanged
		}
		if in.BeforeWrite != nil {
			if err := in.BeforeWrite(ctx, tx); err != nil {
				return StartSessionResult{}, err
			}
		}
		historyEnd := locked.LastMessageID
		if in.HistoryBoundaryPending && in.MessageID == "" {
			// 原生命令没有公开平台游标：把旧代的历史终点保持开放，直到下一条真实入站
			// 原子地合上旧代、并用同一个消息 id 打开待定代。
			historyEnd = pgtype.Text{}
		} else if in.MessageID != "" {
			historyEnd = textOrNull(in.MessageID)
		}
		if _, err := qtx.RetireChannelChatSessionBinding(ctx, db.RetireChannelChatSessionBindingParams{
			ID: locked.ID, HistoryEndMessageID: historyEnd,
		}); err != nil {
			return StartSessionResult{}, fmt.Errorf("retire current chat route: %w", err)
		}
		nextRevision = locked.RouteRevision + 1
	} else if in.BeforeWrite != nil {
		if err := in.BeforeWrite(ctx, tx); err != nil {
			return StartSessionResult{}, err
		}
	}

	title := ""
	if in.PersistMessage {
		title = deriveFirstMessageTitle(in.Body, in.MediaPendingSeconds > 0)
	}
	session, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		ID: dbid.NewV7(), WorkspaceID: in.WorkspaceID, AgentID: in.AgentID,
		CreatorID: in.Sender, Title: title,
	})
	if err != nil {
		return StartSessionResult{}, fmt.Errorf("create started chat session: %w", err)
	}
	if _, err := qtx.MarkChatSessionExplicitlyCreated(ctx, session.ID); err != nil {
		return StartSessionResult{}, fmt.Errorf("mark started chat explicit: %w", err)
	}
	config := in.BindingConfig
	if len(config) == 0 {
		config = []byte("{}")
	}
	startMessageID := textOrNull(in.MessageID)
	binding, err := qtx.CreateChannelChatSessionBindingGeneration(ctx, db.CreateChannelChatSessionBindingGenerationParams{
		ChatSessionID: session.ID, InstallationID: in.InstallationID,
		ChannelType: string(s.channelType), ChannelChatID: in.BindingKey,
		ChatType: string(in.ChatType), Config: config, RouteRevision: nextRevision,
		HistoryStartMessageID:  startMessageID,
		HistoryBoundaryPending: in.HistoryBoundaryPending && in.MessageID == "",
	})
	if err != nil {
		if isUniqueViolation(err) {
			return StartSessionResult{}, ErrRouteChanged
		}
		return StartSessionResult{}, fmt.Errorf("create next chat route: %w", err)
	}
	result := StartSessionResult{SessionID: session.ID, BindingID: binding.ID, RouteRevision: binding.RouteRevision}
	result.Append.InitialTitle = title
	result.Append.BindingID = binding.ID
	result.Append.RouteRevision = binding.RouteRevision
	result.Append.ContextRevision = 1
	if in.PersistMessage {
		if _, err := qtx.SetChannelChatContextInitiator(ctx, db.SetChannelChatContextInitiatorParams{
			ChatSessionID: session.ID, Revision: 1, InitiatorUserID: in.Initiator,
		}); err != nil {
			return StartSessionResult{}, fmt.Errorf("snapshot started chat initiator: %w", err)
		}
		msg, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID: dbid.NewV7(), ChatSessionID: session.ID, Role: "user", Content: in.Body,
			ChannelMediaPendingSecs: pgtype.Float8{Float64: in.MediaPendingSeconds, Valid: in.MediaPendingSeconds > 0},
			ChannelIngested:         pgtype.Bool{Bool: true, Valid: true},
			ChannelContextRevision:  pgtype.Int8{Int64: 1, Valid: true},
		})
		if err != nil {
			return StartSessionResult{}, fmt.Errorf("create first chat message: %w", err)
		}
		if err := qtx.TouchChatSession(ctx, session.ID); err != nil {
			return StartSessionResult{}, fmt.Errorf("touch started chat: %w", err)
		}
		result.Append.MessageID = msg.ID
		result.Append.PendingContexts = []PendingContext{{Revision: 1, InitiatorUserID: in.Initiator}}
	}
	if in.MessageID != "" {
		if err := qtx.UpdateChannelChatSessionBindingReplyTarget(ctx, db.UpdateChannelChatSessionBindingReplyTargetParams{
			ReplyChatSessionID: session.ID, LastMessageID: textOrNull(in.MessageID), LastThreadID: textOrNull(in.ThreadID),
		}); err != nil {
			return StartSessionResult{}, fmt.Errorf("set started chat reply target: %w", err)
		}
	}
	dedupMessageID := in.DedupMessageID
	if dedupMessageID == "" {
		dedupMessageID = in.MessageID
	}
	if in.ClaimToken.Valid && dedupMessageID != "" {
		rows, err := qtx.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
			InstallationID: in.InstallationID, MessageID: dedupMessageID, ClaimToken: in.ClaimToken,
		})
		if err != nil {
			return StartSessionResult{}, fmt.Errorf("mark start chat dedup: %w", err)
		}
		if rows == 0 {
			return StartSessionResult{}, ErrClaimLost
		}
		result.Append.DedupMarked = true
	}
	if in.BeforeCommit != nil {
		if err := in.BeforeCommit(ctx, tx, session); err != nil {
			return StartSessionResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return StartSessionResult{}, fmt.Errorf("commit start chat: %w", err)
	}
	startOutcome := "chat_started"
	if in.PersistMessage {
		startOutcome = "chat_started_with_message"
	}
	slog.Info("channel chat route started",
		"outcome", startOutcome,
		"channel_type", string(s.channelType),
		"source_chat_session_id", utilUUID(current.ChatSessionID),
		"new_chat_session_id", utilUUID(session.ID),
		"chat_route_generation", binding.RouteRevision,
	)
	return result, nil
}

func utilUUID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

// BindMediaInput 在一条短促的纯数据库事务里，把已上传媒体链接到 /issue 目标或持久化
// chat message。IssueDescriptionBase 有效时，只在该 issue 仍保有"创建那一刻的描述"时允许
// 行内替换；否则 issue 媒体以"并发安全"的追加形态落下。远程下载/上传发生在本调用之前、
// 连接器 ACK 路径之外。
type BindMediaInput struct {
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

// channelCommandMessageKind 标记一条由 Router 同步处理的控制面轮次。公开 Chat 投影省略
// 它，任务批次封装也省略——agent 在后续轮次不能再执行一遍这条命令。
const channelCommandMessageKind = "channel_command"

// AppendUserMessage 把用户消息写进 chat_session（touch 它并记录回复目标），提供认领 token
// 时在事务内跑去重 Mark，返回持久化消息 id 及（若有）解析出的 `/issue` 命令。当并发回收
// 中途轮换了去重 token 时返回 ErrClaimLost——此时整个事务回滚（不会落下 chat_message）。
func (s *ChatSession) AppendUserMessage(ctx context.Context, in AppendInput) (AppendResult, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return AppendResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	commandSource := in.CommandText
	if commandSource == "" {
		commandSource = in.Body
	}
	cmd, _ := ParseIssueCommand(commandSource)
	// 上下文路径在 binding 与 generation 之前锁 chat_session。这也让后面的
	// TouchChatSession 更新不会引入"binding → chat_session"的反向边去对着任务入队。
	if _, err := qtx.LockChatSessionForAppend(ctx, in.SessionID); err != nil {
		return AppendResult{}, fmt.Errorf("lock chat session for append: %w", err)
	}
	binding, err := qtx.LockCurrentChannelChatSessionBindingBySession(ctx, in.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AppendResult{}, ErrRouteChanged
		}
		return AppendResult{}, fmt.Errorf("lock channel chat binding: %w", err)
	}
	// 绑定锁把普通追加按路由顺序串行化。在这个围栏**之后**才判断首轮可见性并初始化其
	// 兜底标题——两条并发入站消息不可能都宣布"我创建了"、也不可能让第二条消息赢走标题。
	currentSession, err := qtx.GetChatSession(ctx, in.SessionID)
	if err != nil {
		return AppendResult{}, fmt.Errorf("reload chat session for append: %w", err)
	}
	hadPublicUserMessage, err := qtx.ChatSessionHasPublicUserMessage(ctx, in.SessionID)
	if err != nil {
		return AppendResult{}, fmt.Errorf("check public chat history: %w", err)
	}
	becameVisible := cmd == nil && !hadPublicUserMessage && !currentSession.ExplicitlyCreatedAt.Valid
	initializedTitle := ""
	if cmd == nil {
		title := deriveFirstMessageTitle(in.Body, in.MediaPendingSeconds > 0)
		if becameVisible {
			// 隐式会话迎来第一条"公开用户消息"：这是它转正的时刻，替换掉隐式标题。
			if _, err := qtx.ReplaceImplicitChatSessionTitle(ctx, db.ReplaceImplicitChatSessionTitleParams{ID: in.SessionID, Title: title}); err != nil {
				return AppendResult{}, fmt.Errorf("replace implicit chat title: %w", err)
			}
			initializedTitle = title
		} else if title != "" {
			// 显式创建的会话在首条消息时才初始化标题（没有就是空）。
			if _, err := qtx.InitializeChatSessionTitle(ctx, db.InitializeChatSessionTitleParams{ID: in.SessionID, Title: title}); err == nil {
				initializedTitle = title
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return AppendResult{}, fmt.Errorf("initialize chat title: %w", err)
			}
		}
	}
	contextState, err := qtx.LockChannelChatContextGenerationByRevision(ctx, db.LockChannelChatContextGenerationByRevisionParams{
		ChatSessionID: in.SessionID, Revision: binding.ContextRevision,
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("lock channel chat context: %w", err)
	}
	contextRevision := binding.ContextRevision
	if in.ForceFresh {
		// /clear：推进到新一代。历史边界消息 = 本条触发（若它本身也入库）。
		advanced, err := qtx.AdvanceChannelChatContextGeneration(ctx, db.AdvanceChannelChatContextGenerationParams{
			ChatSessionID: in.SessionID, CurrentRevision: contextRevision,
			HistoryBoundaryMessageID: textOrNull(in.MessageID), HasMessageBody: in.MessageID != "",
		})
		if err != nil {
			return AppendResult{}, fmt.Errorf("advance channel chat context: %w", err)
		}
		contextRevision = advanced.Revision
	} else if contextState.HistoryBoundaryPending && in.MessageID != "" {
		// 上一条命令把历史边界悬着；真实入站来了，用它合上边界。
		if err := qtx.ResolveChannelChatContextHistoryStart(ctx, db.ResolveChannelChatContextHistoryStartParams{
			HistoryStartMessageID: textOrNull(in.MessageID), ChatSessionID: in.SessionID, Revision: contextRevision,
		}); err != nil {
			return AppendResult{}, fmt.Errorf("resolve channel chat history boundary: %w", err)
		}
	}
	// 渠道命令不进 agent 输入，所以它不能顶替"恢复更早未认领的 agent 可见消息"所用的
	// 主体身份。
	if contextRevision > 0 && cmd == nil {
		if _, err := qtx.SetChannelChatContextInitiator(ctx, db.SetChannelChatContextInitiatorParams{
			ChatSessionID: in.SessionID, Revision: contextRevision, InitiatorUserID: in.Sender,
		}); err != nil {
			return AppendResult{}, fmt.Errorf("snapshot channel context initiator: %w", err)
		}
	}
	// channel_ingested 是取消路径门控所依据的不可变来源戳：必须与消息盖在同一事务里，
	// 之后任何绑定删除（归档、重绑安装）都无法剥掉它。
	msg, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:                      dbid.NewV7(),
		ChatSessionID:           in.SessionID,
		Role:                    "user",
		Content:                 in.Body,
		MessageKind:             textOrNullIf(cmd != nil, channelCommandMessageKind),
		ChannelMediaPendingSecs: pgtype.Float8{Float64: in.MediaPendingSeconds, Valid: in.MediaPendingSeconds > 0},
		ChannelIngested:         pgtype.Bool{Bool: true, Valid: true},
		ChannelContextRevision:  pgtype.Int8{Int64: contextRevision, Valid: contextRevision > 0},
	})
	if err != nil {
		return AppendResult{}, fmt.Errorf("create chat message: %w", err)
	}
	pendingContexts, err := qtx.ListUnownedChannelChatContextRevisions(ctx, in.SessionID)
	if err != nil {
		return AppendResult{}, fmt.Errorf("list pending channel contexts: %w", err)
	}
	if err := qtx.TouchChatSession(ctx, in.SessionID); err != nil {
		return AppendResult{}, fmt.Errorf("touch chat session: %w", err)
	}

	// 记录最新触发，解耦的出站 patcher 才能把回复回进来源话题。
	if in.MessageID != "" {
		if err := qtx.UpdateChannelChatSessionBindingReplyTarget(ctx, db.UpdateChannelChatSessionBindingReplyTargetParams{
			ReplyChatSessionID: in.SessionID,
			LastMessageID:      textOrNull(in.MessageID),
			LastThreadID:       textOrNull(in.ThreadID),
		}); err != nil {
			return AppendResult{}, fmt.Errorf("update reply target: %w", err)
		}
	}

	markedInTx := false
	dedupMessageID := in.DedupMessageID
	if dedupMessageID == "" {
		dedupMessageID = in.MessageID
	}
	if in.ClaimToken.Valid && dedupMessageID != "" {
		rows, err := qtx.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
			InstallationID: in.InstallationID,
			MessageID:      dedupMessageID,
			ClaimToken:     in.ClaimToken,
		})
		if err != nil {
			return AppendResult{}, fmt.Errorf("mark dedup processed: %w", err)
		}
		if rows == 0 {
			// 认领行已被别的 worker 重新认领：靠 deferred Rollback 整体回滚，
			// 第二条 chat_message 不会落下。
			return AppendResult{}, ErrClaimLost
		}
		markedInTx = true
	}
	if in.BeforeCommit != nil {
		if err := in.BeforeCommit(ctx, tx, currentSession, contextRevision, binding.ID, binding.RouteRevision); err != nil {
			return AppendResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return AppendResult{}, fmt.Errorf("commit: %w", err)
	}
	return AppendResult{
		MessageID:       msg.ID,
		IssueCommand:    cmd,
		DedupMarked:     markedInTx,
		ContextRevision: contextRevision,
		PendingContexts: pendingContexts,
		InitialTitle:    initializedTitle,
		BecameVisible:   becameVisible,
		BindingID:       binding.ID,
		RouteRevision:   binding.RouteRevision,
	}, nil
}

// MarkPendingFresh 持久化一条裸 `/clear` 命令。带正文的 `/clear` 消息在
// AppendUserMessage 的事务里标记同一个标志。
func (s *ChatSession) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return s.MarkPendingFreshWithDedup(ctx, sessionID, messageID, pgtype.UUID{}, "", pgtype.UUID{})
}

// MarkPendingFreshWithDedup 原子地推进上下文代并完结一个可选的传输去重认领。原生命令
// 没有公开平台游标，所以 messageID 可能为空、而 dedupMessageID 持有持久化的 Socket Mode
// 信封 id。
func (s *ChatSession) MarkPendingFreshWithDedup(
	ctx context.Context,
	sessionID pgtype.UUID,
	messageID string,
	installationID pgtype.UUID,
	dedupMessageID string,
	claimToken pgtype.UUID,
) error {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fresh context tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockChatSessionForAppend(ctx, sessionID); err != nil {
		return fmt.Errorf("lock chat session for fresh context: %w", err)
	}
	binding, err := qtx.LockCurrentChannelChatSessionBindingBySession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRouteChanged
		}
		return fmt.Errorf("lock channel chat binding: %w", err)
	}
	_, err = qtx.LockChannelChatContextGenerationByRevision(ctx, db.LockChannelChatContextGenerationByRevisionParams{
		ChatSessionID: sessionID, Revision: binding.ContextRevision,
	})
	if err != nil {
		return fmt.Errorf("lock channel chat context: %w", err)
	}
	if _, err := qtx.AdvanceChannelChatContextGeneration(ctx, db.AdvanceChannelChatContextGenerationParams{
		ChatSessionID: sessionID, CurrentRevision: binding.ContextRevision,
		HistoryBoundaryMessageID: textOrNull(messageID), HasMessageBody: false,
	}); err != nil {
		return fmt.Errorf("advance channel chat context: %w", err)
	}
	if claimToken.Valid && dedupMessageID != "" {
		rows, err := qtx.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
			InstallationID: installationID,
			MessageID:      dedupMessageID,
			ClaimToken:     claimToken,
		})
		if err != nil {
			return fmt.Errorf("mark fresh context dedup: %w", err)
		}
		if rows == 0 {
			return ErrClaimLost
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit fresh context: %w", err)
	}
	return nil
}

// BindMediaRefs 在 IssueID 有效时为它创建附件行，否则把它们链到既有的持久化 chat
// message，并清除消息的媒体待处理标记。失败先回滚附件行，再单独清标记——占位符可以
// 立刻被晋升、做优雅降级。保留"仅错误"的既有 API，供适配器测试替身与直接调用方使用。
func (s *ChatSession) BindMediaRefs(ctx context.Context, in BindMediaInput) error {
	_, err := s.BindMediaRefsWithResult(ctx, in)
	return err
}

// BindMediaRefsWithResult 额外把"首个媒体标题初始化"报告给生产适配器，Router 据此发布并
// 精修标题。
func (s *ChatSession) BindMediaRefsWithResult(ctx context.Context, in BindMediaInput) (BindMediaResult, error) {
	var result BindMediaResult
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin media tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	if len(in.MediaRefs) > 0 {
		if err := s.bindMediaRefs(ctx, qtx, in, &result); err != nil {
			_ = tx.Rollback(ctx)
			if clearErr := s.clearMediaPending(ctx, s.q, in); clearErr != nil {
				return BindMediaResult{}, errors.Join(err, clearErr)
			}
			return BindMediaResult{}, err
		}
	}
	if err := s.clearMediaPending(ctx, qtx, in); err != nil {
		return BindMediaResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		// 歧义提交无需裁断：意图台账行在同一个事务里被删，所以"提交成功 ⇔ 意图已删"，
		// 原子成立。任一种结果下对象都交给对账器收尾。
		return BindMediaResult{}, fmt.Errorf("commit media: %w", err)
	}
	return result, nil
}

// clearMediaPending 清除 chat message 的媒体待处理标记（在绑定事务内、或绑定失败后的
// 单独补救事务里）。
func (s *ChatSession) clearMediaPending(ctx context.Context, q SessionQueries, in BindMediaInput) error {
	if err := q.ClearChatMessageChannelMediaPending(ctx, db.ClearChatMessageChannelMediaPendingParams{
		ID:            in.MessageID,
		ChatSessionID: in.SessionID,
	}); err != nil {
		return fmt.Errorf("clear chat message media pending: %w", err)
	}
	return nil
}

// bindMediaRefs 是媒体绑定的核心事务逻辑（在调用方事务内执行）。
func (s *ChatSession) bindMediaRefs(ctx context.Context, qtx SessionQueries, in BindMediaInput, result *BindMediaResult) error {
	if !in.WorkspaceID.Valid {
		return errors.New("bind media refs: workspace_id is required")
	}
	if !in.MessageID.Valid {
		return errors.New("bind media refs: message_id is required")
	}
	keys := make([]string, 0, len(in.MediaRefs))
	for _, ref := range in.MediaRefs {
		if ref.StorageURL == "" {
			return errors.New("bind media refs: storage_url is required")
		}
		if ref.StorageKey == "" {
			return errors.New("bind media refs: storage_key is required")
		}
		keys = append(keys, ref.StorageKey)
	}
	if in.IssueID.Valid {
		if _, err := qtx.LockIssueForChannelMediaBind(ctx, db.LockIssueForChannelMediaBindParams{
			ID:          in.IssueID,
			WorkspaceID: in.WorkspaceID,
		}); err != nil {
			return fmt.Errorf("validate issue media target: %w", err)
		}
	}
	// 在同一个事务内认领意图台账行：提交成功 ⇔ 意图已删，原子成立，歧义 COMMIT 无需
	// 裁断。已被对账器挪进 'deleting' 的键不会被返回，其引用**不得**挂载——对象正在被
	// 删除，占位符保持原位。
	claimedKeys, err := qtx.ClaimChannelMediaPendingObjectsForBind(ctx, db.ClaimChannelMediaPendingObjectsForBindParams{
		StorageKeys: keys,
		WorkspaceID: in.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("claim media intents: %w", err)
	}
	claimed := make(map[string]bool, len(claimedKeys))
	for _, k := range claimedKeys {
		claimed[k] = true
	}
	type createdMedia struct {
		id       pgtype.UUID
		ref      channel.MediaRef
		filename string
	}
	created := make([]createdMedia, 0, len(in.MediaRefs))
	ids := make([]pgtype.UUID, 0, len(in.MediaRefs))
	for _, ref := range in.MediaRefs {
		if !claimed[ref.StorageKey] {
			slog.Warn("channel media: intent claimed by reconciler; skipping attach",
				"storage_key", ref.StorageKey)
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("create attachment id: %w", err)
		}
		contentType := ref.MimeType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		filename := ref.Filename
		if filename == "" {
			filename = defaultMediaFilename(ref.Type, id.String(), contentType)
		}
		chatSessionID := in.SessionID
		if in.IssueID.Valid {
			chatSessionID = pgtype.UUID{}
		}
		att, err := qtx.CreateAttachment(ctx, db.CreateAttachmentParams{
			ID:            pgtype.UUID{Bytes: id, Valid: true},
			WorkspaceID:   in.WorkspaceID,
			IssueID:       in.IssueID,
			ChatSessionID: chatSessionID,
			UploaderType:  "member",
			UploaderID:    in.Sender,
			Filename:      filename,
			Url:           ref.StorageURL,
			ContentType:   contentType,
			SizeBytes:     ref.SizeBytes,
		})
		if err != nil {
			return fmt.Errorf("create channel attachment: %w", err)
		}
		ids = append(ids, att.ID)
		created = append(created, createdMedia{id: att.ID, ref: ref, filename: filename})
	}
	if len(ids) == 0 {
		return nil
	}
	if !in.IssueID.Valid {
		// 普通消息：头一件媒体驱动"媒体标题初始化"（仅当会话还没有标题）。
		for _, media := range created {
			source := media.ref.Filename
			if strings.TrimSpace(source) == "" {
				source = mediaTypeTitle(media.ref.Type)
			}
			title := DeriveChatTitle(source)
			if title == "" {
				continue
			}
			if _, err := qtx.InitializeChatSessionMediaTitle(ctx, db.InitializeChatSessionMediaTitleParams{
				ID: in.SessionID, MessageID: in.MessageID, Title: title,
			}); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("initialize media chat title: %w", err)
				}
			} else {
				result.InitialTitle = title
				result.TitleSource = source
			}
			break
		}
	}
	if in.IssueID.Valid {
		// /issue 目标：把媒体块拼进 issue 描述（必要时替换命令正文里的行内占位符）。
		issueMarkdown := make([]string, 0, len(created))
		replacements := make([]inlineMediaReplacement, 0, len(created))
		for _, media := range created {
			block := channelmedia.Block(
				uuid.UUID(media.id.Bytes).String(),
				media.filename,
				media.ref.Type == channel.MsgTypeImage,
			)
			issueMarkdown = append(issueMarkdown, block)
			if media.ref.InlinePlaceholder != "" {
				replacements = append(replacements, inlineMediaReplacement{
					placeholder: media.ref.InlinePlaceholder,
					index:       media.ref.InlineIndex,
					markdown:    block,
				})
			}
		}

		base := pgtype.Text{}
		description := pgtype.Text{}
		if in.IssueDescriptionBase.Valid {
			if composed, changed := composeIssueCommandMediaDescription(
				in.Body,
				in.IssueCommandText,
				replacements,
				in.IssueDescriptionBase.String,
			); changed {
				base = in.IssueDescriptionBase
				description = pgtype.Text{String: composed, Valid: true}
			}
		}
		if _, err := qtx.MaterializeIssueChannelMediaMarkdown(ctx, db.MaterializeIssueChannelMediaMarkdownParams{
			ID:              in.IssueID,
			WorkspaceID:     in.WorkspaceID,
			BaseDescription: base,
			Description:     description.String,
			Markdown:        pgtype.Text{String: strings.Join(issueMarkdown, "\n\n"), Valid: true},
		}); err != nil {
			return fmt.Errorf("materialize issue channel media markdown: %w", err)
		}
		return nil
	}
	linkedIDs, err := qtx.LinkAttachmentsToChatMessage(ctx, db.LinkAttachmentsToChatMessageParams{
		ChatMessageID: in.MessageID,
		ChatSessionID: in.SessionID,
		WorkspaceID:   in.WorkspaceID,
		UploaderType:  "member",
		UploaderID:    in.Sender,
		AttachmentIds: ids,
	})
	if err != nil {
		return fmt.Errorf("link chat attachments: %w", err)
	}

	linked := make(map[pgtype.UUID]bool, len(linkedIDs))
	for _, id := range linkedIDs {
		linked[id] = true
	}
	replacements := make([]inlineMediaReplacement, 0, len(created))
	for _, media := range created {
		if !linked[media.id] || media.ref.InlinePlaceholder == "" {
			continue
		}
		replacements = append(replacements, inlineMediaReplacement{
			placeholder: media.ref.InlinePlaceholder,
			index:       media.ref.InlineIndex,
			markdown:    inlineAttachmentMarkdown(media.ref, media.id),
		})
	}
	// 把落库正文里的占位符原位替换成 Markdown 附件链接。
	if body, changed := composeInlineMediaBody(in.Body, replacements); changed {
		rows, err := qtx.UpdateChatMessageContentForChannelMedia(ctx, db.UpdateChatMessageContentForChannelMediaParams{
			ID:            in.MessageID,
			ChatSessionID: in.SessionID,
			Content:       body,
		})
		if err != nil {
			return fmt.Errorf("update chat message inline media: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("update chat message inline media: updated %d rows", rows)
		}
	}
	return nil
}

// inlineMediaReplacement 是一次"把占位符换成 Markdown"的候选替换。
type inlineMediaReplacement struct {
	placeholder string
	index       int
	markdown    string
}

type inlineMediaEdit struct {
	start int
	end   int
	text  string
}

// composeInlineMediaBody 按占位符在正文里第 index 次出现的位置，把它们依次替换成
// Markdown；返回替换后的正文与是否发生了替换。
func composeInlineMediaBody(body string, replacements []inlineMediaReplacement) (string, bool) {
	edits := make([]inlineMediaEdit, 0, len(replacements))
	for _, replacement := range replacements {
		if replacement.placeholder == "" || replacement.index < 0 || replacement.markdown == "" {
			continue
		}
		start := nthSubstringIndex(body, replacement.placeholder, replacement.index)
		if start < 0 {
			continue
		}
		edits = append(edits, inlineMediaEdit{
			start: start,
			end:   start + len(replacement.placeholder),
			text:  replacement.markdown,
		})
	}
	if len(edits) == 0 {
		return body, false
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out strings.Builder
	last := 0
	for _, edit := range edits {
		if edit.start < last {
			continue // 区间重叠：只保第一个（防御畸形输入）
		}
		out.WriteString(body[last:edit.start])
		out.WriteString(edit.text)
		last = edit.end
	}
	out.WriteString(body[last:])
	return out.String(), true
}

// composeIssueCommandMediaDescription 在与规范化入站正文相同的位次上物化媒体，然后去掉
// /issue 指令行。命令前的已解析媒体保留在前缀里；适配器加的引用上下文仍不属于 issue
// 描述契约。
func composeIssueCommandMediaDescription(body, commandText string, replacements []inlineMediaReplacement, fallback string) (string, bool) {
	commandStart, _, ok := issueCommandLineBounds(body, commandText)
	if !ok {
		return fallback, false
	}

	type positionedMarkdown struct {
		start    int
		markdown string
	}
	prefix := make([]positionedMarkdown, 0, len(replacements))
	for _, replacement := range replacements {
		start := nthSubstringIndex(body, replacement.placeholder, replacement.index)
		if start >= 0 && start < commandStart {
			prefix = append(prefix, positionedMarkdown{start: start, markdown: replacement.markdown})
		}
	}
	sort.Slice(prefix, func(i, j int) bool { return prefix[i].start < prefix[j].start })

	composed, changed := composeInlineMediaBody(body, replacements)
	if !changed {
		return fallback, false
	}
	_, commandEnd, ok := issueCommandLineBounds(composed, commandText)
	if !ok {
		return fallback, false
	}

	parts := make([]string, 0, len(prefix)+1)
	for _, item := range prefix {
		if markdown := strings.TrimSpace(item.markdown); markdown != "" {
			parts = append(parts, markdown)
		}
	}
	if suffix := strings.TrimSpace(composed[commandEnd:]); suffix != "" {
		parts = append(parts, suffix)
	}
	description := strings.Join(parts, "\n\n")
	for _, replacement := range replacements {
		if replacement.markdown != "" && strings.Contains(description, replacement.markdown) {
			return description, true
		}
	}
	// 畸形适配器布局把每个命中的占位符都塞进了上面被删掉的命令行里。回退为追加——附件
	// 绝不会仅仅为了保住一个没法用的行内布局而变得不可见。
	return fallback, false
}

// nthSubstringIndex 返回 marker 在 body 里第 target 次（0 起）出现的下标；不足则 -1。
func nthSubstringIndex(body, marker string, target int) int {
	offset := 0
	for index := 0; ; index++ {
		found := strings.Index(body[offset:], marker)
		if found < 0 {
			return -1
		}
		found += offset
		if index == target {
			return found
		}
		offset = found + len(marker)
	}
}

// inlineAttachmentMarkdown 为一件已链接的媒体生成行内 Markdown：图片用 ![](...)，其余按
// 附件名生成转义后的链接。
func inlineAttachmentMarkdown(ref channel.MediaRef, id pgtype.UUID) string {
	downloadPath := "/api/attachments/" + uuid.UUID(id.Bytes).String() + "/download"
	if ref.Type == channel.MsgTypeImage {
		return "![](" + downloadPath + ")"
	}
	label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(ref.Filename)
	if label == "" {
		label = "attachment"
	}
	return "[" + label + "](" + downloadPath + ")"
}

// defaultMediaFilename 在平台没给文件名时按媒体种类与 content type 造一个 `前缀-id.扩展名`
// 的默认名。
func defaultMediaFilename(kind channel.MsgType, id, contentType string) string {
	prefix := "attachment"
	switch kind {
	case channel.MsgTypeImage:
		prefix = "image"
	case channel.MsgTypeVideo:
		prefix = "video"
	case channel.MsgTypeAudio:
		prefix = "audio"
	case channel.MsgTypeFile:
		prefix = "file"
	}
	ext := ""
	switch contentType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	case "video/mp4":
		ext = ".mp4"
	}
	return prefix + "-" + id + ext
}

// isUniqueViolation 判断错误是不是 PostgreSQL 唯一约束冲突。
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == pgSQLStateUniqueViolation
	}
	return false
}

// textOrNull 把空串编码成 SQL NULL，其余编码成有效文本。
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// textOrNullIf 按 valid 决定文本是否有效（false 即 NULL）。
func textOrNullIf(valid bool, s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: valid}
}
