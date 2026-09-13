// outbound.go：把 agent 的聊天回复投回钉钉——整条往返的出站半边，监听
// EventChatDone / EventTaskFailed。
//
// 事件到来后先确认任务确实源自钉钉（channel_task_delivery 的 channel_type + task
// 输入来源判定），找到对应聊天绑定与安装，再用该安装的凭据把回复（或失败提示）
// 发回来源会话。没有钉钉绑定的会话直接忽略，因此能与飞书、Slack 订阅器在共享
// 事件总线上共存；仅在钉钉被配置时注册。
//
// 之所以也订阅 task-failed：失败时钉钉会话要与 Web 记录保持一致——缺了它，用户
// 会对着那条「👀 On it」的 ack 永远等下去。
package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries 是钉钉出站订阅器需要的生成查询子集。*db.Queries 满足它。
type outboundQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

// Outbound 把 agent 的聊天回复投回钉钉——整条往返的出站半边。监听到
// EventChatDone / EventTaskFailed 后，为任务的会话找出钉钉聊天绑定，把回复（或
// 失败提示）发进来源会话。没有钉钉绑定的会话被忽略，所以能与飞书、Slack 订阅器
// 在共享事件总线上共存。仅在钉钉被配置时注册。
type Outbound struct {
	q       outboundQueries
	decrypt Decrypter
	client  *Client
	logger  *slog.Logger
}

// NewOutbound 基于生成查询、AppSecret 解密器与共享的令牌缓存 Client 构造钉钉
// 出站订阅器。
func NewOutbound(q outboundQueries, decrypt Decrypter, client *Client, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	if client == nil {
		client = NewClient(nil, "")
	}
	return &Outbound{q: q, decrypt: decrypt, client: client, logger: logger}
}

// Register 订阅 chat-done 与 task-failed。订阅 task-failed 是为了让钉钉会话与
// Web 记录保持一致——没有它，失败的那轮会让用户对着「👀 On it」ack 永远等下去。
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, o.handleEvent)
}

// handleEvent 是事件的同步回调。总线投递是同步的，卡住的钉钉 HTTP 调用绝不能把
// 发布侧钉死：这里新建带 10 秒硬超时的 ctx 再往下走。
func (o *Outbound) handleEvent(e events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.processEvent(ctx, e); err != nil {
		o.logger.WarnContext(ctx, "dingtalk outbound: reply delivery failed",
			"error", err, "chat_session_id", e.ChatSessionID)
	}
}

// processEvent 执行一次事件处理主流程：抽任务/会话 -> 判定来源 -> 取安装凭据 ->
// 发送。任何一步失败都返回错误记日志（不影响总线其它订阅者）。
func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	taskID, sessionID, ok := taskAndSessionFromEvent(e)
	if !ok || !sessionID.Valid {
		// issue / autopilot 任务不带 chat_session，直接跳过。
		return nil
	}
	content := eventContent(e)
	if content == "" {
		return nil // 没有要说的内容（空补全，或处于等待重试的失败）
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // 纯 Multica 任务或快照不变式被打破：关门失败
		}
		return fmt.Errorf("lookup dingtalk task delivery: %w", err)
	}
	if delivery.ChannelType != string(TypeDingTalk) {
		return nil
	}
	binding := bindingFromTaskDelivery(delivery)
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
		ChannelType: string(TypeDingTalk),
	})
	if err != nil {
		return fmt.Errorf("load dingtalk installation: %w", err)
	}
	if inst.Status != "active" {
		return nil // 事件触发到回复之间安装被吊销了
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return fmt.Errorf("decode dingtalk credentials: %w", err)
	}
	s := &sender{client: o.client, robotCode: creds.RobotCode, appKey: creds.AppKey, appSecret: creds.AppSecret}
	if _, err := s.send(ctx, outboundTarget(binding), content); err != nil {
		return fmt.Errorf("post dingtalk reply: %w", err)
	}
	return nil
}

// bindingFromTaskDelivery 把投递行转成聊天会话绑定结构：两者同源，只是
// channel_task_delivery 用扁平的 channel_* 列、会话绑定用统一结构。
func bindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// eventContent 从 EventChatDone 负载（类型化，或序列化往返后的 map 形态）或
// EventTaskFailed 负载里取出要投的正文。取空则保持沉默。
//
// 对 task-failed，正文镜像 Web 记录的失败 chat_message：广播的 `error` 字段带
// 的是同一份脱敏失败文本；若自动重试还在挂起则【不带】该字段（重试那轮会报它
// 自己的结果），所以「error 在场」就表示可投递。
func eventContent(e events.Event) string {
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if e.Type == protocol.EventTaskFailed {
			if retryPending, _ := p["retry_pending"].(bool); retryPending {
				return ""
			}
			if s, _ := p["error"].(string); s != "" {
				return "⚠️ " + s
			}
			return ""
		}
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

// taskAndSessionFromEvent 从事件信封或类型化/map 负载里找回任务 id 与会话 id。
// 返回 ok=true 表示至少任务 id 是有效的。
func taskAndSessionFromEvent(e events.Event) (taskID, sessionID pgtype.UUID, ok bool) {
	if e.TaskID != "" {
		_ = taskID.Scan(e.TaskID)
	}
	if e.ChatSessionID != "" {
		_ = sessionID.Scan(e.ChatSessionID)
	}
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		if !taskID.Valid {
			_ = taskID.Scan(p.TaskID)
		}
		if !sessionID.Valid {
			_ = sessionID.Scan(p.ChatSessionID)
		}
	case map[string]any:
		if !taskID.Valid {
			if raw, _ := p["task_id"].(string); raw != "" {
				_ = taskID.Scan(raw)
			}
		}
		if !sessionID.Valid {
			if raw, _ := p["chat_session_id"].(string); raw != "" {
				_ = sessionID.Scan(raw)
			}
		}
	}
	return taskID, sessionID, taskID.Valid
}
