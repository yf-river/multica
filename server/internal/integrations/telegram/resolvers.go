package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 角色定位：Telegram 的 ResolverSet——channel-agnostic 引擎 Router 跑入站流水线时，
// 需要每平台各自实现的那组"缝"（按安装路由 → 两级去重 → 身份与成员资格 → 保证会话 →
// 追加+标记 → /issue → 限速运行）。本文件把全部 Telegram 特有逻辑缝进通用引擎，
// 完全建立在通用 channel_* 查询 + 共享 engine.ChatSession 之上——零新增查询、零表结构
// 改动，与 slack/resolvers.go 同构。
//
// 文件内部按 Router 的调用顺序组织：安装路由 → 身份 → 去重 → 会话绑定 → 审计 →
// 输入指示。

// originTelegramChat 是通过 Telegram /issue 命令创建的 issue 的 origin_type 标签。
const originTelegramChat = "telegram_chat"

// NewTelegramResolverSet 组装 Telegram ResolverSet。replier 传 nil 表示关闭判定类出站
// 提示；deps 携带 Decrypter 时才启用打字指示（sendChatAction 需要解密 token）。
func NewTelegramResolverSet(q *db.Queries, tx engine.TxStarter, replier engine.OutboundReplier, typing engine.TypingNotifier) engine.ResolverSet {
	return engine.ResolverSet{
		Installation: &installationResolver{q: q},
		Identity:     &identityResolver{q: q},
		Dedup:        &deduper{q: q},
		Session: &sessionBinder{session: engine.NewChatSession(q, tx, TypeTelegram, engine.SessionTitles{
			Group:    "Telegram group",
			Direct:   "Telegram direct message",
			Fallback: "Telegram chat",
		})},
		Audit:      &auditor{q: q},
		Replier:    replier,
		Typing:     typing,
		OriginType: originTelegramChat,
	}
}

var (
	_ engine.InstallationResolver = (*installationResolver)(nil)
	_ engine.IdentityResolver     = (*identityResolver)(nil)
	_ engine.Deduper              = (*deduper)(nil)
	_ engine.SessionBinder        = (*sessionBinder)(nil)
	_ engine.Auditor              = (*auditor)(nil)
	_ engine.TypingNotifier       = (*typingNotifier)(nil)
)

// telegramBindingConfig 是持久化在"聊天会话绑定"config 里的不透明出站路由信息：当绑定键
// 是复合键（forum 话题隔离）时，真正的 chat id 靠它存活（回复时从 config 还原出 chat id）。
type telegramBindingConfig struct {
	ChatID string `json:"chat_id"`
}

// telegramSessionRouting 从一条入站消息推导出三样东西：会话隔离键 bindingKey、绑定配置
// config、以及回复线程 replyThread。规则：私聊是一整条连续会话（键 = chat id）；群聊每个
// chat 一条会话，但 forum 话题按话题隔离（键 = "chat:thread"）——这是飞书"按线程隔离"最
// 接近的对应物。纯函数，无 DB 也能单测。
func telegramSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte, replyThread string) {
	chatID := msg.Source.ChatID
	cfg, _ := json.Marshal(telegramBindingConfig{ChatID: chatID})
	if msg.Source.ChatType == channel.ChatTypeGroup && msg.Source.ThreadID != "" {
		return chatID + ":" + msg.Source.ThreadID, cfg, msg.Source.ThreadID
	}
	return chatID, cfg, msg.Source.ThreadID
}

// decodeTelegramRaw 把入站消息里携带的 telegramRawEvent 解回来（出站/审计侧要读
// BotID、EventType 等字段时用）。
func decodeTelegramRaw(msg channel.InboundMessage) (telegramRawEvent, error) {
	var raw telegramRawEvent
	if len(msg.Raw) == 0 {
		return telegramRawEvent{}, errors.New("telegram: inbound message Raw is empty")
	}
	if err := json.Unmarshal(msg.Raw, &raw); err != nil {
		return telegramRawEvent{}, fmt.Errorf("decode telegram inbound raw: %w", err)
	}
	return raw, nil
}

