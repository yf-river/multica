// autopilot_projection.go
//
// 职责：以"可靠事件消费"的方式把 autopilot 运行投影到与它关联的 Issue /
// 任务上。与 autopilot_listeners.go（进程内轻量回调）不同，这里注册的是
// 带投递回执的持久消费者：Issue 到终态、任务完成/失败/取消这类关键事件
// 即使进程重启也不会丢，保证 autopilot 运行的收尾动作最终一致地落库。
package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableAutopilotConsumers 把 autopilot 运行投影的可靠消费者注册到
// dispatcher，订阅 Issue 更新与任务完成/失败/取消四类事件。消费者名为
// "autopilot_run_projection"。
func registerDurableAutopilotConsumers(dispatcher *eventoutbox.Dispatcher) error {
	return registerDurableEvents(dispatcher, "autopilot_run_projection", consumeAutopilotRunProjection,
		protocol.EventIssueUpdated, protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled)
}

// consumeAutopilotRunProjection 消费一条 autopilot 投影事件：
//   - Issue 更新：仅当状态真的变了且属于会让 autopilot 运行收尾的终态时，
//     用"事件快照里的状态"（而不是库里可能已前进到的更新状态）去投影运行；
//   - 任务终态事件：先加载并校验任务投影行，再把任务投影回它所属的 autopilot
//     运行。
//
// 返回投影过程中新产生的派生事件（若有），由调度器统一外发；无派生则返回 nil。
func consumeAutopilotRunProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	var projected *events.Event
	var err error

	switch event.Type {
	case protocol.EventIssueUpdated:
		payload, ok := decodeIssueEvent(event)
		if !ok {
			return nil, fmt.Errorf("decode autopilot issue projection payload")
		}
		if !payload.StatusChanged {
			return nil, nil
		}
		if err := validateIssueProjectionScope(event, payload.Issue); err != nil {
			return nil, err
		}
		issue, exists, loadErr := getIssueForProjection(ctx, queries, payload.Issue)
		if loadErr != nil || !exists {
			return nil, loadErr
		}
		// The event snapshot is authoritative for this ordered stream entry;
		// the row may already have advanced to a later status by delivery time.
		//
		// 中文说明：对这条有序流里的事件而言，"事件快照"才是权威——投递时
		// 库里那行可能已经前进到更新的状态了，不能拿它来投影。
		issue.Status = payload.Issue.Status
		if !isAutopilotTerminalIssueStatus(ctx, queries, issue) {
			return nil, nil
		}
		projected, err = service.ProjectAutopilotRunFromIssue(ctx, queries, issue)
	case protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled:
		_, task, exists, loadErr := loadTaskProjectionRow(ctx, queries, event)
		if loadErr != nil || !exists {
			return nil, loadErr
		}
		projected, err = service.ProjectAutopilotRunFromTask(ctx, queries, task)
	default:
		return nil, fmt.Errorf("unsupported autopilot projection event %q", event.Type)
	}
	if err != nil {
		return nil, err
	}
	if projected == nil {
		return nil, nil
	}
	return []events.Event{*projected}, nil
}

// isAutopilotTerminalIssueStatus 判断 Issue 的（有效）状态是否属于会让
// autopilot 运行收尾的终态：done / in_review / cancelled / blocked。
// 用 issuestatus.Effective 解析，因此自定义状态会按它继承的规范状态来判定。
func isAutopilotTerminalIssueStatus(ctx context.Context, queries *db.Queries, issue db.Issue) bool {
	switch issuestatus.Effective(ctx, queries, issue.WorkspaceID, issue.Status) {
	case "done", "in_review", "cancelled", "blocked":
		return true
	default:
		return false
	}
}
