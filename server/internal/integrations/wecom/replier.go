// replier.go —— WeCom 的 OutboundReplier。处理引擎的 needs_binding /
// agent_offline / agent_archived / issue_created 结局：经入站循环拥有的同一条
// aibot WebSocket 回一条文本消息（aibot 没有 REST 出站；每个写都在 socket 上，
// 经 sendersRegistry 查得）。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
)

// defaultBindingPath is where the web app serves the bind page.
const defaultBindingPath = "/wecom/bind"

// normalizeBindingPath applies the one default: the binding prompt is the only
// thing that builds this URL, and a deployment that configures no path still
// needs a usable link.
func normalizeBindingPath(p string) string {
	if p == "" {
		return defaultBindingPath
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// OutboundReplier 为 WeCom 实现 engine.OutboundReplier。
type OutboundReplier struct {
	binding     binder
	senders     *sendersRegistry
	appURL      string
	bindingPath string
	logger      *slog.Logger
}

// binder 是 sendBindingPrompt 需要的 BindingTokenService 切片，声明成接口以便群 /
// 私聊路由能用假实现练（不落 DB 的 token 铸造）。生产值是 *BindingTokenService。
type binder interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, wecomUserID string) (BindingToken, error)
}

// OutboundReplierConfig 配置回复器。Binding 与 AppURL 是 needs_binding 提示工作
// 所必需的；缺了它们提示会被跳过（离线 / 归档 / 任务通知照常发）。
type OutboundReplierConfig struct {
	Binding *BindingTokenService

	// Senders 是和 wecom ChannelDeps 用同一张的 sendersRegistry。回复器按
	// installation id 查活 wsSender。
	Senders *sendersRegistry

	// AppURL is the Multica web app host the user clicks into to redeem
	// the binding token (e.g. https://multica.example). It comes from
	// MULTICA_APP_URL (falling back to FRONTEND_ORIGIN) and is
	// intentionally separate from MULTICA_PUBLIC_URL, which is the
	// backend/API URL — the bind page (/wecom/bind) is served by the web
	// app, so the link must point at the app host.
	AppURL      string
	BindingPath string // 默认 "/wecom/bind"
	Logger      *slog.Logger
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

// NewOutboundReplier 构造回复器。
func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r := &OutboundReplier{
		senders:     cfg.Senders,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: normalizeBindingPath(cfg.BindingPath),
		logger:      logger,
	}
	// 只在非 nil 时经接口赋值：把 nil *BindingTokenService 存进 binder 接口，会得
	// 到「装着 typed nil 的非 nil 接口值」，击穿 sendBindingPrompt 里的
	// `r.binding == nil` 守卫、在 Mint 上空指针崩溃。
	if cfg.Binding != nil {
		r.binding = cfg.Binding
	}
	return r
}

// Reply 把每个结局路由到它对用户可见的消息。错误记日志、不向上抛：回复器脱离入站
// ACK 路径运行（那条 goroutine 归 engine.Router 所有）。
func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	// These notices land in the room the message came from, so the room
	// decides the language — not whichever member happened to trigger them.
	//
	// The binding prompt is the one thing here addressed to a person rather
	// than the room, and it needs no separate resolution: its reader is
	// unbound by definition, so they have no profile to consult and get the
	// same deployment default the room does.
	c := copyFor(deploymentLocale())

	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res, c); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		if err := r.post(ctx, inst, msg, c.AgentOffline); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: offline notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentArchived:
		if err := r.post(ctx, inst, msg, c.AgentArchived); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeFreshPending:
		if err := r.post(ctx, inst, msg, c.FreshPending); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: fresh-start confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeChatStarted:
		if err := r.post(ctx, inst, msg, c.ChatStarted); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: new-chat confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIssueUsage:
		if err := r.post(ctx, inst, msg, c.IssueUsage); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: issue usage reply failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeInvokeDenied:
		if err := r.sendInvokeDenied(ctx, inst, msg); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: invoke-denied notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIngested:
		// 只有「创建任务」的消息才值得一条确认；普通聊天消息保持安静（agent 自己的
		// 回答经 EventChatDone / Channel.Send 落地）。
		if res.IssueID.Valid {
			// The engine reports a duplicate by carrying the OTHER issue's
			// id, number and title with IssueDuplicate set. Answering both
			// cases with the created copy told the reporter their bug was
			// filed under a number somebody else opened, under a title they
			// never wrote — so they stopped chasing it and the report was
			// lost. slack/replier.go:125 and dingtalk/replier.go:125 both
			// branch here; WeCom was the one that did not.
			text := issueCreatedText(res, c)
			if res.IssueDuplicate {
				text = issueDuplicateText(res, c)
			}
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "wecom replier: issue confirmation failed",
					"installation_id", util.UUIDToString(inst.ID),
					"duplicate", res.IssueDuplicate, "error", err)
			}
		}
	}
}

