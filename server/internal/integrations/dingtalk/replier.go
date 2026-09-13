// replier.go：钉钉出站回复器（engine.OutboundReplier 的钉钉实现）——引擎判定
// 完一条入站消息的「结局」后，把对应的用户可见回复投回钉钉。它复用与
// EventChatDone 订阅器相同的 sender，不需要新的传输通道。
//
// 覆盖的结局：
//   - NeedsBinding：发送者未绑定。铸造一次性绑定令牌，回一条指向站内兑换页的
//     「先关联你的账号」引导；绑定后他的下一条消息就能触达 agent。
//   - AgentOffline / AgentArchived：状态提示，别让用户干等不知道为何没反应。
//   - FreshPending / IssueUsage：命令确认或纠错引导。
//   - Ingested 且同步建了 issue：带 issue 标识与标题的确认。普通聊天保持沉默。
//   - Dropped：被丢弃的 /issue 命令给一句明确的拒绝，避免发送者以为静默等于
//     已受理。
package dingtalk

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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	agentOfflineText        = "⚠️ The agent is offline, so this message won't be processed automatically."
	agentArchivedText       = "⚠️ This agent has been archived and can't respond. Please contact your workspace admin."
	freshPendingText        = "✅ Fresh start ready. Your next chat message will run without previous context."
	chatStartedText         = "✅ Started a new Multica chat. Your next message will enter it."
	issueUsageText          = "Please include an issue title. Use:\n\n`/issue <title>`\n\n`[description]` (optional)"
	issueUsageWithMediaText = "Please add a title and resend with the image (*image can come before or after the command*):\n\n`/issue <title>`\n\n`[description]` (optional)"
	// 对丢弃的 /issue 命令的回绝文案，从被删掉的 engine 前 IssueCommandProcessor
	// 承继：没有它们，用户的命令会凭空消失、连「永远不会被处理」的信号都收不到。
	issueNotMemberText = "You're not a member of this Multica workspace, so I can't file an issue for you. Ask a workspace admin to invite you, then send the command again."
	issueDisabledText  = "This DingTalk robot isn't connected to Multica (or was disconnected). Ask the agent owner or a workspace owner/admin to reconnect it."
)

// bindingMinter 是回复器需要的绑定令牌能力面。*BindingTokenService 满足它。
type bindingMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, dingtalkUserID string) (BindingToken, error)
}

// OutboundReplier 是 engine.OutboundReplier 的钉钉实现。
type OutboundReplier struct {
	binding     bindingMinter
	decrypt     Decrypter
	client      *Client
	appURL      string
	bindingPath string
	logger      *slog.Logger
}

// OutboundReplierConfig 配置回复器。Binding + AppURL 是 NeedsBinding 引导可用
// 的前提；缺了它们就跳过绑定引导（状态与命令提示照常发）。
type OutboundReplierConfig struct {
	Binding bindingMinter
	Decrypt Decrypter
	Client  *Client
	// AppURL 是用户点进去兑换绑定令牌的 Multica Web 应用主机（例如
	// https://multica.example）。绑定页（/dingtalk/bind）由 Web 应用服务，所以
	// 链接必须指到应用主机而不是 API 主机。与 Slack 回复器的 AppURL 一致。
	AppURL      string
	BindingPath string // 绑定页路径，默认 "/dingtalk/bind"
	Logger      *slog.Logger
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

// NewOutboundReplier 构造回复器。
func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := cfg.Client
	if client == nil {
		client = NewClient(nil, "")
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/dingtalk/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	return &OutboundReplier{
		binding:     cfg.Binding,
		decrypt:     cfg.Decrypt,
		client:      client,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
		logger:      logger,
	}
}

// Reply 按结局分派要发给用户的消息。错误只记日志不向上抛：回复器脱离入站 ACK
// 路径运行，它的失败不应拖累消息确认。
func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		if err := r.post(ctx, inst, msg, agentOfflineText); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: offline notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentArchived:
		if err := r.post(ctx, inst, msg, agentArchivedText); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeFreshPending:
		if err := r.post(ctx, inst, msg, freshPendingText); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: fresh-start confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeChatStarted:
		if err := r.post(ctx, inst, msg, chatStartedText); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: new-chat confirmation failed", "installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIssueUsage:
		text := issueUsageText
		if res.IssueUsageHadMedia {
			text = issueUsageWithMediaText
		}
		if err := r.post(ctx, inst, msg, text); err != nil {
			r.logger.WarnContext(ctx, "dingtalk replier: issue usage reply failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIngested:
		if res.IssueID.Valid {
			text := issueCreatedText(res)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "dingtalk replier: issue outcome reply failed",
					"installation_id", util.UUIDToString(inst.ID), "error", err)
			}
		}
	case engine.OutcomeDropped:
		// 被丢弃的 /issue 命令要回一句明确拒绝，别让发送者以为会建 issue 而干等；
		// 其它丢弃（重复消息、没 @ 机器人的群聊闲聊）保持沉默。
		if text := droppedReplyText(res, msg); text != "" {
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "dingtalk replier: drop refusal failed",
					"installation_id", util.UUIDToString(inst.ID), "error", err)
			}
		}
	}
}

