// wecom_resolvers.go —— 入站 channel_type 为 "wecom" 时 engine.Router 路由所经
// 的那套 ResolverSet。每个接口方法都在引擎规范化的 channel.InboundMessage 与
// wecom 的 store / 服务之间做翻译。规范化信封不带平台特有字段（BotID、发送者
// userid），它们取自塞在 channel.InboundMessage.Raw 里的 wecom InboundMessage。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// originWecomChat 是经 wecom /issue 命令创建的任务所写的 issue.origin_type 标
// 签。沿用 "lark_chat" 的模式（平台名 + "_chat"），好让分析侧保持同一套来源家族。
const originWecomChat = "wecom_chat"

// wecomMsgFromRaw 从 channel.InboundMessage.Raw 解码 wecom 侧的 InboundMessage。
// 每个解析器最后都会至少解一次；把 JSON tag 收拢在这里，Raw 形状一变就是单文件
// 修改。
func wecomMsgFromRaw(msg channel.InboundMessage) (InboundMessage, error) {
	if len(msg.Raw) == 0 {
		return InboundMessage{}, errors.New("wecom: inbound message Raw is empty")
	}
	var wm InboundMessage
	if err := json.Unmarshal(msg.Raw, &wm); err != nil {
		return InboundMessage{}, fmt.Errorf("wecom: decode inbound raw: %w", err)
	}
	return wm, nil
}

// NewResolverSet 由 store、共享的会话服务和一个出站回复器拼出 wecom 的
// ResolverSet。wecom 没有「正在输入」这种提示能力，所以 Typing 留 nil——Router
// 把 nil Typing 当成无操作。
//
// replier 可选：传 nil 即关闭出站绑定引导。media 也可选：没配对象存储后端时传
// nil，入站附件退化成它们的占位文本。
func NewResolverSet(
	store *Store,
	session engineSessionBinder,
	replier engine.OutboundReplier,
	media engine.MediaResolver,
) engine.ResolverSet {
	set := engine.ResolverSet{
		Installation: &installationResolver{store: store},
		Identity:     &identityResolver{store: store},
		Dedup:        NewDeduper(store),
		Session:      &sessionBinder{session: session},
		Audit:        &auditor{store: store},
		OriginType:   originWecomChat,
		// 直接原样赋值：media 本身就是接口，nil 参数会以「nil 接口」落地，
		// Router 的 `set.Media != nil` 守卫依然成立。（钉钉用 if 包同一句赋值，
		// 对同一原因来说是多余的——它的 media 参数同样是 engine.MediaResolver。
		// 那里真正的 typed-nil 隐患在它上面的 if：ack 是具体类型
		// *ackNotifier。）
		Media: media,
	}
	if replier != nil {
		set.Replier = replier
	}
	return set
}

// engineSessionBinder 是 wecom 绑定器驱动的 engine.ChatSession 切片。声明为接
// 口，好让平台特有的参数映射能在单元测试里用假实现跑；生产值是
// *engine.ChatSession。
type engineSessionBinder interface {
	EnsureSession(ctx context.Context, in engine.EnsureSessionInput) (pgtype.UUID, error)
	StartSession(ctx context.Context, in engine.StartSessionInput) (engine.StartSessionResult, error)
	MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error
	AppendUserMessage(ctx context.Context, in engine.AppendInput) (engine.AppendResult, error)
	BindMediaRefs(ctx context.Context, in engine.BindMediaInput) error
}

func (r *sessionBinder) StartSession(ctx context.Context, p engine.StartSessionParams) (engine.StartSessionResult, error) {
	result, err := r.session.StartSession(ctx, engine.StartSessionInput{
		EnsureSessionInput: engine.EnsureSessionInput{
			WorkspaceID: p.Installation.WorkspaceID, AgentID: p.Installation.AgentID,
			InstallationID: p.Installation.ID, Sender: p.Creator,
			BindingKey: p.Message.Source.ChatID, ChatType: p.Message.Source.ChatType,
		},
		Initiator: p.Sender,
		Body:      p.Message.Text, CommandText: p.Message.CommandText, MessageID: p.Message.MessageID,
		ClaimToken: p.ClaimToken, MediaPendingSeconds: p.MediaPendingSeconds,
		PersistMessage: p.PersistMessage, HistoryBoundaryPending: p.HistoryBoundaryPending,
		BeforeCommit: p.BeforeCommit,
	})
	return engine.StartSessionResult{SessionID: result.SessionID, BindingID: result.BindingID, RouteRevision: result.RouteRevision, Append: result.Append}, err
}

// ---- installation routing ----

type installationResolver struct{ store *Store }

// ResolveInstallation 按入站事件携带的 BotID 查 wecom 安装记录。每条
// aibot_msg_callback 帧都是经由它到达的那条 WebSocket 连接来标识机器人的（一
// 条连接一个机器人）；连接器把 BotID 盖进 InboundMessage.Raw，这个解析器就只是
// 一次纯粹的 DB 查询，而不需要 socket 侧的额外管道。
func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	wm, err := wecomMsgFromRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	if wm.BotID == "" {
		return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
	}
	inst, err := r.store.GetInstallationByBotID(ctx, wm.BotID)
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
		Active:          inst.Status == InstallationActive,
		Platform:        inst,
	}, nil
}

