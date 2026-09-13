// resolvers.go：把与渠道无关的入站流水线与 Slack 安装路由、身份绑定、去重、
// 会话持久化、审计、反应、回复、附件摄取接起来的 resolver 集合。共享渠道状态
// 全部落在通用 channel_* 表上。
//
// 各流水线阶段的 Slack 实现在这里组装成一个 engine.ResolverSet；会话层用
// 「bindingKey + config」约定来做隔离与出站路由（见 slackSessionRouting）。
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// originSlackChat 是经 Slack /issue 命令建出的 issue 的 origin_type 标签。
const originSlackChat = "slack_chat"

// NewSlackResolverSet 装配入站流水线每个阶段的 Slack 实现。replier 负责绑定引导
// 与状态提示、typing 管理「处理中」反应、media 落盘入站附件。每个可选依赖都可
// 以传 nil 只关掉那一项能力，不影响普通 Slack 消息摄取的正常流转。
func NewSlackResolverSet(q *db.Queries, tx engine.TxStarter, replier engine.OutboundReplier, typing *TypingIndicatorManager, media engine.MediaResolver) engine.ResolverSet {
	set := engine.ResolverSet{
		Installation: &installationResolver{q: q},
		Identity:     &identityResolver{q: q},
		Dedup:        &deduper{q: q},
		Session: &sessionBinder{session: engine.NewChatSession(q, tx, TypeSlack, engine.SessionTitles{
			Group:    "Slack channel",
			Direct:   "Slack direct message",
			Fallback: "Slack chat",
		})},
		Audit:      &auditor{q: q},
		Media:      media,
		Replier:    replier,
		OriginType: originSlackChat,
	}
	// 只有拿到的是具体 manager 才赋接口。若把一个「类型化 nil」塞进接口字段，
	// 接口本身会判成非 nil，流水线调用时就会崩。
	if typing != nil {
		set.Typing = &slackTypingNotifier{mgr: typing}
	}
	return set
}

var (
	_ engine.InstallationResolver = (*installationResolver)(nil)
	_ engine.IdentityResolver     = (*identityResolver)(nil)
	_ engine.Deduper              = (*deduper)(nil)
	_ engine.SessionBinder        = (*sessionBinder)(nil)
	_ engine.Auditor              = (*auditor)(nil)
	_ engine.TypingNotifier       = (*slackTypingNotifier)(nil)
)

// slackBindingConfig 是持久化在聊天绑定 config 上的出站路由信息。当绑定键是
// 复合形态（Slack 频道内的某个线程）时，真实频道 id 放在这里，出站路径才能投回
// 正确的会话。
type slackBindingConfig struct {
	ChannelID string `json:"channel_id"`
}

// slackSessionRouting 从一条入站 Slack 消息推导出会话层需要分开维护的三样东西
// （Elon 第二轮必改项）：
//
//   - bindingKey：会话隔离键（存成 channel_chat_id）。私聊按频道就是一条连续的
//     会话，所以键就是频道 id。频道/群消息则按【线程根】隔离——键 =
//     "channel:root"——于是同一频道里的两个 @bot 线程是两段会话，与 Hermes 一致。
//     线程根是：在线程里回复时取入站 thread_ts，否则取消息自身 ts（一条顶层的
//     @ 提及会开一个新的根）。
//   - config：真实频道 id，绑定键是复合形态时出站也能工作。
//   - replyThread：回复要挂的 thread_ts（群取线程根；私聊取入站线程，顶层发送
//     时可能为空）。
//
// 做成纯函数，隔离契约无需 DB 就能做单测。
func slackSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte, replyThread string) {
	chatID := msg.Source.ChatID
	cfg, _ := json.Marshal(slackBindingConfig{ChannelID: chatID})
	if msg.Source.ChatType == channel.ChatTypeP2P {
		return chatID, cfg, msg.Source.ThreadID
	}
	// 线程根在 @ 提及是「既有线程里的回复」时取入站 thread_ts，否则取消息自身的
	// ts（顶层提及成为 bot 回复所挂的根）。无论哪种，根之后都能从绑定里找回来
	// （channel_chat_id 的后缀 / last_thread_id）——历史读取器正是靠它去读线程的。
	threadRoot := msg.Source.ThreadID
	if threadRoot == "" {
		threadRoot = msg.MessageID
	}
	return chatID + ":" + threadRoot, cfg, threadRoot
}

// decodeSlackRaw 把入站消息里打好的 Slack 原始信封解出来。消息没有 Raw 或解析
// 失败都算错误——resolver 侧宁可显式失败也不拿空结构硬扛。
func decodeSlackRaw(msg channel.InboundMessage) (slackRawEvent, error) {
	var raw slackRawEvent
	if len(msg.Raw) == 0 {
		return slackRawEvent{}, errors.New("slack: inbound message Raw is empty")
	}
	if err := json.Unmarshal(msg.Raw, &raw); err != nil {
		return slackRawEvent{}, fmt.Errorf("decode slack inbound raw: %w", err)
	}
	return raw, nil
}

