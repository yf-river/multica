// resolvers.go：钉钉的 ResolverSet——把渠道无关的 engine.Router 入站管道接到
// 钉钉特有查询上的一组平台缝。
//
// 角色：Router 处理一条入站消息时依次调用这些 resolver：安装反查（盖了 AppKey 的
// 信封 → channel_installation）、发送者身份（绑定 + 工作区成员复查）、消息去重、
// 会话绑定与追加、丢弃审计。全部只建在通用 channel_* 查询与共享 engine.ChatSession
// 之上，镜像飞书与 Slack 的 ResolverSet。
//
// 群存在性元数据：额外带一个 groupPresenceObserver，在消息通过「@ 到了机器人 +
// 成员身份」门槛后，把「机器人在哪个群、群里叫什么」记进产品元数据表。
package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// originDingTalkChat 是经钉钉 /issue 命令建出的 issue 的 origin_type 标签。要与
// 数据库 CHECK 约束保持同步（与已有的 lark_chat / slack_chat 渠道 origin 一致）。
const originDingTalkChat = "dingtalk_chat"

// NewDingTalkResolverSet 基于生成查询 + tx 启动器（供共享 session 服务用）装配
// 钉钉 ResolverSet。replier 负责投递出站的绑定引导/状态/issue 已建等通知，传 nil
// 即停用。经典机器人发送 API 没有按消息的 reaction，ack 通知器顶替打字指示灯
// （摄取时发一条「正在处理」），传 nil 即停用。media 可选：配置后走共享
// MediaResolver 与媒体意图台账管道。
func NewDingTalkResolverSet(q *db.Queries, tx engine.TxStarter, replier engine.OutboundReplier, ack *ackNotifier, media engine.MediaResolver, botNames *BotNameResolver) engine.ResolverSet {
	groupPresence := &groupPresenceObserver{
		q:        q,
		botNames: botNames,
		logger:   slog.Default(),
	}
	set := engine.ResolverSet{
		Installation: &installationResolver{q: q},
		Identity:     &identityResolver{q: q},
		Dedup:        &deduper{q: q},
		Session: &sessionBinder{session: engine.NewChatSession(q, tx, TypeDingTalk, engine.SessionTitles{
			Group:    "DingTalk group",
			Direct:   "DingTalk direct message",
			Fallback: "DingTalk chat",
		}), groupPresence: groupPresence},
		Audit:      &auditor{q: q},
		Replier:    replier,
		OriginType: originDingTalkChat,
	}
	// 防的是把 nil *ackNotifier 塞进接口字段（那样 set.Typing 会变成一个非 nil 的
	// 类型化 nil，判 nil 判不出来）；与 Slack/飞书同款防护。
	if ack != nil {
		set.Typing = ack
	}
	if media != nil {
		set.Media = media
	}
	return set
}

var (
	_ engine.InstallationResolver = (*installationResolver)(nil)
	_ engine.IdentityResolver     = (*identityResolver)(nil)
	_ engine.Deduper              = (*deduper)(nil)
	_ engine.SessionBinder        = (*sessionBinder)(nil)
	_ engine.Auditor              = (*auditor)(nil)
)

// dingtalkBindingConfig 是聊天绑定 config 上持久化的不透明出站路由：足够把一条主动
// 回复寻址回来源会话。StaffID 是 1:1 私聊的唯一收件人；群聊里它为空（群用
// conversation id 寻址）。
type dingtalkBindingConfig struct {
	ConversationType string `json:"conversation_type"`
	ConversationID   string `json:"conversation_id"`
	StaffID          string `json:"staff_id,omitempty"`
}

// dingtalkSessionRouting 从一条入站消息推导「会话隔离键」与出站路由 config。钉钉没
// 有线程，所以一个会话（私聊或群聊）就是一条持续会话，用 conversation id 做键；
// config 带上出站路径回发所需的一切。
func dingtalkSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte) {
	chatID := msg.Source.ChatID
	cfg := dingtalkBindingConfig{
		ConversationType: convTypeGroup,
		ConversationID:   chatID,
	}
	if msg.Source.ChatType == channel.ChatTypeP2P {
		cfg.ConversationType = convTypeP2P
		cfg.StaffID = msg.Source.SenderID
	}
	raw, _ := json.Marshal(cfg)
	return chatID, raw
}

