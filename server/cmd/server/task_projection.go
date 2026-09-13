// task_projection.go
//
// 职责：消费"任务走到终态"的领域事件，为任务活动流/失败通知做投影。
// 任务（AgentTaskQueue 行）完成、失败或被取消时，事件消费者要先把事件里的
// 快照和库里任务行的真实状态对一遍（防止乱序/伪造/跨身份串数据），确认一致
// 后再写"任务活动"投影；若事件是失败类型，还要额外投影失败通知。本文件是
// 读取与校验部分，具体写库逻辑在 projectTaskActivity /
// projectTaskFailedNotifications 里。
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// consumeTaskTerminalIssueProjection 处理任务终态事件：先加载并校验任务投影行，
// 投影出对应 Issue 上的"任务活动"条目；任务失败时再补投影失败通知。
// 取消（cancelled）事件只做活动投影、不投影通知，因此提前返回 nil。
// 返回的是消费过程中派生出的新事件列表（若有），由调度器统一外发。
func consumeTaskTerminalIssueProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, _, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists || payload.IssueID == "" {
		return nil, err
	}

	action := ""
	switch event.Type {
	case protocol.EventTaskCompleted:
		action = "task_completed"
	case protocol.EventTaskFailed:
		action = "task_failed"
	case protocol.EventTaskCancelled:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported terminal task projection event %q", event.Type)
	}

	emitted, err := projectTaskActivity(ctx, queries, event, payload, action)
	if err != nil {
		return nil, err
	}
	if event.Type == protocol.EventTaskFailed {
		notifications, err := projectTaskFailedNotifications(ctx, queries, event, payload)
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, notifications...)
	}
	return emitted, nil
}

// loadTaskProjectionRow 解出任务终态事件里的负载，并按多个维度与库里任务行
// 交叉校验，任何不一致都直接报错拒收：
//   - 任务的 AgentID、IssueID 必须与事件一致（防身份错位）；
//   - 事件里声明的状态必须与库里当前状态一致（防乱序/过期事件）；
//   - 事件类型必须与任务当前状态匹配（如 completed 事件只能投给已完成的
//     任务，否则拒绝）。
//
// 任务行已被删除时返回 exists=false（与 issue_projection 同理，是"正常完成"
// 而非错误）。decodeEventPayload 泛型版本在此文件用于统一解码。
func loadTaskProjectionRow(ctx context.Context, queries *db.Queries, event events.Event) (taskEventPayload, db.AgentTaskQueue, bool, error) {
	payload, ok := decodeEventPayload[taskEventPayload](event)
	if !ok || payload.TaskID == "" {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("decode terminal task projection payload")
	}
	taskID, err := util.ParseUUID(payload.TaskID)
	if err != nil {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("projection event has invalid task ID: %w", err)
	}
	task, err := queries.GetAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payload, db.AgentTaskQueue{}, false, nil
	}
	if err != nil {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("load task before projection: %w", err)
	}
	actualAgentID := util.UUIDToString(task.AgentID)
	actualIssueID := ""
	if task.IssueID.Valid {
		actualIssueID = util.UUIDToString(task.IssueID)
	}
	if payload.AgentID != actualAgentID || payload.IssueID != actualIssueID {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection identity mismatch")
	}
	if payload.Status != task.Status {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection status mismatch: event=%s row=%s", payload.Status, task.Status)
	}
	// 反查"哪个事件类型对应哪种任务终态"：完成→completed、失败→failed、
	// 取消→cancelled。查不到说明事件类型不支持投到任务终态，放行会污染下游。
	expectedStatus := map[string]string{
		protocol.EventTaskCompleted: "completed",
		protocol.EventTaskFailed:    "failed",
		protocol.EventTaskCancelled: "cancelled",
	}[event.Type]
	if expectedStatus == "" || task.Status != expectedStatus {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection event %s cannot project row status %s", event.Type, task.Status)
	}
	return payload, task, true, nil
}
