// listeners.go
//
// 职责：把事件总线上发布的事件接到实时广播（WebSocket）上。
// 这里区分两类事件：个人事件（收件箱、邀请、会话创建等）只推给目标用户
// （SendToUser），其余事件广播到整个工作区房间。同时负责一件"见不得光"的
// 事：把仅供进程内监听者使用的负载键（如 issue:updated 的 prev_description）
// 在出站前剥掉，避免把巨大的字段复制到每个客户端连接上（见 MUL-5492）。
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// internalOnlyPayloadKeys 列出"只为进程内监听者存在、绝不能被序列化发给
// WebSocket 客户端"的负载键。
//
// issue:updated 会携带 prev_description 和 prev_title，供进程内监听者与当前
// 新值做对比：subscriber_listeners.go 给新被 @ 的人加订阅、notification_listeners.go
// 拼 @ 通知、activity_listeners.go 记录标题变更。它们都跑在 bus.Subscribe 上，
// 而 Publish 在下面那个 SubscribeAll 转发器之前派发——所以离开前删掉这些键
// 不会影响它们。
//
// 没有任何客户端读这两个键——packages/core/types/events.ts 里的
// IssueUpdatedPayload 压根没声明它们。它们会跑到线上，纯粹是因为转发器原样
// 复用了生产者那份 payload map：导致每次描述自动保存都会向工作区内每个连接
// 广播整整两份描述全文（issue 里的新版 + prev_description 旧版），包括那些
// 根本没打开该 Issue 的用户。写库是 O(1) 的，扇形广播却是 O(工作区连接数 ×
// 描述长度)（MUL-5492）。
//
// 这里用"查表"而不是对单一事件类型写 if，是因为这个 bug 是结构性的而不是
// 笔误：以后给已发布负载再加一个大字段，会无声地继承同样的开销。把清单声明
// 出来，等于把"进程内/对外负载的边界"收拢在一个可评审的位置。
var internalOnlyPayloadKeys = map[string][]string{
	protocol.EventIssueUpdated: {"prev_description", "prev_title"},
	// task:failed error text is consumed synchronously by channel outbounds.
	// It may contain provider/runtime detail that belongs in the originating
	// chat transcript, not in the workspace-wide realtime fanout.
	//
	// 中文说明：task:failed 的错误文本由各渠道出站同步消费。它可能包含提供商/
	// 运行时的细节——这些属于源聊天记录，不该全工作区实时广播出去。
	protocol.EventTaskFailed: {"error"},
}