// nullText 把空字符串编码成 pgtype.Text 的 NULL（审计表里空 chat/event/message id 应落
// NULL 而非空串，查询时语义才正确）。
func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// ---- 安装路由 ----

type installationResolver struct{ q *db.Queries }

func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	raw, err := decodeTelegramRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	inst, err := r.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeTelegram),
		// 按 bot id 路由：每条轮询循环只服务一个 bot，(channel_type, app_id) 唯一索引
		// 恰好把 bot id 映射回它的安装记录。
		AppID: raw.BotID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	return engine.ResolvedInstallation{
		ID:              inst.ID,
		WorkspaceID:     inst.WorkspaceID,
		AgentID:         inst.AgentID,
		InstallerUserID: inst.InstallerUserID,
		Active:          inst.Status == "active",
		Platform:        inst,
	}, nil
}

// ---- 身份 ----

type identityResolver struct{ q *db.Queries }

func (r *identityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	binding, err := r.q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
		}
		return engine.ResolvedIdentity{}, err
	}
	// 有绑定记录不再等于"是成员"（表间没有外键约束、可能已被移出工作区）；重新校验一次。
	if _, err := r.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      binding.MulticaUserID,
		WorkspaceID: inst.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
		}
		return engine.ResolvedIdentity{}, err
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// ---- 去重 ----

type deduper struct{ q *db.Queries }

func (r *deduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.q.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 拿不到认领行 = 已被认领过 = 重复消息。
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return claim.ClaimToken, nil
}

func (r *deduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

func (r *deduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- 会话绑定 / 追加 ----

// chatSession 是 Telegram 适配器需要的共享引擎会话接口的窄面。把适配器挡在这条窄缝后面，
// 参数映射可以脱离数据库做单测。
type chatSession interface {
	EnsureSession(ctx context.Context, in engine.EnsureSessionInput) (pgtype.UUID, error)
	StartSession(ctx context.Context, in engine.StartSessionInput) (engine.StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendUserMessage(ctx context.Context, in engine.AppendInput) (engine.AppendResult, error)
	BindMediaRefs(ctx context.Context, in engine.BindMediaInput) error
}

// sessionBinder 把引擎的会话参数翻译成 Telegram 会话语义（见 telegramSessionRouting）。
type sessionBinder struct{ session chatSession }

func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config, _ := telegramSessionRouting(p.Message)
	return r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     bindingKey,
		BindingConfig:  config,
		ChatType:       p.Message.Source.ChatType,
	})
}

func (r *sessionBinder) StartSession(ctx context.Context, p engine.StartSessionParams) (engine.StartSessionResult, error) {
	bindingKey, config, replyThread := telegramSessionRouting(p.Message)
	result, err := r.session.StartSession(ctx, engine.StartSessionInput{
		EnsureSessionInput: engine.EnsureSessionInput{
			WorkspaceID: p.Installation.WorkspaceID, AgentID: p.Installation.AgentID,
			InstallationID: p.Installation.ID, Sender: p.Creator,
			BindingKey: bindingKey, BindingConfig: config, ChatType: p.Message.Source.ChatType,
		},
		Initiator: p.Sender,
		Body:      p.Message.Text, MessageID: p.Message.MessageID, ThreadID: replyThread,
		ClaimToken: p.ClaimToken, MediaPendingSeconds: p.MediaPendingSeconds,
		PersistMessage: p.PersistMessage, HistoryBoundaryPending: p.HistoryBoundaryPending,
		BeforeCommit: p.BeforeCommit,
	})
	return engine.StartSessionResult{
		SessionID: result.SessionID, BindingID: result.BindingID,
		RouteRevision: result.RouteRevision, Append: result.Append,
	}, err
}

