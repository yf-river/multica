// feishu_resolvers.go：飞书 ResolverSet——channel 无关的 engine.Router 跑入站
// 管线时需要的飞书侧平台实现。
//
// 每个 resolver 负责在引擎规范化的 channel.InboundMessage / engine 类型与飞书
// store / service 之间翻译。规范化信封没有携带的平台特有字段（app_id、
// event_type、create time）从原始 InboundMessage 读取——feishuChannel 会把它
// 塞进 channel.InboundMessage.Raw——这是文档化的适配器边界（core 从不读 Raw）。
package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// originFeishuChat 是经飞书 /issue 命令创建 issue 时写的 issue.origin_type
// 标签。保留为 "lark_chat"（与切换前 dispatcher 一致），让分析分类口径不漂移。
const originFeishuChat = "lark_chat"

// larkMsgFromRaw 解码 feishuChannel 塞在 channel.InboundMessage.Raw 里的原始
// 飞书 InboundMessage。
func larkMsgFromRaw(msg channel.InboundMessage) (InboundMessage, error) {
	var lm InboundMessage
	if len(msg.Raw) == 0 {
		return InboundMessage{}, errors.New("lark: inbound message Raw is empty")
	}
	if err := json.Unmarshal(msg.Raw, &lm); err != nil {
		return InboundMessage{}, fmt.Errorf("decode feishu inbound raw: %w", err)
	}
	return lm, nil
}

// NewFeishuResolverSet 由 store、共享会话服务、审计日志器和（可选的）出站
// replier + typing indicator 装配飞书 ResolverSet。飞书只是 channel 无关的
// engine.ChatSession 的又一个使用者——不存在飞书专属的会话实现。
func NewFeishuResolverSet(store *ChannelStore, session *engine.ChatSession, audit AuditLogger, replier OutcomeReplier, typing *TypingIndicatorManager, media engine.MediaResolver) engine.ResolverSet {
	set := engine.ResolverSet{
		Installation: &feishuInstallationResolver{store: store},
		Identity:     &feishuIdentityResolver{store: store},
		Dedup:        &feishuDeduper{store: store},
		Session:      &feishuSessionBinder{session: session},
		Audit:        &feishuAuditor{audit: audit},
		OriginType:   originFeishuChat,
	}
	if replier != nil {
		set.Replier = &feishuOutboundReplier{replier: replier}
	}
	if typing != nil {
		set.Typing = &feishuTypingNotifier{mgr: typing}
	}
	if media != nil {
		set.Media = media
	}
	return set
}

// ---- 安装路由 ----

// feishuInstallationResolver 按事件携带的 app_id 解析安装记录。
type feishuInstallationResolver struct{ store *ChannelStore }

// ResolveInstallation 实现 engine.InstallationResolver。
func (r *feishuInstallationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	lm, err := larkMsgFromRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	inst, err := r.store.GetLarkInstallationByAppID(ctx, lm.AppID)
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
		Active:          InstallationStatus(inst.Status) == InstallationActive,
		Platform:        inst,
	}, nil
}

// ---- 身份 ----

// feishuIdentityResolver 把发送者 open_id 解析成 workspace 成员。
type feishuIdentityResolver struct{ store *ChannelStore }