// sendBindingPrompt 铸造一枚绑定令牌并把「关联账号」引导发出去。令牌嵌在指向站
// 内兑换页的 URL 里；绑定完成后发送者的下一条消息就能触达 agent。
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
	text := "👋 To start chatting with me, link your DingTalk account to Multica: [link your account](" +
		bindURL + ")\n\n(This link expires in 15 minutes.)"
	// 单次绑定链接必须【私聊（1:1）】发给本人，绝不走 targetFromMessage：群聊里
	// 那样发等于把令牌贴进整群，群里任何成员都能捡去兑换——把发送者的钉钉 id 绑
	// 到自己名下（身份错绑）。
	target := sendTarget{ConversationType: convTypeP2P, StaffID: sender}
	if _, err := sendInstallationText(ctx, r.client, r.decrypt, inst, target, text); err != nil {
		return fmt.Errorf("post dingtalk binding prompt: %w", err)
	}
	return nil
}

// post 从携带的平台行里解析安装凭据，并把 text 发回消息来源的会话。
func (r *OutboundReplier) post(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	if _, err := sendInstallationText(ctx, r.client, r.decrypt, inst, targetFromMessage(msg), text); err != nil {
		return fmt.Errorf("post dingtalk reply: %w", err)
	}
	return nil
}

// sendInstallationText 从携带的平台行解析安装凭据，并把 text 发到 target。供
// OutboundReplier 与 ack 通知器共用，两条主动发送路径的解密逻辑保持一致。
func sendInstallationText(ctx context.Context, client *Client, decrypt Decrypter, inst engine.ResolvedInstallation, target sendTarget, text string) (string, error) {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return "", errors.New("installation platform row unavailable")
	}
	creds, err := decodeCredentials(row.Config, decrypt)
	if err != nil {
		return "", fmt.Errorf("decode credentials: %w", err)
	}
	s := &sender{client: client, robotCode: creds.RobotCode, appKey: creds.AppKey, appSecret: creds.AppSecret}
	return s.send(ctx, target, text)
}

// targetFromMessage 从入站消息自带的路由身份构造回复目标（用于即时绑定/状态回
// 复——此刻还没有任何聊天绑定存在）。
func targetFromMessage(msg channel.InboundMessage) sendTarget {
	t := sendTarget{ConversationType: convTypeGroup, ConversationID: msg.Source.ChatID}
	if msg.Source.ChatType == channel.ChatTypeP2P {
		t.ConversationType = convTypeP2P
		t.StaffID = msg.Source.SenderID
	}
	return t
}

// isAddressedIssueCommand 判断 msg 是不是一条显式 @ 到机器人的 /issue 命令——
// 这是被删掉的 engine 前 divert 用的门槛。只有这类消息才配回错误/拒绝：发送者
// 明确请求了动作，沉默会被理解成「已受理」。
func isAddressedIssueCommand(msg channel.InboundMessage) bool {
	if !msg.AddressedToBot {
		return false
	}
	source := msg.CommandText
	if source == "" {
		source = msg.Text
	}
	_, ok := engine.ParseIssueCommand(source)
	return ok
}

// droppedReplyText 把 OutcomeDropped 的结果映射成对用户可见的拒绝文案。
func droppedReplyText(res engine.Result, msg channel.InboundMessage) string {
	if !isAddressedIssueCommand(msg) {
		return ""
	}
	switch res.DropReason {
	case engine.DropReasonNonWorkspaceMember:
		return issueNotMemberText
	case engine.DropReasonRevokedInstallation:
		return issueDisabledText
	default:
		return ""
	}
}

// issueCreatedText 拼一条「issue 已创建」的确认文案。
func issueCreatedText(res engine.Result) string {
	identifier := issueResultIdentifier(res)
	if res.IssueTitle == "" {
		return "✅ Created " + identifier
	}
	return "✅ Created " + identifier + " — " + res.IssueTitle
}

// issueDuplicateText 拼一条「重复 issue 未创建」的提示文案。
func issueDuplicateText(res engine.Result) string {
	identifier := issueResultIdentifier(res)
	if res.IssueTitle == "" {
		return "⚠️ Not created — active issue " + identifier + " already exists."
	}
	return "⚠️ Not created — active issue " + identifier + " already exists: " + res.IssueTitle
}

// issueResultIdentifier 产出 issue 的展示标识：有项目内编号用它，其次回退到
// #数字（issue number），最后用 issue id。
func issueResultIdentifier(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	if res.IssueNumber > 0 {
		return fmt.Sprintf("#%d", res.IssueNumber)
	}
	return util.UUIDToString(res.IssueID)
}