// ---- identity ----

type identityResolver struct{ store *Store }

// ResolveSender 把企微智能机器人的 userid（aibot API 按 bot 分配的匿名
// "T"-前缀 id，取自 Source.SenderID）经 channel_user_binding 表映射成 Multica
// 用户。首次发言的发送者没有行，返回 engine.ErrSenderUnbound，Router 会把它和
// 出站绑定引导配对（见 OutboundReplier.sendBindingPrompt）。
//
// 为什么显式绑定而不是隐式启发：aibot 的 userid 与真实企业 userid 或邮箱毫无
// 关系——它们是 per-(bot, user) 的匿名稳定 id——所以邮箱前缀匹配（内部客服号适
// 配器用过的套路）走不通。理由详见 binding.go。
//
// 成员资格复查：绑定行的存在并不证明「当前仍是工作区成员」——被移出成员的绑定
// 会一直活着，直到管理员清理。ErrSenderNotMember 让 Router 静默丢弃而不是再次
// 提示（这才是正确的产品结果，也避免泄露「某人曾是成员」）。
func (r *identityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	senderID := strings.TrimSpace(msg.Source.SenderID)
	if senderID == "" {
		return engine.ResolvedIdentity{}, engine.ErrSenderUnbound
	}
	binding, err := r.store.Queries.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  senderID,
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

// ---- dedup ----

// NewDeduper builds the claim store the Router and the adapter share. Both must
// go through it: they claim the same (installation, message) key on the same
// table, which is what keeps one message from being answered in both places.
func NewDeduper(store *Store) engine.Deduper { return &deduper{store: store} }

// deduper is the wecom Deduper. It uses the shared channel_inbound_message_dedup
// sqlc queries — the same table Feishu / Slack use — so the two-phase
// idempotency invariant is enforced uniformly across channels.
type deduper struct{ store *Store }

func (d *deduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	row, err := d.store.Queries.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return row.ClaimToken, nil
}

func (d *deduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := d.store.Queries.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

func (d *deduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := d.store.Queries.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- session binding ----

type sessionBinder struct{ session engineSessionBinder }

// EnsureSession 选定 wecom 的会话隔离键。单聊（p2p）里企微的 ChatID 本身就是
// userid，一个用户一条会话；群聊则按 chatid 作键，让群里的所有流量落进同一会话
// ——aibot API 没有一等公民的 thread 概念。
func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	return r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     p.Message.Source.ChatID,
		ChatType:       p.Message.Source.ChatType,
	})
}

func (r *sessionBinder) MarkPendingFresh(ctx context.Context, sessionID pgtype.UUID, messageID string) error {
	return r.session.MarkPendingFresh(ctx, sessionID, messageID)
}

func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	// 适配器自己算出来的命令源优先，Text 只是兜底。过去这里用 Text 覆盖它——把
	// 适配器已经算好的「剥掉 @」那一行扔了，于是群里 /issue 解析器拿到的是
	// "@Multica Bot /issue …" 而当成正文。和 lark/feishu_resolvers.go:206、
	// slack/resolvers.go:337 是同样的两行。
	commandText := p.Message.CommandText
	if commandText == "" {
		commandText = p.Message.Text
	}
	return r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:      p.SessionID,
		Sender:         p.Sender,
		InstallationID: p.InstallationID,
		Body:           p.Message.Text,
		CommandText:    commandText,
		MessageID:      p.Message.MessageID,
		ClaimToken:     p.ClaimToken,
		ForceFresh:     p.Message.ForceFresh,
		// 会话任务要等多久再跑。没有它，run 会在消息一落地就触发，agent 拿到的
		// 是 "[Image]" 占位符，而下载还在进行中。
		MediaPendingSeconds: p.MediaPendingSeconds,
	})
}

// BindMedia 把解析器存下来的对象绑到它们随之而来的那条消息上。媒体解析器出现
// 之前，这里正确地是个 no-op——没有可绑的东西——而返回 nil 会被 Router 读成
// 「绑好了」；一旦媒体真的开始解析还保持原样，就意味着每个附件都被下载、解密、
// 存储，然后被悄悄丢弃。
//
// IssueID 是让一条 /issue 轮次的附件属于那条 issue、而不是属于创建它的会话消息
// 的关键；其余 issue 字段让描述能引用它们。飞书把全套都传过去
// （lark/feishu_resolvers.go:223）。
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

// ---- audit ----

type auditor struct{ store *Store }

func (a *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	var eventType string
	if wm, err := wecomMsgFromRaw(msg); err == nil {
		eventType = wm.MsgType
	}
	var instIDArg pgtype.UUID
	if instID.Valid {
		instIDArg = instID
	}
	return a.store.Queries.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ID:               dbid.NewV7(),
		InstallationID:   instIDArg,
		ChannelType:      channelTypeWecom,
		ChannelChatID:    textOrNull(msg.Source.ChatID),
		EventType:        eventType,
		ChannelEventID:   textOrNull(msg.EventID),
		ChannelMessageID: textOrNull(msg.MessageID),
		DropReason:       string(reason),
	})
}

// textOrNull 把空字符串映射成 NULL 的 pgtype.Text——共享的
// RecordChannelInboundDrop 查询在 id 列上用了 sqlc.narg，所以我们传 NULL 而非
// ""。
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
