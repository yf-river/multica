// replier.go：Slack 出站回复器（engine.OutboundReplier 的 Slack 实现）——引擎
// 判定完一条入站消息的「结局」后，把对应的用户可见回复投回 Slack（MUL-3666
// 收尾，接上 stage-3 里 Replier=nil 的空尾巴）。它复用与 EventChatDone 出站
// 订阅器相同的 bot 令牌 Send 路径，不需要新的传输通道。
//
// 覆盖的结局：
//   - NeedsBinding：发送者还没绑定。铸造一枚一次性绑定令牌，回一条指向站内
//     兑换页的「先关联你的账号」引导；绑定完成后他的下一条消息就能触达 agent。
//   - AgentOffline / AgentArchived：发状态提示，别让用户干等不知道为什么没反应。
//   - Ingested 且借 /issue 建了 issue：对新 issue 的确认回复。
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 以下文本是发给用户的可见回复内容（agent 离线/归档提示、新聊天开启确认、
// /issue 用法提示等）。它们直接以 Slack 消息形式展示，因此保留英文原文；发送
// 场景见 Reply 里各 Outcome 分支的引用。
const (
	agentOfflineText  = "⚠️ The agent is offline right now. Your message was received and will be handled once it's back online."
	agentArchivedText = "⚠️ This agent has been archived and can't respond. Please contact your workspace admin."
	freshPendingText  = "✅ Fresh start ready. Your next chat message will run without previous context."
	chatStartedText   = "✅ Started a new Multica chat. Your next message will enter it."
	issueUsageText    = "Please include an issue title. Use:\n\n`/issue <title>`\n`[description]` (optional)"
)

// bindingMinter 是回复器需要的绑定令牌能力面。*BindingTokenService 满足它。
type bindingMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, slackUserID string) (BindingToken, error)
}

// controlAckLedger 是控制类回复（绑定引导、issue 确认等）落「已发出」记录的
// 数据面。*db.Queries 满足它。
type controlAckLedger interface {
	RecordChannelOutboundMessage(ctx context.Context, arg db.RecordChannelOutboundMessageParams) error
}

// OutboundReplier 是 engine.OutboundReplier 的 Slack 实现。
type OutboundReplier struct {
	binding     bindingMinter
	decrypt     Decrypter
	newSender   func(creds credentials) replySender
	appURL      string
	bindingPath string
	logger      *slog.Logger
	ledger      controlAckLedger
}

// OutboundReplierConfig 配置回复器。Binding + AppURL 是 NeedsBinding 引导可用
// 的前提；缺了它们就跳过绑定引导（离线/归档/issue 通知照常发）。
type OutboundReplierConfig struct {
	Binding bindingMinter
	Decrypt Decrypter
	// AppURL 是用户点进去兑换绑定令牌的 Multica Web 应用主机（例如
	// https://multica.example），来自 MULTICA_APP_URL（缺省回退到
	// FRONTEND_ORIGIN）。它刻意与 MULTICA_PUBLIC_URL 分开——后者是给 webhook
	// 与 daemon 类端点用的后端/API 公网地址；而绑定页（/slack/bind）由 Web 应用
	// 服务，链接必须指到应用主机而不是 API 主机。与 Lark 回复器的 AppURL 一致。
	AppURL      string
	BindingPath string // 绑定页路径，默认 "/slack/bind"
	Logger      *slog.Logger
	Queries     *db.Queries
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

// NewOutboundReplier 构造回复器。发送器工厂与出站订阅器一致：发消息只需要
// bot 令牌。
func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/slack/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	r := &OutboundReplier{
		binding:     cfg.Binding,
		decrypt:     cfg.Decrypt,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
		logger:      logger,
		ledger:      cfg.Queries,
	}
	r.newSender = func(c credentials) replySender {
		return newSlackSender(c, slack.New(c.BotToken), logger)
	}
	return r
}

