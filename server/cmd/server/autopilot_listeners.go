// autopilot_listeners.go
//
// 职责：把自动驾驶（autopilot）与它关联的 Issue / 任务事件挂上钩，让 autopilot
// 运行状态跟着业务对象实时走。订阅方式是"事件总线回调"（进程内轻量，非可靠
// 投递），用于同步进度；写库这类要保证不丢的关键路径在 autopilot_projection.go
// 里走可靠事件。两个文件互补：这里的回调优先用内存里的字段做快速过滤，尽量
// 不查库。
package main

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerAutopilotListeners 订阅 Issue 与任务事件，让 autopilot 运行与它
// 关联的 Issue、任务保持同步。
func registerAutopilotListeners(bus *events.Bus, svc *service.AutopilotService) {
	ctx := context.Background()

	// 当一条 origin_type='autopilot' 的 Issue 走到终态时，同步更新对应的
	// autopilot 运行。
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		statusChanged, _ := payload["status_changed"].(bool)
		if !statusChanged {
			return
		}
		issue, ok := payload["issue"].(handler.IssueResponse)
		if !ok {
			return
		}
		// Only handle statuses that finalize an autopilot run. This gate exists
		// to skip the issue load for routine forward progress, so it stays a
		// literal comparison for built-in keys. A custom status carries the
		// behavior of the canonical status it inherits, but resolving that
		// needs a catalog read — so let non-built-in keys through and let
		// SyncRunFromIssue normalize once it has the issue. (MUL-6243)
		//
		// 中文说明：这里只处理"会让 autopilot 运行收尾"的状态。这个闸门是
		// 为了给常规的前进式状态更新省掉一次 Issue 回查，所以对内置状态做
		// 字面量比较。自定义状态继承了它所属规范状态的行为，但要解析出来得
		// 读状态目录——因此非内置状态直接放行，等 SyncRunFromIssue 拿到完整
		// Issue 后再归一化。（MUL-6243）
		if issuestatus.IsBuiltIn(issue.Status) &&
			issue.Status != "done" && issue.Status != "in_review" &&
			issue.Status != "cancelled" && issue.Status != "blocked" {
			return
		}
		// 从库里把完整 Issue 捞出来，确认它的 origin_type 确实是 autopilot。
		dbIssue, err := svc.Queries.GetIssue(ctx, parseUUID(issue.ID))
		if err != nil {
			slog.Debug("autopilot listener: failed to load issue", "issue_id", issue.ID, "error", err)
			return
		}
		svc.SyncRunFromIssue(ctx, dbIssue)
	})

	// 任务完成或失败时，检查它是不是 autopilot 的 run_only 任务。
	bus.Subscribe(protocol.EventTaskCompleted, func(e events.Event) {
		syncRunFromTaskEvent(ctx, svc, e)
	})
	bus.Subscribe(protocol.EventTaskFailed, func(e events.Event) {
		syncRunFromTaskEvent(ctx, svc, e)
	})
	bus.Subscribe(protocol.EventTaskCancelled, func(e events.Event) {
		syncRunFromTaskEvent(ctx, svc, e)
	})
}

// syncRunFromTaskEvent 根据任务终态事件同步 autopilot 运行：任务若直属于某个
// autopilot 运行（带 AutopilotRunID），就同步那个运行；任务本身不属于任何
// autopilot 运行，但失败事件是它关联的 Issue 上由 autopilot 派发的任务产物，
// 则去同步该 Issue 对应运行的进度。
func syncRunFromTaskEvent(ctx context.Context, svc *service.AutopilotService, e events.Event) {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	taskID, ok := payload["task_id"].(string)
	if !ok || taskID == "" {
		return
	}
	task, err := svc.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		return
	}
	if task.AutopilotRunID.Valid {
		svc.SyncRunFromTask(ctx, task)
		return
	}
	if e.Type == protocol.EventTaskFailed {
		svc.SyncRunFromLinkedIssueTask(ctx, task)
	}
}