// outboundTarget 从聊天绑定的 config 还原出投递目标；config 缺失或不可解析时回退
// 到 channel_chat_id（当群聊发）。
func outboundTarget(b db.ChannelChatSessionBinding) sendTarget {
	target := sendTarget{ConversationType: convTypeGroup, ConversationID: b.ChannelChatID}
	if len(b.Config) > 0 {
		var cfg dingtalkBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil {
			if cfg.ConversationType != "" {
				target.ConversationType = cfg.ConversationType
			}
			if cfg.ConversationID != "" {
				target.ConversationID = cfg.ConversationID
			}
			target.StaffID = cfg.StaffID
		}
	}
	return target
}

// decodeDingTalkRaw 把消息 Raw 里的钉钉特有信封解回来；Raw 为空或解不动都算错误。
func decodeDingTalkRaw(msg channel.InboundMessage) (dingtalkRawEvent, error) {
	var raw dingtalkRawEvent
	if len(msg.Raw) == 0 {
		return dingtalkRawEvent{}, errors.New("dingtalk: inbound message Raw is empty")
	}
	if err := json.Unmarshal(msg.Raw, &raw); err != nil {
		return dingtalkRawEvent{}, fmt.Errorf("decode dingtalk inbound raw: %w", err)
	}
	return raw, nil
}

// nullText 把空串映射成无效的 pgtype.Text（落库为 NULL），非空映射成有效值。
func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// ---- 安装反查：AppKey → 安装 ----

type installationResolver struct{ q *db.Queries }

// ResolveInstallation 用接收连接盖在信封上的 AppKey 反查安装行。每份安装各有一条
// Stream 连接，盖上的 AppKey 就能唯一确定安装（钉钉回调本身不带 robotCode）。
func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	inst, err := r.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeDingTalk),
		AppID:       raw.AppID,
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

// ---- 发送者身份 ----

type identityResolver struct{ q *db.Queries }

// ResolveSender 把发送者的钉钉 staff id 映射成 Multica 用户。先查该安装下的用户绑
// 定；绑定存在后还要再查一次工作区成员关系——本系统没有成员外键，绑定存在并不能
// 证明成员资格，必须显式复查。
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
	// 绑定的存在不再证明成员资格（没有外键）；显式复查工作区成员。
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

// ---- 消息去重 ----

type deduper struct{ q *db.Queries }

// Claim 尝试领取一条消息的去重 claim：领到返回 claim token；重复消息（已处理/处理
// 中）返回 engine.ErrDuplicate。
func (r *deduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.q.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return claim.ClaimToken, nil
}

