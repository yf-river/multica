// outbound.go：EventChatDone 出站投递——把 agent 的回复投回 Slack，是「用户
// 发言 -> agent 处理 -> 回复落回 Slack」整条链路的出站半边。
//
// 事件驱动：监听 events 总线上的 EventChatDone。事件到达后先判定这次任务确实
// 源自 Slack（读 channel_task_delivery 的 channel_type、并核对 task 输入批次的
// 渠道来源），找到对应的 Slack 聊天绑定，用 bot 令牌把回复发进来源会话/线程，
// 最后把投递回执（provenance 与 outbound 记录）落库供事件侧识别。没有 Slack
// 绑定的会话直接忽略，所以在共享事件总线上与飞书 Patcher 共存互不干扰；仅当
// Slack 被配置启用时本订阅器才会注册。
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries 是 Slack 出站订阅器用到的生成查询子集。*db.Queries 满足它；
// 抽成接口是为了测试时能注入桩实现。
type outboundQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	SetChatMessageChannelOutboundProvenanceByTask(ctx context.Context, arg db.SetChatMessageChannelOutboundProvenanceByTaskParams) (int64, error)
	RecordChannelOutboundMessage(ctx context.Context, arg db.RecordChannelOutboundMessageParams) error
}

// replySender 负责投出一条回复。*slackSender 满足它，所以出站路径直接复用
// Send 里的 Markdown->mrkdwn 转换、分片与线程处理。
type replySender interface {
	SendWithMetadata(ctx context.Context, out channel.OutboundMessage, metadata slack.SlackMetadata) (channel.SendResult, error)
}

// Outbound 把 agent 的聊天回复投回 Slack，是整条往返的出站半边。它镜像飞书
// Patcher：监听到 EventChatDone 后，为已完成任务的会话找出 Slack 聊天绑定，
// 把回复发进来源频道/线程。没有 Slack 绑定的会话被忽略，因此能与飞书 Patcher
// 在共享事件总线上共存。仅在 Slack 被配置时注册。
type Outbound struct {
	q         outboundQueries
	decrypt   Decrypter
	logger    *slog.Logger
	newSender func(creds credentials) replySender
}

// NewOutbound 基于生成查询与 bot/app 令牌解密器构造 Slack 出站订阅器。
func NewOutbound(q outboundQueries, decrypt Decrypter, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{q: q, decrypt: decrypt, logger: logger}
	o.newSender = func(c credentials) replySender {
		// 发消息只需要 bot 令牌；入站 Socket Mode 用的是该安装另一条独立的
		// app 级令牌（见 slack_channel.go）。
		return newSlackSender(c, slack.New(c.BotToken), logger)
	}
	return o
}

// Register 订阅总线上的 chat-done 事件。
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
}

// handleEvent 是事件的同步回调。总线投递是同步的，一个卡住的 Slack HTTP 调用
// 绝不能把发布侧钉死：所以这里新建带 10 秒硬超时的 ctx 再往下走。
func (o *Outbound) handleEvent(e events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.processEvent(ctx, e); err != nil {
		o.logger.WarnContext(ctx, "slack outbound: reply delivery failed",
			"error", err, "chat_session_id", e.ChatSessionID)
	}
}