func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result, c copyPack) error {
	sender := res.Sender
	if sender == "" {
		sender = msg.Source.SenderID
	}
	if sender == "" {
		return errors.New("wecom: missing sender id")
	}
	if r.binding == nil {
		return errors.New("wecom: binding service not configured")
	}
	if r.appURL == "" {
		return errors.New("wecom: app url not configured")
	}
	token, err := r.binding.Mint(ctx, inst.WorkspaceID, inst.ID, sender)
	if err != nil {
		return fmt.Errorf("wecom: mint binding token: %w", err)
	}
	// 节流压掉了这次铸造：一条活链接已经在这位用户手上。当初只存过哈希，所以没
	// 有可重建的 URL——让用户去点已有的那条消息即可。节流窗口远比 TTL 短，那条
	// 链接还有大半条命。
	//
	// This text is delivered by postPrivate below, which always lands in the
	// 1:1 — the same conversation the earlier link is sitting in, whichever
	// room triggered this. So it points up the current thread rather than
	// telling the reader to go to a chat they are already reading. Only the
	// group ack further down runs in the room, and it is the one that names
	// the 1:1.
	text := c.BindingPending
	if !token.Reused {
		bindURL := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
		text = c.BindingPromptPrefix + bindURL + c.BindingPromptSuffix
	}
	// 绑定 token 是 bearer 凭证：binding.Redeem 只校验兑换者属于 token 的工作区，
	// 而绑定页在加载时就会以「当前登录者」身份兑换。把它发给 msg.Source.ChatID——
	// 群里那就是群——会让先点的任何成员把发送者的企微 userid 绑到自己名下，之后
	// 发送者的所有消息（含 /issue）都解析到劫持者头上。所以要把链接以 chat_type=1
	// 私发给发送者自己的 userid（和 outbound.go 给收件箱推送用的同一个地址），绝不
	// 发进房间。Lark 的 SendBindingPromptCard 出于同样理由指向发送者的 OpenID。
	if err := r.postPrivate(ctx, inst, sender, text); err != nil {
		return err
	}
	// 群里的触发仍需要一个回答——沉默读起来像机器人坏了——但要一个不点名的、
	// 无 token 的回答。只在私发被接受后才发，这样房间永远不会被指向一条线上拒收
	// 过的消息。1:1 触发已经在其唯一房间里收到提示了。
	if aibotChatTypeFromChannel(msg.Source.ChatType) == chatTypeGroupInt {
		return r.post(ctx, inst, msg, c.BindingSentPrivately)
	}
	return nil
}

// sendInvokeDenied tells a member the agent is not theirs to run.
//
// A 1:1 is answered in place. A GROUP trigger is answered in the sender's own
// 1:1 and the room hears nothing: a line there would tell everyone present both
// which member was refused and that the agent is someone's private one. The
// sender is bound by definition at this point — the identity check is what
// produced the user id this verdict was read for — so a 1:1 route to them
// exists.
func (r *OutboundReplier) sendInvokeDenied(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) error {
	if aibotChatTypeFromChannel(msg.Source.ChatType) != chatTypeGroupInt {
		return r.post(ctx, inst, msg, copyFor(deploymentLocale()).InvokeDenied)
	}
	return r.postPrivate(ctx, inst, msg.Source.SenderID, copyFor(deploymentLocale()).InvokeDenied)
}

// postPrivate delivers text to a single user's 1:1 chat (chat_type=1),
// regardless of which room triggered the message. Used for bearer-credential
// content (the binding link) that must never land in a group.
func (r *OutboundReplier) postPrivate(ctx context.Context, inst engine.ResolvedInstallation, userID, text string) error {
	if r.senders == nil {
		return errors.New("wecom: sender registry not configured")
	}
	if !inst.ID.Valid {
		return errors.New("wecom: installation id is zero")
	}
	if userID == "" {
		return errors.New("wecom: missing user id")
	}
	sender := r.senders.get(inst.ID)
	if sender == nil {
		return errors.New("wecom: connection not ready")
	}
	return sender.sendTextCtx(ctx, userID, chatTypeSingleInt, text)
}

// post 在注册表里查这条安装的活 wsSender，用给定文本推 aibot_send_msg。
// Supervisor 没有活跃连接时（租约翻转后的重连中、或刚 Revoke 后）返回
// "connection not ready"。
func (r *OutboundReplier) post(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	if r.senders == nil {
		return errors.New("wecom: sender registry not configured")
	}
	if !inst.ID.Valid {
		return errors.New("wecom: installation id is zero")
	}
	sender := r.senders.get(inst.ID)
	if sender == nil {
		return errors.New("wecom: connection not ready")
	}
	chatID := msg.Source.ChatID
	if chatID == "" {
		return errors.New("wecom: missing chat_id")
	}
	chatType := aibotChatTypeFromChannel(msg.Source.ChatType)
	return sender.sendTextCtx(ctx, chatID, chatType, text)
}

// issueDuplicateText 回答一条「因已有活跃任务覆盖它」而被引擎拒绝的 /issue。它点
// 名的是另一张任务——这正是全部意义：举报人需要知道讨论已经在哪里，而不是被塞
// 一个会被他当成自己的编号。
//
// That title belongs to the pre-existing issue, so it is text some *other*
// member wrote — not even the reporter's own, which is what makes this the
// worse of the two /issue call sites — and the reply ships as markdown. The
// pack's renderer runs it through breakMemberLinks, the same entry point
// issueCreated uses: it breaks the inline "](" form and the link reference
// definition a multi-line title can smuggle instead. See markdown.go.
func issueDuplicateText(res engine.Result, c copyPack) string {
	return c.issueDuplicate(issueRef(res), res.IssueTitle)
}

// issueCreatedText answers a /issue the engine accepted. The title is the
// reporter's own text and this confirmation goes back into the chat that
// triggered it — in a group, in front of the room — so the pack breaks its
// links before it goes out. See copyPack.issueCreated.
func issueCreatedText(res engine.Result, c copyPack) string {
	return c.issueCreated(issueRef(res), res.IssueTitle)
}

// issueRef is how an issue is named in a confirmation: its identifier when the
// engine produced one, its number otherwise.
func issueRef(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	return fmt.Sprintf("#%d", res.IssueNumber)
}
