// plugin_event_bridge.go —— 把内部事件总线翻译成插件可订阅的七种事件（两套词汇、一处映射）。
package service

import (
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 把内部事件总线翻译成插件可订阅的七种事件。
//
// 刻意两套词汇：内部名字是实时传输的关注点，前端需要时可改动；插件名字是已发布的契约。单点映射意味着我方
// 改名只动本文件，而不是打破每个已安装插件。

// EventSink 是桥接所需的派发器半边。
//
// 用接口是为了无需网络即可测试词汇映射：本文件决定的是“内部事件变成哪个插件事件”，这值得单独断言，
// 而非经一个活端点间接验证。
type EventSink interface {
	Dispatch(eventType, workspaceID string, payload any)
}

// SubscribePluginEvents 把派发器接到总线上。
//
// Bus.Publish 是【内联】调用监听者的——跑在发布请求的 goroutine 上——因此这些闭包所做的一切必须廉价
// 且不阻塞：它们抽取一个 id 后移交，网络调用发生在 worker 上。
func SubscribePluginEvents(bus *events.Bus, dispatcher EventSink) {
	if bus == nil || dispatcher == nil {
		return
	}

	// 监听者做尽可能少的事：不加检视地把载荷移交。在这里抽取 issue id，会在每个工作区、对每个此类事件，把完整
	// issue 体的 JSON 往返压到发布请求的 goroutine 上——包括完全关掉插件的部署。该工作在 worker 上、在开关检查
	// 之后才发生，那里没有任何人等它。
	forward := func(pluginEvent string) events.Handler {
		return func(e events.Event) {
			dispatcher.Dispatch(pluginEvent, e.WorkspaceID, e.Payload)
		}
	}

	bus.Subscribe(protocol.EventIssueCreated, forward(plugincontract.EventIssueCreated))
	bus.Subscribe(protocol.EventCommentCreated, forward(plugincontract.EventCommentCreated))
	bus.Subscribe(protocol.EventTaskRunning, forward(plugincontract.EventTaskStarted))
	bus.Subscribe(protocol.EventTaskCompleted, forward(plugincontract.EventTaskCompleted))
	bus.Subscribe(protocol.EventTaskFailed, forward(plugincontract.EventTaskFailed))

	// issue.status_changed 内部没有独立事件：状态变更是一条带 status_changed=true 的 issue:updated。在这里派生它
	// 而非二次发布，让一次写入只产出一条内部事件，并让插件订阅它关心的具体东西、而不是自己过滤每个字段变化。
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		dispatcher.Dispatch(plugincontract.EventIssueUpdated, e.WorkspaceID, e.Payload)
		// 只是一次 map 查找，不是解析：足够廉价，可放请求 goroutine。
		if payloadFlag(e.Payload, "status_changed") {
			dispatcher.Dispatch(plugincontract.EventIssueStatusChanged, e.WorkspaceID, e.Payload)
		}
	})
}

// issueIDFromPayload 找到事件所属的 issue，以便把为 hook 签发的回调 token 收窄到该 issue。无 issue 的载荷
// 产生零 UUID——表示该授权不按 issue 收敛。
func issueIDFromPayload(payload any) pgtype.UUID {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return pgtype.UUID{}
	}
	var shape struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
		Comment struct {
			IssueID string `json:"issue_id"`
		} `json:"comment"`
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		return pgtype.UUID{}
	}
	for _, candidate := range []string{shape.Issue.ID, shape.Comment.IssueID, shape.IssueID} {
		if candidate == "" {
			continue
		}
		if parsed, err := parseUUIDValue(candidate); err == nil {
			return parsed
		}
	}
	return pgtype.UUID{}
}

func payloadFlag(payload any, key string) bool {
	if fields, ok := payload.(map[string]any); ok {
		flag, _ := fields[key].(bool)
		return flag
	}
	return false
}
