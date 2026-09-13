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

const (
	agentOfflineText  = "⚠️ 智能体当前不在线，你的消息已收到，等它上线后会处理。"
	agentArchivedText = "⚠️ 该智能体已归档，无法回复。请联系工作区管理员。"
	freshPendingText  = "✅ 已准备从空上下文运行。你的下一条聊天消息仍会进入当前对话，但不会带上之前的上下文。"
	chatStartedText   = "✅ 已新建 Multica 对话。你的下一条消息会进入该对话。"
	issueUsageText    = "请填写任务标题，格式如下：\n\n`/issue <标题>`\n`[描述]`（可选）"
)

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

	// AppURL 是用户点进去兑换绑定 token 的 Multica Web 应用主机（如
	// https://multica.example）。它来自 MULTICA_APP_URL（回退 FRONTEND_ORIGIN），
	// 刻意与 MULTICA_PUBLIC_URL 分开——后者是后端 / API 的 URL。绑定页
	// （/wecom/bind）由 Web 应用托管，所以链接必须指向应用主机。
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
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/wecom/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	r := &OutboundReplier{
		senders:     cfg.Senders,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
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
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		if err := r.post(ctx, inst, msg, agentOfflineText); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: offline notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentArchived:
		if err := r.post(ctx, inst, msg, agentArchivedText); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeFreshPending:
		if err := r.post(ctx, inst, msg, freshPendingText); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: fresh-start confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeChatStarted:
		if err := r.post(ctx, inst, msg, chatStartedText); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: new-chat confirmation failed", "installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIssueUsage:
		if err := r.post(ctx, inst, msg, issueUsageText); err != nil {
			r.logger.WarnContext(ctx, "wecom replier: issue usage reply failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIngested:
		// 只有「创建任务」的消息才值得一条确认；普通聊天消息保持安静（agent 自己的
		// 回答经 EventChatDone / Channel.Send 落地）。
		if res.IssueID.Valid {
			// 引擎报重复的做法是带着另一张任务的 id、编号与标题、把 IssueDuplicate
			// 置位。若两种情形都用「已创建」文案回答，等于告诉举报人他的 bug 是在
			// 别人开的编号下、用一个他从未写过的标题登记了——于是他们不再追，报告
			// 就此丢失。slack/replier.go:125 和 dingtalk/replier.go:125 都在这里分
			// 支；WeCom 曾是唯一不分的那家。
			text := issueCreatedText(res)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "wecom replier: issue confirmation failed",
					"installation_id", util.UUIDToString(inst.ID),
					"duplicate", res.IssueDuplicate, "error", err)
			}
		}
	}
}

func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) error {
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
	// 这段文案由下面的 postPrivate 投递，它永远落在 1:1——无论哪间房触发的，都和
	// 更早那条链接躺在同一场对话里。所以它指引的是「当前这条线程」而不是让读者
	// 去一条他本来就在看的会话。只有更下面的群 ack 跑在房间里，而那一条正是点名
	// 1:1 的。
	text := "👋 绑定链接刚才已经发给你了，就在上方，请直接点击完成绑定。"
	if !token.Reused {
		bindURL := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
		text = "👋 请先绑定你的 Multica 账号，才能与我对话：\n" + bindURL + "\n（链接 15 分钟内有效）"
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
		return r.post(ctx, inst, msg, "👋 已把绑定链接私发给你，请在与我的单聊里点击完成绑定。")
	}
	return nil
}

// postPrivate 把 text 送到某位用户 1:1 会话（chat_type=1），不管这条消息是哪间房
// 触发的。用于绝不能被发进群的 bearer 凭证内容（绑定链接）。
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
// 那个标题属于既有任务，所以是某个*别的*成员写的——甚至不是举报人自己的，这让它
// 成了两处 /issue 调用点里更糟的那处——而回复以 markdown 形态发出。所以过
// breakMemberLinks——issueCreatedText 跑它标题时用的同一个入口：它拆内联 "]("
// 形态，也拆多行标题能偷渡进来的链接引用定义。见 markdown.go。
func issueDuplicateText(res engine.Result) string {
	id := res.IssueIdentifier
	if id == "" {
		id = fmt.Sprintf("#%d", res.IssueNumber)
	}
	title := breakMemberLinks(strings.TrimSpace(res.IssueTitle))
	if title == "" {
		return "⚠️ 未创建 —— 已存在进行中的 " + id
	}
	return "⚠️ 未创建 —— 已存在进行中的 " + id + " — " + title
}

func issueCreatedText(res engine.Result) string {
	id := res.IssueIdentifier
	if id == "" {
		id = fmt.Sprintf("#%d", res.IssueNumber)
	}
	// 标题是举报人自己的文本，而这条确认会回到触发它的会话——群里就是当着全房间
	// 的面。标题若写成 "安全升级：请点击 [重置密码](https://evil.example) 完成验
	// 证"，否则就会以机器人名义顶着一个可点击链接回来。带换行的标题可以定义一条
	// 引用定义而不是内联写出，所以引用定义拆分在这里同样适用。
	title := breakMemberLinks(strings.TrimSpace(res.IssueTitle))
	if title == "" {
		return "✅ 已创建 " + id
	}
	return "✅ 已创建 " + id + " — " + title
}