// processEvent 执行一次事件的处理主流程：判定任务来源 -> 取安装与绑定 -> 发
// 消息 -> 落回执。任何一步失败都返回错误交由上层记日志（不影响总线其它订阅者）。
func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	sessionID, err := util.ParseUUID(e.ChatSessionID)
	if err != nil || !sessionID.Valid {
		// issue / autopilot 类任务没有 chat_session，直接跳过。
		return nil
	}
	content := chatDoneContent(e.Payload)
	if content == "" {
		return nil // 没有要说的内容（空补全）
	}
	// 能走到这里的都是「有绑定、非空」的补全，但还要先判定任务出处再取凭据或
	// 发送：Web / Mobile 直接聊天的任务可能复用了当初发源于 Slack 的会话，可它
	// 们的回复只属于 Multica。判定不出源头时出站投递选择「关门失败」（不发送）。
	// 密封渠道的任务和直接聊天任务一样持有一个输入批次，所以判别依据是那个批次
	// 不可变的 channel_ingested 出处，而不是 chat_input_task_id 是否存在（那是
	// #5645 最初用的判别方式，后来被换掉了）。
	taskID, ok := chatDoneTaskID(e)
	if !ok {
		return nil
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // 纯 Multica 聊天或其它渠道，不归 Slack 投递
		}
		return fmt.Errorf("lookup slack task delivery: %w", err)
	}
	if delivery.ChannelType != string(TypeSlack) {
		return nil
	}
	binding := slackBindingFromTaskDelivery(delivery)
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, o.q, task)
	if err != nil {
		return fmt.Errorf("classify task input origin: %w", err)
	}
	if !deliver {
		return nil
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeSlack),
	})
	if err != nil {
		return fmt.Errorf("load slack installation: %w", err)
	}
	if inst.Status != "active" {
		return nil // 从事件触发到回复之间安装被吊销了，别再发
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return fmt.Errorf("decode slack credentials: %w", err)
	}
	channelID, threadTS := outboundTarget(binding)
	result, err := o.newSender(creds).SendWithMetadata(ctx, channel.OutboundMessage{
		ChatID:   channelID,
		Text:     content,
		ThreadID: threadTS,
	}, outboundMetadata(delivery.BindingID, delivery.RouteRevision, "task_reply"))
	if err != nil {
		return fmt.Errorf("post slack reply: %w", err)
	}
	messageIDs := result.MessageIDs
	if len(messageIDs) == 0 && result.MessageID != "" {
		messageIDs = []string{result.MessageID}
	}
	if len(messageIDs) == 0 {
		return errors.New("post slack reply: provider returned no message id")
	}
	rows, err := o.q.SetChatMessageChannelOutboundProvenanceByTask(ctx, db.SetChatMessageChannelOutboundProvenanceByTaskParams{
		ChannelType:    pgtype.Text{String: string(TypeSlack), Valid: true},
		InstallationID: binding.InstallationID,
		ChannelChatID:  pgtype.Text{String: channelID, Valid: true},
		MessageIds:     messageIDs,
		TaskID:         taskID,
	})
	if err != nil {
		return fmt.Errorf("record slack reply provenance: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("record slack reply provenance: updated %d assistant rows, want 1", rows)
	}
	for _, messageID := range messageIDs {
		if err := o.q.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
			OutboundInstallationID: delivery.InstallationID,
			OutboundChannelType:    delivery.ChannelType,
			OutboundMessageID:      messageID,
			OutboundBindingID:      delivery.BindingID,
			OutboundRouteRevision:  delivery.RouteRevision,
			OutboundTaskID:         taskID,
			OutboundKind:           "task_reply",
		}); err != nil {
			return fmt.Errorf("record slack outbound message: %w", err)
		}
	}
	return nil
}

// slackBindingFromTaskDelivery 把投递行转成聊天会话绑定结构：两者字段同源，
// 只是 channel_task_delivery 用扁平的 channel_* 列、会话绑定用统一结构，出站
// 路径里需要按绑定形态取用（回复目标、route revision 等）。
func slackBindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// chatDoneTaskID 从事件信封或 TaskService 发出的类型化/map 负载里抽出任务 id。
// 抽不出任务源头时出站投递选择关门失败（不发送），防止把回复发错会话。
func chatDoneTaskID(e events.Event) (pgtype.UUID, bool) {
	raw := e.TaskID
	if raw == "" {
		switch p := e.Payload.(type) {
		case protocol.ChatDonePayload:
			raw = p.TaskID
		case map[string]any:
			raw, _ = p["task_id"].(string)
		}
	}
	id, err := util.ParseUUID(raw)
	return id, err == nil && id.Valid
}

// outboundTarget 从聊天绑定里恢复真正的发送目标。channel_chat_id 可能是
// 「channel:threadRoot」这种复合隔离键，所以真实频道 id 从绑定 config
// （slackBindingConfig）里读；回复线程用记录下来的 last_thread_id。
func outboundTarget(b db.ChannelChatSessionBinding) (channelID, threadTS string) {
	channelID = b.ChannelChatID
	if len(b.Config) > 0 {
		var cfg slackBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChannelID != "" {
			channelID = cfg.ChannelID
		}
	}
	if b.LastThreadID.Valid {
		threadTS = b.LastThreadID.String
	}
	return channelID, threadTS
}

// chatDoneContent 从 EventChatDone 负载里取出回复正文（类型化负载，或经过一次
// 序列化往返后的 map 形态）。
func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}