// nullText 把空串转成「NULL」的 pgtype.Text，非空则带上值；入库时可空列的统一
// 写法。
func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// installTeamID 从存库的安装 config 里读出真实 Slack team id，读不到/解不开就
// 返回 ""。与 decodeCredentials / DecodePublicConfig 不同，它【不】回退到
// app_id：team 路由与身份复用必须对上真实的 Slack 工作区，而 BYO 应用的
// app_id != team_id。
func installTeamID(installConfigJSON json.RawMessage) string {
	var cfg installConfig
	_ = json.Unmarshal(installConfigJSON, &cfg)
	return cfg.TeamID
}

// installationServesTeam 判断某安装（按它存的 config）能否服务 eventTeamID 的
// 事件。入站路由按 api_app_id 走，而 api_app_id 标识的是 Slack【应用】不是
// Slack【工作区】：一个 BYO 应用若被分发/安装进另一个 Slack 工作区，它发的事件
// 会带着【同一个】 app id。所以这里额外要求事件的 team 与已装 bot 所属的 team
// 一致。老安装没记 team（历史数据）时采取宽松放行。
func installationServesTeam(installConfigJSON json.RawMessage, eventTeamID string) bool {
	teamID := installTeamID(installConfigJSON)
	return teamID == "" || teamID == eventTeamID
}

// ---- 安装路由 ----

type installationResolver struct{ q *db.Queries }

// ResolveInstallation 按入站事件里的 api_app_id 查回安装，并校验事件 team 归属。
func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	raw, err := decodeSlackRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	inst, err := r.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeSlack),
		// 按事件的 api_app_id 路由：每份 BYO 安装都把真实 Slack app id 存进路由
		// 键槽（config->>'app_id'），而按安装建立的 Socket Mode 连接只投递自家
		// 应用的事件，所以 api_app_id 能唯一确定安装。
		AppID: raw.APIAppID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	if !installationServesTeam(inst.Config, raw.TeamID) {
		return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
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

// ---- 身份解析 ----

// identityQueries 是 identityResolver 需要的生成查询子集。抽成接口（而非
// *db.Queries）是为了跨安装复用路径能用假实现做单测（与 slashQueries 同理）。
// *db.Queries 满足它。
type identityQueries interface {
	GetChannelUserBindingByUserID(ctx context.Context, arg db.GetChannelUserBindingByUserIDParams) (db.ChannelUserBinding, error)
	FindReusableChannelUserBinding(ctx context.Context, arg db.FindReusableChannelUserBindingParams) (db.ChannelUserBinding, error)
	GetMemberByUserAndWorkspace(ctx context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
	CreateChannelUserBinding(ctx context.Context, arg db.CreateChannelUserBindingParams) (db.ChannelUserBinding, error)
}

type identityResolver struct{ q identityQueries }

// ResolveSender 把 Slack 发送者解析成 Multica 用户：先查本安装的直接绑定，没有
// 就尝试复用同工作区、同 team 下其它安装的绑定（见 reusableBinding），最后再验
// 成员资格。
func (r *identityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	senderID := msg.Source.SenderID
	binding, err := r.q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  senderID,
	})
	reused := false
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedIdentity{}, err
		}
		// 没绑到【当前这个】安装。在提示绑定之前，先试试复用同一 Slack 用户对
		// 本工作区里【另一个】同 team 安装做过的绑定（MUL-3911）：每 Slack 工作
		// 区只绑一次，而不是每个应用绑一次。
		cand, ok, ferr := r.reusableBinding(ctx, inst, senderID)
		if ferr != nil {
			return engine.ResolvedIdentity{}, ferr
		}
		if !ok {
			return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
		}
		binding, reused = cand, true
	}
	// 绑定存在不再证明成员资格（没有外键），需要复核。对复用的绑定这一步同时
	// 也是落地门槛：绝不替一个已经离开工作区的用户持久化绑定。
	if _, err := r.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      binding.MulticaUserID,
		WorkspaceID: inst.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if reused {
				// 同一人、但已不是成员：提示他重新绑定而不是对一个他从未在这个
				// 应用上绑过的身份报「不是成员」。
				return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
			}
			return engine.ResolvedIdentity{}, engine.ErrSenderNotMember
		}
		return engine.ResolvedIdentity{}, err
	}
	if reused {
		// 把复用到的绑定「物化」成当前安装自己的绑定行：此后消息走快的按安装
		// 路径解析，也会像其它绑定一样随成员一起被清理。ON CONFLICT 保证幂等；
		// 并发首条消息抢先写好的情况下返回同一行。
		if _, err := r.q.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
			WorkspaceID:    inst.WorkspaceID,
			MulticaUserID:  binding.MulticaUserID,
			InstallationID: inst.ID,
			ChannelType:    string(TypeSlack),
			ChannelUserID:  senderID,
			Config:         []byte(`{}`),
		}); err != nil {
			return engine.ResolvedIdentity{}, fmt.Errorf("materialize reused slack binding: %w", err)
		}
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// reusableBinding 找同一 Slack 用户对【另一个】安装——必须同工作区 + 同 Slack
// team——做过的绑定，让一个 Slack 工作区里的第二个应用不必再让用户重新绑定
// （MUL-3911）。ok=false（且无错误）表示「没有可复用的，提示绑定」：安装没记
// team（老数据）、Platform 不是 ChannelInstallation、或没有匹配的绑定行。
func (r *identityResolver) reusableBinding(ctx context.Context, inst engine.ResolvedInstallation, senderID string) (db.ChannelUserBinding, bool, error) {
	ci, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return db.ChannelUserBinding{}, false, nil
	}
	teamID := installTeamID(ci.Config)
	if teamID == "" {
		return db.ChannelUserBinding{}, false, nil
	}
	cand, err := r.q.FindReusableChannelUserBinding(ctx, db.FindReusableChannelUserBindingParams{
		WorkspaceID:   inst.WorkspaceID,
		ChannelType:   string(TypeSlack),
		ChannelUserID: senderID,
		TeamID:        teamID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelUserBinding{}, false, nil
		}
		return db.ChannelUserBinding{}, false, err
	}
	return cand, true, nil
}