// Reply 按结局分派要发给用户的消息。错误只记日志不向上抛：回复器脱离入站
// ACK 路径运行，它的失败不应拖累消息确认。
func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "slack replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		if err := r.postResult(ctx, inst, msg, res, agentOfflineText); err != nil {
			r.logger.WarnContext(ctx, "slack replier: offline notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentArchived:
		if err := r.postResult(ctx, inst, msg, res, agentArchivedText); err != nil {
			r.logger.WarnContext(ctx, "slack replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeFreshPending:
		if err := r.postResult(ctx, inst, msg, res, freshPendingText); err != nil {
			r.logger.WarnContext(ctx, "slack replier: fresh-start confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeChatStarted:
		if err := r.postResult(ctx, inst, msg, res, chatStartedText); err != nil {
			r.logger.WarnContext(ctx, "slack replier: new-chat confirmation failed", "installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIssueUsage:
		if err := r.postResult(ctx, inst, msg, res, issueUsageText); err != nil {
			r.logger.WarnContext(ctx, "slack replier: issue usage reply failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIngested:
		// 只有借 /issue 建出 issue 的产品化结局才值得立刻回复；普通聊天保持沉默
		// （agent 自己的回复稍后经 ChatDone 通道落回）。
		if res.IssueID.Valid {
			text := issueCreatedText(res)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
			if err := r.postResult(ctx, inst, msg, res, text); err != nil {
				r.logger.WarnContext(ctx, "slack replier: issue outcome reply failed",
					"installation_id", util.UUIDToString(inst.ID), "error", err)
			}
		}
	}
}

// postResult 是统一的投递落点：把一段文本回复发到消息来源会话，并在有绑定
// 信息时把「已发出」记录落进 outbound 台账（kind 区分 control_ack 与
// issue_ack）。ledger 或绑定缺失时不落账——那只是少了审计痕迹，回复本身已发出。
func (r *OutboundReplier) postResult(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result, text string) error {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return errors.New("installation platform row unavailable")
	}
	creds, err := decodeCredentials(row.Config, r.decrypt)
	if err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	kind := "control_ack"
	if res.Outcome == engine.OutcomeIngested && res.IssueID.Valid {
		kind = "issue_ack"
	}
	sent, err := r.newSender(creds).SendWithMetadata(ctx, channel.OutboundMessage{
		ChatID: msg.Source.ChatID, Text: text, ThreadID: msg.Source.ThreadID,
	}, outboundMetadata(res.ChannelBindingID, res.ChannelRouteRevision, kind))
	if err != nil {
		return fmt.Errorf("post slack reply: %w", err)
	}
	if r.ledger == nil || !res.ChannelBindingID.Valid {
		return nil
	}
	ids := sent.MessageIDs
	if len(ids) == 0 && sent.MessageID != "" {
		ids = []string{sent.MessageID}
	}
	for _, id := range ids {
		if err := r.ledger.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
			OutboundInstallationID: inst.ID, OutboundChannelType: string(TypeSlack),
			OutboundMessageID: id, OutboundBindingID: res.ChannelBindingID,
			OutboundRouteRevision: res.ChannelRouteRevision, OutboundKind: kind,
		}); err != nil {
			return fmt.Errorf("record slack control acknowledgement: %w", err)
		}
	}
	return nil
}

// sendBindingPrompt 铸造一枚绑定令牌并把「关联账号」引导发到 Slack：令牌嵌在
// 指向站内兑换页的 URL 里，用户点开后把 Slack 账号与 Multica 账号绑定。
func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) error {
	sender := res.Sender
	if sender == "" {
		sender = msg.Source.SenderID
	}
	if sender == "" {
		return errors.New("missing sender id")
	}
	if r.binding == nil {
		return errors.New("binding service not configured")
	}
	if r.appURL == "" {
		return errors.New("app url not configured")
	}
	token, err := r.binding.Mint(ctx, inst.WorkspaceID, inst.ID, sender)
	if err != nil {
		return fmt.Errorf("mint binding token: %w", err)
	}
	bindURL := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	// 把 URL 包成显式的 Slack 链接 <url|label>：formatMrkdwn 会保护这类既有实体、
	// 不做 Markdown 变换，否则 base64url 令牌里的 `_`/`-` 字符会被误当成斜体
	// 标记吃掉。
	text := "👋 To start chatting with me, link your Slack account to Multica: <" +
		bindURL + "|link your account>\n(This link expires in 15 minutes.)"
	return r.postResult(ctx, inst, msg, res, text)
}

// issueCreatedText 拼一条「issue 已创建」的确认文案。
func issueCreatedText(res engine.Result) string {
	id := issueResultIdentifier(res)
	title := memberIssueTitle(strings.TrimSpace(res.IssueTitle))
	if title == "" {
		return "✅ Created " + id
	}
	return "✅ Created " + id + " — " + title
}

// issueDuplicateText 拼一条「重复 issue 未创建」的提示文案（引用已存在的活跃
// issue，避免用户误以为丢了）。
func issueDuplicateText(res engine.Result) string {
	id := issueResultIdentifier(res)
	title := memberIssueTitle(strings.TrimSpace(res.IssueTitle))
	if title == "" {
		return "⚠️ Not created — active issue " + id + " already exists."
	}
	return "⚠️ Not created — active issue " + id + " already exists: " + title
}

// memberIssueTitle 净化成员写的标题再放进文案：打断 markdown 链接与相邻文字
// 粘连，并把 `<` 换成 `&lt;`。
func memberIssueTitle(title string) string {
	title = channel.BreakMarkdownLinkAdjacency(title)
	// formatMrkdwn 有意保留既有的 Slack 实体（如 <url|label>、<@user>）。先在这
	// 一步把成员写下的 `<` 转义掉，让标题里的链接/提及按可见文本处理，而不是被
	// Slack 渲染成可点的实体。
	return strings.ReplaceAll(title, "<", "&lt;")
}

// issueResultIdentifier 产出 issue 的展示标识：有项目内编号（identifier）用它，
// 否则回退成 #数字（issue number）。
func issueResultIdentifier(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	return fmt.Sprintf("#%d", res.IssueNumber)
}
