package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：Telegram 的 OutboundReplier——引擎侧"判定类回复"的投放缝（engine seam），
// 对齐 slack/replier.go：当引擎对一条入站消息给出"结论结果"（而非正常 agent 回复）时，
// 由本文件把结论翻译成对用户可见的 Telegram 消息并发送。
// 各结果对应的回复：
//   - NeedsBinding：签发一次性绑定令牌，回复指向产品内兑换页（/telegram/bind）的链接；
//   - AgentOffline / AgentArchived：状态通知；
//   - FreshPending / ChatStarted / IssueUsage：命令确认或纠正性引导；
//   - Ingested 且带 /issue 结果：创建成功或重复确认；
//   - Dropped 的点名 /issue 命令：权限/状态性拒绝。
//
// 注意文案 msg* 常量本体在 sender.go（那里有"为什么用英文"的决策记录）；本文件只引用的
// 常量（msgIssueNotMember 等）定义在本文件顶部。

const (
	msgFreshPending   = "✅ Fresh start ready. Your next chat message will run without previous context."
	msgChatStarted    = "✅ Started a new Multica chat. Your next message will enter it."
	msgIssueUsage     = "Please include an issue title. Use:\n\n/issue <title>\n[description] (optional)"
	msgIssueNotMember = "You're not a member of this Multica workspace, so I can't file an issue for you. Ask a workspace admin to invite you, then send the command again."
	msgIssueDisabled  = "This Telegram bot isn't connected to Multica (or was disconnected). Ask a workspace admin to reconnect it."
)

// bindingMinter 是 replier 需要的签发令牌接口；*BindingTokenService 天然满足它
// （抽接口是为了测试注入假实现）。
type bindingMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, telegramUserID string) (BindingToken, error)
}

// OutboundReplier 是 Telegram 对 engine.OutboundReplier 的实现。
type OutboundReplier struct {
	binding     bindingMinter
	decrypt     Decrypter
	appURL      string
	bindingPath string
	apiBase     string
	client      *http.Client
	logger      *slog.Logger
}

// OutboundReplierConfig 配置 replier。Binding 与 AppURL 是 NeedsBinding 提示的前置条件；
// 缺了它们该提示就跳过（其余状态通知照常发）。
type OutboundReplierConfig struct {
	Binding bindingMinter
	Decrypt Decrypter
	// AppURL 是兑换链接里的 Multica Web 应用域名，取值来源与 Slack replier 相同
	//（MULTICA_APP_URL 优先，兜底 FRONTEND_ORIGIN）。
	AppURL      string
	BindingPath string // 默认 "/telegram/bind"
	APIBase     string
	HTTPClient  *http.Client
	Logger      *slog.Logger
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

// NewOutboundReplier 组装 replier，补齐默认值（logger、bindingPath 缺省带前导斜杠）。
func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/telegram/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	return &OutboundReplier{
		binding:     cfg.Binding,
		decrypt:     cfg.Decrypt,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
		apiBase:     cfg.APIBase,
		client:      cfg.HTTPClient,
		logger:      logger,
	}
}

// Reply 把每种结果路由到对应的用户可见消息。错误只记日志、不向上抛：replier 运行在
// 入站 ACK 路径之外（解耦的独立 goroutine），发提示失败不应该反过来影响消息已处理的确认。
func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentOffline:
		if err := r.post(ctx, inst, msg, msgAgentOffline); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: offline notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeAgentArchived:
		if err := r.post(ctx, inst, msg, msgAgentArchived); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeFreshPending:
		if err := r.post(ctx, inst, msg, msgFreshPending); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: fresh-start confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeChatStarted:
		if err := r.post(ctx, inst, msg, msgChatStarted); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: new-chat confirmation failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIssueUsage:
		if err := r.post(ctx, inst, msg, msgIssueUsage); err != nil {
			r.logger.WarnContext(ctx, "telegram replier: issue usage reply failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
	case engine.OutcomeIngested:
		// 消息已入库；若这次入站顺带产出了 issue 结果，才需要回执确认。
		if res.IssueID.Valid {
			text := issueCreatedText(res)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "telegram replier: issue outcome reply failed",
					"installation_id", util.UUIDToString(inst.ID), "error", err)
			}
		}
	case engine.OutcomeDropped:
		// 被拒的消息不一定需要打扰用户（比如群聊里没点名 bot 的闲聊）——按拒绝原因
		// 产出文案，产不出就保持沉默。
		if text := droppedReplyText(res, msg); text != "" {
			if err := r.post(ctx, inst, msg, text); err != nil {
				r.logger.WarnContext(ctx, "telegram replier: drop refusal failed",
					"installation_id", util.UUIDToString(inst.ID), "error", err)
			}
		}
	}
}