// projectOutbound 返回删掉了该事件类型"内部专用键"的负载副本，供序列化给
// 外部消费者。注意绝不原地改传入的 map：调用到这一步时进程内监听者虽然已跑
// 完，但生产者仍拥有这份 map，后面可能还有第二个转发器要读——原地改等于埋雷。
func projectOutbound(eventType string, payload any) any {
	keys := internalOnlyPayloadKeys[eventType]
	if len(keys) == 0 {
		return payload
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	projected := make(map[string]any, len(m))
	for k, v := range m {
		projected[k] = v
	}
	for _, k := range keys {
		delete(projected, k)
	}
	return projected
}

// registerListeners 把事件总线接到 WS 广播上。个人事件（收件箱、邀请）只通过
// SendToUser 发给目标用户；其余事件广播到工作区房间。
//
// broadcaster 参数刻意声明成 realtime.Broadcaster 接口而不是 *realtime.Hub：
// 这样以后想换成 Redis 中继或按特性开关做双写实现，都不用动下面任何一个事件
// 监听器。这是横向扩容计划（MUL-1138）的 Phase 0。
func registerListeners(bus *events.Bus, b realtime.Broadcaster) {
	// 个人事件绝不应该广播给整个工作区。
	personalEvents := map[string]bool{
		protocol.EventInboxNew:           true,
		protocol.EventInboxRead:          true,
		protocol.EventInboxArchived:      true,
		protocol.EventInboxUnarchived:    true,
		protocol.EventInboxBatchRead:     true,
		protocol.EventInboxBatchArchived: true,
		protocol.EventInvitationCreated:  true,
		protocol.EventInvitationRevoked:  true,
		protocol.EventChatSessionCreated: true,
		protocol.EventChatSessionUpdated: true,
	}

	// 辅助函数：把事件序列化后发给指定用户。
	sendToRecipient := func(b realtime.Broadcaster, e events.Event, recipientID string) {
		if recipientID == "" {
			return
		}
		data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
		if err != nil {
			return
		}
		realtime.M.RecordEvent(e.Type)
		b.SendToUser(recipientID, data)
	}

	// inbox:new —— 接收者藏在嵌套的 item 里
	bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return
		}
		recipientID, _ := item["recipient_id"].(string)
		sendToRecipient(b, e, recipientID)
	})

	// inbox:read / archived / unarchived / batch-read / batch-archived ——
	// 接收者在负载顶层。
	for _, eventType := range []string{
		protocol.EventInboxRead, protocol.EventInboxArchived, protocol.EventInboxUnarchived,
		protocol.EventInboxBatchRead, protocol.EventInboxBatchArchived,
	} {
		bus.Subscribe(eventType, func(e events.Event) {
			payload, ok := e.Payload.(map[string]any)
			if !ok {
				return
			}
			recipientID, _ := payload["recipient_id"].(string)
			sendToRecipient(b, e, recipientID)
		})
	}

	// invitation:created —— 推给被邀请人，让他实时看到新邀请。
	bus.Subscribe(protocol.EventInvitationCreated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		inv, ok := payload["invitation"].(handler.InvitationResponse)
		if !ok {
			// 兜底：有些生产者按 map 编码，这里走 map 分支取被邀请人 ID。
			if invMap, ok := payload["invitation"].(map[string]any); ok {
				if uid, _ := invMap["invitee_user_id"].(*string); uid != nil && *uid != "" {
					data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
					if err != nil {
						return
					}
					realtime.M.RecordEvent(e.Type)
					b.SendToUser(*uid, data)
				}
			}
			return
		}
		if inv.InviteeUserID != nil && *inv.InviteeUserID != "" {
			data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
			if err != nil {
				return
			}
			realtime.M.RecordEvent(e.Type)
			b.SendToUser(*inv.InviteeUserID, data)
		}
	})

	// invitation:revoked —— 推给被邀请人，让他的待处理列表即时更新。
	bus.Subscribe(protocol.EventInvitationRevoked, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		uid, _ := payload["invitee_user_id"].(*string)
		if uid != nil && *uid != "" {
			sendToRecipient(b, e, *uid)
		}
	})

	// Chat 会话对创建者私有。它的初始标题可能由创建者的第一条消息推导而来，
	// 所以"会话列表失效"这类事件不能广播给每个工作区成员。此事件在各生产者
	// 路径上 ActorID 都是创建者本人。
	for _, eventType := range []string{protocol.EventChatSessionCreated, protocol.EventChatSessionUpdated} {
		bus.Subscribe(eventType, func(e events.Event) {
			sendToRecipient(b, e, e.ActorID)
		})
	}

	// member:added —— 同时发给被邀请的用户本人，让他发现自己进了新工作区。
	// 传 excludeWorkspace 参数，避免已在该目标房间内（经 SubscribeAll 的
	// BroadcastToWorkspace 到达）的客户端把同一条事件收两遍。
	bus.Subscribe(protocol.EventMemberAdded, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		var userID string
		switch m := payload["member"].(type) {
		case handler.MemberWithUserResponse:
			userID = m.UserID
		case map[string]any:
			userID, _ = m["user_id"].(string)
		default:
			slog.Warn("member:added: unexpected member payload type", "type", fmt.Sprintf("%T", payload["member"]))
		}
		if userID == "" {
			return
		}
		data, err := json.Marshal(map[string]any{"type": e.Type, "payload": projectOutbound(e.Type, e.Payload), "actor_id": e.ActorID, "actor_type": e.ActorType})
		if err != nil {
			return
		}
		realtime.M.RecordEvent(e.Type)
		b.SendToUser(userID, data, e.WorkspaceID)
	})

	// SubscribeAll 处理"非个人事件"的工作区广播。
	bus.SubscribeAll(func(e events.Event) {
		// 个人事件跳过——它们由上面各自类型的监听器处理。
		if personalEvents[e.Type] {
			return
		}

		msg := map[string]any{
			"type":       e.Type,
			"payload":    projectOutbound(e.Type, e.Payload),
			"actor_id":   e.ActorID,
			"actor_type": e.ActorType,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			slog.Error("failed to marshal event", "event_type", e.Type, "error", err)
			return
		}

		// Phase 1 (MUL-1138)：高频 task/chat 事件按资源做作用域路由，刻意还没
		// 启用。服务端零件（Hub.subscribe/unsubscribe 协议、ScopeAuthorizer、
		// Redis Streams 中继）都已就位，但客户端（WSClient + 各页面的
		// chat/task hooks）还没有发送 subscribe 帧、也不会在断线重连时重放
		// 订阅。现在就把这些事件改走 BroadcastToScope("task"|"chat", ...)，
		// 会静默丢掉每一条 chat/task 消息——实时聊天时间线、未读徽标、待处理
		// 任务 UI 全部会被打坏。
		//
		// 在客户端落地作用域订阅 PR 之前，task/chat 事件继续走工作区广播
		// （与改动前行为一致）。生产方仍会填充 Event.TaskID / ChatSessionID
		// 字段，将来切换开关只是这里改一行的事。背景见 PR #1429 review。

		if e.WorkspaceID != "" {
			realtime.M.RecordEvent(e.Type)
			b.BroadcastToWorkspace(e.WorkspaceID, data)
		} else if strings.HasPrefix(e.Type, "daemon:") {
			realtime.M.RecordEvent(e.Type)
			b.Broadcast(data)
		}
		// 其余直接丢弃——没有工作区的非 daemon 事件不做全局广播。
	})
}
