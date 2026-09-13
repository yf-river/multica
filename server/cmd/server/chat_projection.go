// chat_projection.go
//
// 职责：以可靠事件消费的方式，把"聊天任务走到终态"投影成聊天消息与终态事件。
// 聊天（chat）背后是一次 Agent 任务：任务完成时，把模型的最终输出落成一条
// assistant 消息并发出 chat:done 事件；任务失败时，若系统判断"不需要自动重试"
// （即该任务没有 retry 子任务在跑），则落一条失败说明消息并发出 chat:message
// 事件。整个逻辑的关键在"幂等"：先查任务是否已有对应 assistant 消息，有了就
// 复用，避免事件重放导致同一回答写两条。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// chatCompletionProjectionConsumer 是"聊天完成投影"这个可靠消费者的名字。
const chatCompletionProjectionConsumer = "chat_completion_projection"

// chatFailureProjectionConsumer 是"聊天失败投影"这个可靠消费者的名字。
const chatFailureProjectionConsumer = "chat_failure_projection"

// registerDurableChatConsumers 注册聊天终态的两个可靠消费者：任务完成 →
// 完成投影；任务失败 → 失败投影。
func registerDurableChatConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventTaskCompleted, chatCompletionProjectionConsumer, consumeChatCompletionProjection); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventTaskFailed, chatFailureProjectionConsumer, consumeChatFailureProjection)
}

// consumeChatCompletionProjection 消费"任务完成"事件：把模型输出投影成聊天里
// 的 assistant 消息，并派发一条 chat:done 事件。注意只派 chat:done 这一条——
// 通道相关的转发靠各自的 listener 订阅 chat:done 完成，如果在这里再套一层
// 派生事件会造成重复投递、重试不再幂等。
func consumeChatCompletionProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	task, exists, err := loadChatTerminalTask(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	message, err := projectCompletedChatMessage(ctx, queries, task)
	if err != nil {
		return nil, err
	}
	chatEvent := completedChatEvent(event, task, message)
	// The chat:done event itself is durable. Channel-specific relays consume
	// that event through their normal listener; enqueueing a second nested event
	// here would duplicate delivery and make retries non-idempotent.
	//
	// 中文说明：chat:done 事件本身就是可靠投递的。各渠道（频道）的中继通过
	// 各自的 listener 消费它；这里若再入队一条嵌套事件会导致重复投递，且让
	// 重试失去幂等性。
	return []events.Event{chatEvent}, nil
}

// consumeChatFailureProjection 消费"任务失败"事件：先查该失败任务有没有
// retry 子任务——有的话说明马上要重试，此刻不投影失败消息（免得用户看到
// 一条"失败"转头又好了）；确认不重试后才落失败消息并派发 chat:message。
func consumeChatFailureProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	task, exists, err := loadChatTerminalTask(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	hasRetry, err := queries.HasRetryTaskForParent(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("load chat failure retry decision: %w", err)
	}
	if hasRetry {
		return nil, nil
	}
	message, err := projectFailedChatMessage(ctx, queries, task)
	if err != nil {
		return nil, err
	}
	return []events.Event{failedChatMessageEvent(event, task, message)}, nil
}

// loadChatTerminalTask 加载并校验"聊天终态"所依托的任务与会话：先按通用任务
// 投影规则校验（loadTaskProjectionRow），再确认任务确实挂在某个聊天会话下，
// 且库里会话工作区与事件一致。会话已被删除（FK 被清）或不存在时视为"没有可
// 更新的会话"，返回 exists=false 而不是报错。
func loadChatTerminalTask(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
) (db.AgentTaskQueue, bool, error) {
	payload, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists || payload.ChatSessionID == "" {
		return db.AgentTaskQueue{}, false, err
	}
	if !task.ChatSessionID.Valid {
		// Deleting a chat session clears the task FK. Its terminal event is then
		// intentionally a no-op because there is no conversation left to update.
		//
		// 中文说明：删除聊天会话会一并清掉任务的会话外键。此时任务的终态事件
		// 刻意做成空操作——已经没有会话可更新了。
		return db.AgentTaskQueue{}, false, nil
	}
	if util.UUIDToString(task.ChatSessionID) != payload.ChatSessionID {
		return db.AgentTaskQueue{}, false, fmt.Errorf("chat terminal projection session mismatch")
	}
	session, err := queries.GetChatSession(ctx, task.ChatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, false, nil
	}
	if err != nil {
		return db.AgentTaskQueue{}, false, fmt.Errorf("load chat terminal session: %w", err)
	}
	if util.UUIDToString(session.WorkspaceID) != event.WorkspaceID {
		return db.AgentTaskQueue{}, false, fmt.Errorf("chat terminal projection workspace mismatch")
	}
	return task, true, nil
}