func (r *sessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	_, _, replyThread := telegramSessionRouting(p.Message)
	commandText := p.Message.CommandText
	if commandText == "" {
		commandText = p.Message.Text
	}
	return r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:           p.SessionID,
		Sender:              p.Sender,
		InstallationID:      p.InstallationID,
		Body:                p.Message.Text,
		CommandText:         commandText,
		MessageID:           p.Message.MessageID,
		ThreadID:            replyThread,
		ClaimToken:          p.ClaimToken,
		MediaPendingSeconds: p.MediaPendingSeconds,
		ForceFresh:          p.Message.ForceFresh,
	})
}

func (r *sessionBinder) BindMedia(ctx context.Context, p engine.BindMediaParams) (engine.BindMediaResult, error) {
	in := engine.BindMediaInput{
		MessageID:   p.MessageID,
		SessionID:   p.SessionID,
		WorkspaceID: p.WorkspaceID,
		Sender:      p.Sender,
		MediaRefs:   p.MediaRefs,
	}
	// 探测增强接口：实现了 BindMediaRefsWithResult 就用它（能拿到结果），否则退到只写
	// 不读的基础版。这样窄面保持最小，富能力的实现不必削足适履。
	if richer, ok := r.session.(interface {
		BindMediaRefsWithResult(context.Context, engine.BindMediaInput) (engine.BindMediaResult, error)
	}); ok {
		return richer.BindMediaRefsWithResult(ctx, in)
	}
	return engine.BindMediaResult{}, r.session.BindMediaRefs(ctx, in)
}

// ---- 审计 ----

type auditor struct{ q *db.Queries }

func (r *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	raw, _ := decodeTelegramRaw(msg) // 尽力而为：Raw 解析失败也要把这次丢弃审计下来
	return r.q.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ID:               dbid.NewV7(),
		ChannelType:      string(TypeTelegram),
		EventType:        raw.EventType,
		DropReason:       string(reason),
		InstallationID:   instID,
		ChannelChatID:    nullText(msg.Source.ChatID),
		ChannelEventID:   nullText(msg.EventID),
		ChannelMessageID: nullText(msg.MessageID),
	})
}

// ---- 打字指示 ----

// typingNotifier 在消息被收入时展示 Telegram 原生的 "typing…" 聊天动作。该动作约 5 秒后
// 自动过期，所以不像 Slack 的回应表情那样需要收尾清理——OnSettled 是空操作。
type typingNotifier struct {
	decrypt Decrypter
	apiBase string
	client  *http.Client
	logger  *slog.Logger
}

// NewTypingNotifier 组装基于 sendChatAction 的打字指示器。
func NewTypingNotifier(decrypt Decrypter, apiBase string, client *http.Client, logger *slog.Logger) engine.TypingNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &typingNotifier{decrypt: decrypt, apiBase: apiBase, client: client, logger: logger}
}

// OnIngested 在消息被收入时点亮输入框旁的 "typing…"。失败只记日志（指示器是锦上添花，
// 不影响消息处理主链路）。
func (n *typingNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID) {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return
	}
	creds, err := decodeCredentials(row.Config, n.decrypt)
	if err != nil {
		n.logger.WarnContext(ctx, "telegram typing: decode credentials failed", "error", err)
		return
	}
	chatID, err := strconv.ParseInt(msg.Source.ChatID, 10, 64)
	if err != nil {
		return
	}
	var threadID int64
	if msg.Source.ThreadID != "" {
		threadID, _ = strconv.ParseInt(msg.Source.ThreadID, 10, 64)
	}
	if err := newBotAPI(n.apiBase, creds.BotToken, n.client).SendChatAction(ctx, chatID, threadID); err != nil {
		n.logger.WarnContext(ctx, "telegram typing: sendChatAction failed", "error", err)
	}
}

// OnSettled 空操作：sendChatAction 的 "typing…" 会在约 5 秒后自动消失，无需清理。
func (n *typingNotifier) OnSettled(ctx context.Context, sessionID pgtype.UUID) {}