// ResolveSender 实现 engine.IdentityResolver。
func (r *feishuIdentityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	binding, err := r.store.GetLarkUserBindingByOpenID(ctx, GetUserBindingByOpenIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
		}
		return engine.ResolvedIdentity{}, err
	}
	isMember, err := r.store.IsWorkspaceMember(ctx, inst.WorkspaceID, binding.MulticaUserID)
	if err != nil {
		return engine.ResolvedIdentity{}, err
	}
	if !isMember {
		return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// ---- 去重 ----

// feishuDeduper 实现两阶段入站去重（claim/mark/release）。
type feishuDeduper struct{ store *ChannelStore }

// Claim 认领一条消息的去重行；已存在（重复）时返回 engine.ErrDuplicate。
func (r *feishuDeduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.store.ClaimLarkInboundDedup(ctx, ClaimInboundDedupParams{
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

// Mark 把已认领的消息标记为已处理。
func (r *feishuDeduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.store.MarkLarkInboundDedupProcessed(ctx, MarkInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// Release 在处理失败时释放认领。
func (r *feishuDeduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.store.ReleaseLarkInboundDedup(ctx, ReleaseInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- 会话绑定 / 追加 ----

// chatSession 是飞书会话绑定器要驱动的 engine.ChatSession 切片。声明成接口，
// 让（平台特有的）参数映射能用 fake 做单元测试；生产值是 *engine.ChatSession。
type chatSession interface {
	EnsureSession(ctx context.Context, in engine.EnsureSessionInput) (pgtype.UUID, error)
	StartSession(ctx context.Context, in engine.StartSessionInput) (engine.StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendUserMessage(ctx context.Context, in engine.AppendInput) (engine.AppendResult, error)
	BindMediaRefs(ctx context.Context, in engine.BindMediaInput) error
}

// StartSession 实现 engine.SessionResolver，用入站消息路由键开启新会话。
func (r *feishuSessionBinder) StartSession(ctx context.Context, p engine.StartSessionParams) (engine.StartSessionResult, error) {
	bindingKey, config := larkSessionRouting(p.Message)
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
	return engine.StartSessionResult{SessionID: result.SessionID, BindingID: result.BindingID, RouteRevision: result.RouteRevision, Append: result.Append}, err
}

// feishuSessionBinder 把引擎的会话操作翻译到 engine.ChatSession。
type feishuSessionBinder struct{ session chatSession }

// larkBindingConfig 是绑定键为复合形态（Lark 话题）时持久化在聊天绑定 config
// 上的不透明出站路由：真正的 chat id 存在这里，出站路径才能把回复发回去。
type larkBindingConfig struct {
	ChatID string `json:"chat_id"`
}

// larkSessionRouting 从一条入站飞书消息推导“会话隔离键”（存为
// channel_chat_id）与出站 config。p2p 或普通群聊里，一个聊天就是一个连续会话，
// 所以键就是 chat id、光靠键即可路由出站（无 config）。话题（话题，带
// thread_id）里的消息按话题隔离——键 = "chat:thread"——于是同一个群里的两个
// @bot 话题就是两个会话（与 Slack 的 channel:threadRoot 同一套模型；见
// engine.EnsureSessionInput）。纯函数，隔离契约无需 DB 即可单测。
func larkSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte) {
	chatID := msg.Source.ChatID
	if msg.Source.ChatType != channel.ChatTypeGroup || msg.Source.ThreadID == "" {
		return chatID, nil
	}
	cfg, _ := json.Marshal(larkBindingConfig{ChatID: chatID})
	return chatID + ":" + msg.Source.ThreadID, cfg
}

// EnsureSession 实现 engine.SessionResolver，确保会话存在。
func (r *feishuSessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config := larkSessionRouting(p.Message)
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

// MarkPendingFresh 实现 engine.SessionResolver。
func (r *feishuSessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

// AppendMessage 实现 engine.SessionResolver：把用户消息追加进会话。命令文本
// 优先取 CommandText，缺失时退回消息正文（裸文本消息没有前置快照）。
func (r *feishuSessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
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
		ThreadID:            p.Message.Source.ThreadID,
		ClaimToken:          p.ClaimToken,
		MediaPendingSeconds: p.MediaPendingSeconds,
		ForceFresh:          p.Message.ForceFresh,
	})
}

// BindMedia 实现 engine.SessionResolver：把媒体引用绑定到会话/issue。优先走
// 会话对象上更丰富的 BindMediaRefsWithResult（返回结果），否则退回无结果的
// BindMediaRefs。
func (r *feishuSessionBinder) BindMedia(ctx context.Context, p engine.BindMediaParams) (engine.BindMediaResult, error) {
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

// ---- 审计 ----

// feishuAuditor 把引擎的丢弃事件翻译成飞书审计参数。
type feishuAuditor struct{ audit AuditLogger }

// RecordDrop 实现 engine.AuditResolver。
func (r *feishuAuditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	// event_type 是平台特有字段（从 Raw 读）；解码失败不致命——没有它这次丢弃
	// 仍值得审计。
	lm, _ := larkMsgFromRaw(msg)
	return r.audit.RecordDrop(ctx, AuditDropParams{
		InstallationID: instID,
		ChatID:         ChatID(msg.Source.ChatID),
		EventType:      lm.EventType,
		LarkEventID:    msg.EventID,
		LarkMessageID:  msg.MessageID,
		Reason:         DropReason(string(reason)),
	})
}

// ---- 出站 replier ----

// feishuOutboundReplier 把引擎的结果翻译成飞书 DispatchResult 交给出站
// replier。
type feishuOutboundReplier struct{ replier OutcomeReplier }

// Reply 实现 engine.ReplierResolver。
func (r *feishuOutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	larkInst, ok := inst.Platform.(Installation)
	if !ok {
		return
	}
	lm, _ := larkMsgFromRaw(msg)
	r.replier.Reply(ctx, larkInst, lm, dispatchResultFromEngine(res))
}

// dispatchResultFromEngine 把引擎判定映射成 OutcomeReplier 消费的飞书
// DispatchResult。Outcome / DropReason 字符串取值一一对应。
func dispatchResultFromEngine(res engine.Result) DispatchResult {
	return DispatchResult{
		Outcome:            Outcome(string(res.Outcome)),
		DropReason:         DropReason(string(res.DropReason)),
		InstallationID:     res.InstallationID,
		ChatSessionID:      res.ChatSessionID,
		SenderOpenID:       OpenID(res.Sender),
		IssueID:            res.IssueID,
		IssueNumber:        res.IssueNumber,
		IssueIdentifier:    res.IssueIdentifier,
		IssueTitle:         res.IssueTitle,
		IssueDuplicate:     res.IssueDuplicate,
		IssueUsageHadMedia: res.IssueUsageHadMedia,
	}
}

// ---- typing indicator ----

// feishuTypingNotifier 在消息被摄入 / 运行尘埃落定时驱动飞书 Typing 反应。
type feishuTypingNotifier struct{ mgr *TypingIndicatorManager }

// OnIngested 实现 engine.TypingResolver：消息被摄入后加反应。
func (r *feishuTypingNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID) {
	larkInst, ok := inst.Platform.(Installation)
	if !ok {
		return
	}
	lm, _ := larkMsgFromRaw(msg)
	r.mgr.Add(ctx, larkInst, sessionID, msg.MessageID, lm.CreateTime)
}

// OnSettled 在运行触发没入队任何任务时清掉反应（agent 离线/归档，或入队
// 失败）——这种情形下 Patcher 由事件驱动的 chat-done / task-failed 清理永不
// 触发，没有这一步 Typing 反应就会一直挂着。
func (r *feishuTypingNotifier) OnSettled(ctx context.Context, sessionID pgtype.UUID) {
	r.mgr.Clear(ctx, sessionID)
}