// projectCompletedChatMessage 把完成的任务落成聊天里的 assistant 消息：
// 先尝试从任务结果里解出最终输出；若输出为空，还有可能是同步完成路径写了
// "无应答"行，得查一下再下结论。任何一条路径都先查"是否已有同任务消息"
// （幂等），有则直接复用。
func projectCompletedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*db.ChatMessage, error) {
	var result protocol.TaskCompletedPayload
	if err := json.Unmarshal(task.Result, &result); err != nil || result.Output == "" {
		// Empty output may still be represented by the synchronous completion
		// path as a no-response row. Check that row before deciding there is no
		// transcript outcome to project.
		//
		// 中文说明：同步完成路径可能把"空输出"表现为一行"无应答"消息，所以先
		// 查这一行再判定没有可投影的会话内容。
		message, lookupErr := queries.GetChatMessageByTaskAssistant(ctx, task.ID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return nil, nil
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("load existing completed chat message: %w", lookupErr)
		}
		return &message, nil
	}
	if existing, lookupErr := queries.GetChatMessageByTaskAssistant(ctx, task.ID); lookupErr == nil {
		return &existing, nil
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load existing completed chat message: %w", lookupErr)
	}

	message, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       redact.Text(util.UnescapeBackslashEscapes(result.Output)),
		TaskID:        task.ID,
		ElapsedMs:     service.ComputeChatElapsedMs(task),
	})
	if err != nil {
		return nil, fmt.Errorf("persist completed chat message: %w", err)
	}
	return &message, nil
}

// projectFailedChatMessage 把失败的任务落成一条 assistant 消息（内容是脱敏后
// 的错误信息，带失败原因与耗时）。同样先查重，避免重放导致重复消息。
func projectFailedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (db.ChatMessage, error) {
	if existing, err := queries.GetChatMessageByTaskAssistant(ctx, task.ID); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.ChatMessage{}, fmt.Errorf("load existing failed chat message: %w", err)
	}
	message := ""
	if task.Error.Valid {
		message = redact.Text(task.Error.String)
	}
	created, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       message,
		TaskID:        task.ID,
		FailureReason: task.FailureReason,
		ElapsedMs:     service.ComputeChatElapsedMs(task),
	})
	if err != nil {
		return db.ChatMessage{}, fmt.Errorf("persist failed chat message: %w", err)
	}
	return created, nil
}

// completedChatEvent 组装"聊天完成"事件：带上会话 ID、任务 ID、落库消息的
// ID/内容/时间/耗时，以及快捷操作是否待处理。事件要实时推到客户端，因此
// StreamKey 指向 "chat:<session_id>" 这个实时流。
func completedChatEvent(event events.Event, task db.AgentTaskQueue, message *db.ChatMessage) events.Event {
	payload := protocol.ChatDonePayload{
		ChatSessionID:       util.UUIDToString(task.ChatSessionID),
		TaskID:              util.UUIDToString(task.ID),
		QuickActionsPending: decodeTaskQuickActionsPending(event),
	}
	if message != nil {
		payload.MessageID = util.UUIDToString(message.ID)
		payload.Content = message.Content
		if message.CreatedAt.Valid {
			payload.CreatedAt = message.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if message.ElapsedMs.Valid {
			payload.ElapsedMs = message.ElapsedMs.Int64
		}
	}
	return events.Event{
		Type:          protocol.EventChatDone,
		StreamKey:     "chat:" + util.UUIDToString(task.ChatSessionID),
		WorkspaceID:   event.WorkspaceID,
		ActorType:     "system",
		TaskID:        util.UUIDToString(task.ID),
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	}
}

// decodeTaskQuickActionsPending 从原任务事件负载里取出"快捷操作是否待处理"
// 标志（解不出就当没有）。
func decodeTaskQuickActionsPending(event events.Event) bool {
	payload, ok := decodeEventPayload[taskEventPayload](event)
	return ok && payload.QuickActionsPending
}

// failedChatMessageEvent 组装"聊天失败"事件：按 chat:message 的消息格式发给
// 会话对应的实时流，内容即刚落库的那条失败消息。
func failedChatMessageEvent(event events.Event, task db.AgentTaskQueue, message db.ChatMessage) events.Event {
	createdAt := ""
	if message.CreatedAt.Valid {
		createdAt = message.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return events.Event{
		Type:          protocol.EventChatMessage,
		WorkspaceID:   event.WorkspaceID,
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload: protocol.ChatMessagePayload{
			ChatSessionID: util.UUIDToString(task.ChatSessionID),
			MessageID:     util.UUIDToString(message.ID),
			Role:          message.Role,
			Content:       message.Content,
			TaskID:        util.UUIDToString(task.ID),
			CreatedAt:     createdAt,
		},
	}
}