// Mark 把已领取的消息标记为处理完成。
func (r *deduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// Release 释放一条已领取但最终失败的消息 claim，让它能被重新投递（或过期清理）。
func (r *deduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- 会话绑定 / 消息追加 ----

type chatSession interface {
	EnsureSession(ctx context.Context, in engine.EnsureSessionInput) (pgtype.UUID, error)
	StartSession(ctx context.Context, in engine.StartSessionInput) (engine.StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendUserMessage(ctx context.Context, in engine.AppendInput) (engine.AppendResult, error)
	BindMediaRefs(ctx context.Context, in engine.BindMediaInput) error
}

type groupPresenceQueries interface {
	UpsertDingTalkBotIdentity(ctx context.Context, arg db.UpsertDingTalkBotIdentityParams) (pgtype.UUID, error)
	RecordDingTalkGroupPresence(ctx context.Context, arg db.RecordDingTalkGroupPresenceParams) (pgtype.UUID, error)
	RecordDingTalkGroupActivity(ctx context.Context, arg db.RecordDingTalkGroupActivityParams) (pgtype.UUID, error)
}

// groupPresenceObserver 在消息通过「群内 @ 到了机器人 + 发送者成员身份」两道门槛
// 之后，把产品元数据落库：机器人在这个群里的可读身份 + 群的「见过」记录。
type groupPresenceObserver struct {
	q        groupPresenceQueries
	botNames *BotNameResolver
	logger   *slog.Logger
}

// Observe 为一条已被受理的群消息记录群存在性：upsert 机器人身份行（含缺权限标记），
// 再记录该群出现过。只在群消息上动作。
func (o *groupPresenceObserver) Observe(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) error {
	if o == nil || o.q == nil || msg.Source.ChatType != channel.ChatTypeGroup {
		return nil
	}
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return err
	}
	botName := ""
	botIdentityIssue := ""
	if o.botNames != nil {
		if installation, ok := inst.Platform.(db.ChannelInstallation); ok {
			botName, err = o.botNames.Resolve(ctx, installation, msg.Source.ChatID)
			if err != nil {
				if errors.Is(err, errMissingChatManagePermission) {
					botIdentityIssue = botIdentityIssueMissingChatManage
				}
				logger := o.logger
				if logger == nil {
					logger = slog.Default()
				}
				logger.WarnContext(ctx, "dingtalk: could not resolve readable bot identity",
					"installation_id", inst.ID,
					"conversation_id", msg.Source.ChatID,
					"error", err,
				)
				botName = ""
			}
		}
	}
	_, err = o.q.UpsertDingTalkBotIdentity(ctx, db.UpsertDingTalkBotIdentityParams{
		BotName:          botName,
		BotIdentityIssue: botIdentityIssue,
		InstallationID:   inst.ID,
		WorkspaceID:      inst.WorkspaceID,
	})
	if err != nil {
		return err
	}
	_, err = o.q.RecordDingTalkGroupPresence(ctx, db.RecordDingTalkGroupPresenceParams{
		ConversationID:    msg.Source.ChatID,
		ConversationTitle: raw.ConversationTitle,
		InstallationID:    inst.ID,
		WorkspaceID:       inst.WorkspaceID,
	})
	return err
}

// RecordActivity 只在 AppendUserMessage 提交成功后推进活跃计数。存在性发现跑得更
// 早（在寻址与成员校验之后），但若在那里计数，会把「后续落库失败」的消息也计进来。
func (o *groupPresenceObserver) RecordActivity(ctx context.Context, installationID pgtype.UUID, msg channel.InboundMessage) error {
	if o == nil || o.q == nil || msg.Source.ChatType != channel.ChatTypeGroup {
		return nil
	}
	_, err := o.q.RecordDingTalkGroupActivity(ctx, db.RecordDingTalkGroupActivityParams{
		InstallationID: installationID,
		ConversationID: msg.Source.ChatID,
	})
	return err
}

// StartSession 处理开启一段新会话（会话首条消息）：确保会话行并追加首条消息，再记
// 群存在性，消息确实落库后推进活跃计数。
func (r *sessionBinder) StartSession(ctx context.Context, p engine.StartSessionParams) (engine.StartSessionResult, error) {
	bindingKey, config := dingtalkSessionRouting(p.Message)
	result, err := r.session.StartSession(ctx, engine.StartSessionInput{
		EnsureSessionInput: engine.EnsureSessionInput{
			WorkspaceID: p.Installation.WorkspaceID, AgentID: p.Installation.AgentID,
			InstallationID: p.Installation.ID, Sender: p.Creator,
			BindingKey: bindingKey, BindingConfig: config, ChatType: p.Message.Source.ChatType,
		},
		Initiator: p.Sender,
		Body:      p.Message.Text, MessageID: p.Message.MessageID, ThreadID: p.Message.Source.ThreadID,
		ClaimToken: p.ClaimToken, MediaPendingSeconds: p.MediaPendingSeconds,
		PersistMessage: p.PersistMessage, HistoryBoundaryPending: p.HistoryBoundaryPending,
		BeforeCommit: p.BeforeCommit,
	})
	if err != nil {
		return engine.StartSessionResult{}, err
	}
	// 群存在性只是尽力而为的元数据，失败绝不让一条合法群消息变成失败。
	if err := r.groupPresence.Observe(ctx, p.Installation, p.Message); err != nil {
		logger := slog.Default()
		if r.groupPresence != nil && r.groupPresence.logger != nil {
			logger = r.groupPresence.logger
		}
		logger.WarnContext(ctx, "dingtalk: could not record group presence",
			"installation_id", p.Installation.ID,
			"conversation_id", p.Message.Source.ChatID,
			"error", err,
		)
	}
	if p.PersistMessage {
		if err := r.groupPresence.RecordActivity(ctx, p.Installation.ID, p.Message); err != nil {
			logger := slog.Default()
			if r.groupPresence != nil && r.groupPresence.logger != nil {
				logger = r.groupPresence.logger
			}
			logger.WarnContext(ctx, "dingtalk: could not record group activity",
				"installation_id", p.Installation.ID,
				"conversation_id", p.Message.Source.ChatID,
				"error", err,
			)
		}
	}
	return engine.StartSessionResult{
		SessionID: result.SessionID, BindingID: result.BindingID,
		RouteRevision: result.RouteRevision, Append: result.Append,
	}, nil
}

// sessionBinder 是 engine.SessionBinder 的钉钉实现，把所有入站会话/追加请求转交给
// 共享 engine.ChatSession，并在关键节点附带群存在性元数据。
type sessionBinder struct {
	session       chatSession
	groupPresence *groupPresenceObserver
}

// EnsureSession 确保会话行存在（沿用现有会话），返回会话 id。
func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config := dingtalkSessionRouting(p.Message)
	sessionID, err := r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     bindingKey,
		BindingConfig:  config,
		ChatType:       p.Message.Source.ChatType,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	// 群清单是尽力而为的元数据。查询或写入失败绝不能把一条合法的群 @ 消息变成失败
	// 的聊天消息。
	if err := r.groupPresence.Observe(ctx, p.Installation, p.Message); err != nil {
		logger := slog.Default()
		if r.groupPresence != nil && r.groupPresence.logger != nil {
			logger = r.groupPresence.logger
		}
		logger.WarnContext(ctx, "dingtalk: could not record group presence",
			"installation_id", p.Installation.ID,
			"conversation_id", p.Message.Source.ChatID,
			"error", err,
		)
	}
	return sessionID, nil
}

// MarkPendingFresh 透传给共享 chatSession 记录一条待生效标记（参数原样转发）。
func (r *sessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

// AppendMessage 把一条后续消息追加进现有会话（含去重 mark）。落库成功后顺手推进群
// 活跃计数。
func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	commandText := p.Message.CommandText
	if commandText == "" {
		commandText = p.Message.Text
	}
	result, err := r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:           p.SessionID,
		Sender:              p.Sender,
		InstallationID:      p.InstallationID,
		Body:                p.Message.Text,
		CommandText:         commandText,
		MessageID:           p.Message.MessageID,
		ThreadID:            p.Message.Source.ThreadID,
		ClaimToken:          p.ClaimToken,
		MediaPendingSeconds: p.MediaPendingSeconds,
		ForceFresh:          p.Message.ForceFresh,
	})
	if err != nil {
		return engine.AppendResult{}, err
	}
	// 群清单是尽力而为的产品元数据。消息与去重 mark 已经落库，计数写入失败绝不能
	// 反过来释放 claim 或触发钉钉重试——那会带来一次重复的用户可见轮次。
	if err := r.groupPresence.RecordActivity(ctx, p.InstallationID, p.Message); err != nil {
		logger := slog.Default()
		if r.groupPresence != nil && r.groupPresence.logger != nil {
			logger = r.groupPresence.logger
		}
		logger.WarnContext(ctx, "dingtalk: could not record group activity",
			"installation_id", p.InstallationID,
			"conversation_id", p.Message.Source.ChatID,
			"error", err,
		)
	}
	return result, nil
}