func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) error {
	// 群聊里公开可见的兑换链接可能被其他群成员捡走——他若点开兑换，就会把**原始发送者**
	// 的 Telegram 身份绑到错误的 Multica 用户头上。所以群聊只提示"请先私聊我"，只有私聊
	// 场景才带真令牌。
	if msg.Source.ChatType == channel.ChatTypeGroup {
		return r.post(ctx, inst, msg, msgBindingGroupHint)
	}
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
	text := "👋 To start chatting with me, link your Telegram account to Multica:\n" + bindURL + "\n(This link expires in 15 minutes.)"
	return r.post(ctx, inst, msg, text)
}

// post 从携带的平台行里解出本安装的 bot token，把纯文本发回消息来源的聊天/话题。
func (r *OutboundReplier) post(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return errors.New("installation platform row unavailable")
	}
	creds, err := decodeCredentials(row.Config, r.decrypt)
	if err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	chatID, err := strconv.ParseInt(msg.Source.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("bad chat id %q: %w", msg.Source.ChatID, err)
	}
	var threadID int64
	if msg.Source.ThreadID != "" {
		threadID, _ = strconv.ParseInt(msg.Source.ThreadID, 10, 64)
	}
	var reply *replyParameters
	if messageID := parseMessageRef(msg.MessageID); messageID != 0 {
		reply = &replyParameters{MessageID: messageID, AllowSendingWithoutReply: true}
	}
	if _, err := newBotAPI(r.apiBase, creds.BotToken, r.client).SendMessage(ctx, sendMessageParams{
		ChatID:          chatID,
		Text:            text,
		MessageThreadID: threadID,
		ReplyParameters: reply,
	}); err != nil {
		return fmt.Errorf("post telegram reply: %w", err)
	}
	return nil
}

// issueCreatedText 拼 /issue 创建成功的回执（识别号 + 标题）。
func issueCreatedText(res engine.Result) string {
	id := issueResultIdentifier(res)
	title := strings.TrimSpace(res.IssueTitle)
	if title == "" {
		return "✅ Created " + id
	}
	return "✅ Created " + id + " — " + title
}

// issueDuplicateText 拼"未创建，已有活跃同题 issue"的回执。
func issueDuplicateText(res engine.Result) string {
	id := issueResultIdentifier(res)
	title := strings.TrimSpace(res.IssueTitle)
	if title == "" {
		return "⚠️ Not created — active issue " + id + " already exists."
	}
	return "⚠️ Not created — active issue " + id + " already exists: " + title
}

// issueResultIdentifier 挑一个最友好的 issue 标识用于回执：业务标识符 > 数字编号 > UUID。
func issueResultIdentifier(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	if res.IssueNumber > 0 {
		return fmt.Sprintf("#%d", res.IssueNumber)
	}
	return util.UUIDToString(res.IssueID)
}

// isAddressedIssueCommand 判断这条消息是不是"点名发给 bot 的 /issue 命令"（决定是否要
// 在派发失败时兜底提醒、被拒时给拒绝理由）。
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

// droppedReplyText 按被拒原因给点名 /issue 命令产出拒绝文案；不是点名 /issue 或原因
// 不需要打扰用户的返回空串（调用方就不发任何消息）。
func droppedReplyText(res engine.Result, msg channel.InboundMessage) string {
	if !isAddressedIssueCommand(msg) {
		return ""
	}
	switch res.DropReason {
	case engine.DropReasonNonWorkspaceMember:
		return msgIssueNotMember
	case engine.DropReasonRevokedInstallation:
		return msgIssueDisabled
	default:
		return ""
	}
}