// ---- 去重 ----

// deduper 基于 channel_* 去重表实现引擎的 Deduper 契约。
type deduper struct{ q *db.Queries }

// Claim 尝试占住 (installation, message) 的去重槽；已经占过的（重复消息）返回
// engine.ErrDuplicate。
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

// ---- 会话绑定/追加 ----

// sessionBinder 把引擎的会话契约转发给引擎 ChatSession，参数由
// slackSessionRouting 换算（bindingKey/config/回复线程）。
type sessionBinder struct{ session *engine.ChatSession }

func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config, _ := slackSessionRouting(p.Message)
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
	bindingKey, config, replyThread := slackSessionRouting(p.Message)
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
	return engine.StartSessionResult{SessionID: result.SessionID, BindingID: result.BindingID, RouteRevision: result.RouteRevision, Append: result.Append}, err
}

func (r *sessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	_, _, replyThread := slackSessionRouting(p.Message)
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
	return r.session.BindMediaRefsWithResult(ctx, engine.BindMediaInput{
		MessageID:            p.MessageID,
		SessionID:            p.SessionID,
		WorkspaceID:          p.WorkspaceID,
		Sender:               p.Sender,
		IssueID:              p.IssueID,
		IssueDescriptionBase: p.IssueDescriptionBase,
		IssueCommandText:     p.IssueCommandText,
		Body:                 p.Body,
		MediaRefs:            p.MediaRefs,
	})
}

// ---- 审计 ----

// auditor 把入站丢弃事件记进 channel_* 丢弃审计表。
type auditor struct{ q *db.Queries }

func (r *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	raw, _ := decodeSlackRaw(msg) // event_type 是尽力而为：解不开也照记这次丢弃
	return r.q.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ID:               dbid.NewV7(),
		ChannelType:      string(TypeSlack),
		EventType:        raw.EventType,
		DropReason:       string(reason),
		InstallationID:   instID,
		ChannelChatID:    nullText(msg.Source.ChatID),
		ChannelEventID:   nullText(msg.EventID),
		ChannelMessageID: nullText(msg.MessageID),
	})
}

// ---- 打字指示（处理中反应） ----

// slackTypingNotifier 把引擎的 TypingNotifier 契约转发给 TypingIndicatorManager。
type slackTypingNotifier struct{ mgr *TypingIndicatorManager }

// OnIngested 在 Slack 消息成功摄取后触发：给用户消息打上反应（频道 = Source.
// ChatID，ts = MessageID），让用户看到 bot 正在处理。解析好的安装把 bot 令牌装在
// Config blob 里——InstallationResolver 把 db.ChannelInstallation 行塞进了
// Platform，这是文档约定的适配器边界，核心层从不读它。
func (n *slackTypingNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID) {
	ci, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return
	}
	n.mgr.Add(ctx, ci, sessionID, msg.Source.ChatID, msg.MessageID)
}

// OnSettled 在「这次运行没能投出任务」时清掉反应（agent 离线/归档，或投递失败）——
// 这些情形不会广播 chat-done / task-failed，总线驱动的清理永远不触发，不在这里
// 摘的话 👀 就永久粘住了。
func (n *slackTypingNotifier) OnSettled(ctx context.Context, sessionID pgtype.UUID) {
	n.mgr.Clear(ctx, sessionID)
}