// BindMedia 把已上传的媒体引用绑到消息/issue 上。共享 session 若实现了带返回值
// 的绑定接口就走它，否则退化为纯副作用调用。
func (r *sessionBinder) BindMedia(ctx context.Context, p engine.BindMediaParams) (engine.BindMediaResult, error) {
	in := engine.BindMediaInput{
		MessageID:            p.MessageID,
		SessionID:            p.SessionID,
		WorkspaceID:          p.WorkspaceID,
		Sender:               p.Sender,
		IssueID:              p.IssueID,
		IssueDescriptionBase: p.IssueDescriptionBase,
		IssueCommandText:     p.IssueCommandText,
		Body:                 p.Body,
		MediaRefs:            p.MediaRefs,
	}
	if richer, ok := r.session.(interface {
		BindMediaRefsWithResult(context.Context, engine.BindMediaInput) (engine.BindMediaResult, error)
	}); ok {
		return richer.BindMediaRefsWithResult(ctx, in)
	}
	return engine.BindMediaResult{}, r.session.BindMediaRefs(ctx, in)
}

// ---- 丢弃审计 ----

type auditor struct{ q *db.Queries }

// RecordDrop 把一条被引擎拒绝的入站消息记进丢弃审计表（含丢弃原因）。
func (r *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	return r.q.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ID:               dbid.NewV7(),
		ChannelType:      string(TypeDingTalk),
		EventType:        "message",
		DropReason:       string(reason),
		InstallationID:   instID,
		ChannelChatID:    nullText(msg.Source.ChatID),
		ChannelEventID:   nullText(msg.EventID),
		ChannelMessageID: nullText(msg.MessageID),
	})
}
