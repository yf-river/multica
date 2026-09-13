// activity_listeners.go
//
// 职责：以可靠事件消费的方式把业务变更投影成"Issue 活动流（activity log）"。
// Issue 创建时记一条 created 活动；Issue 更新时按"动了哪些字段"分别记
// status_changed / priority_changed / assignee_changed / description_updated
// 等条目；任务终态则由 task_issue_projection 消费者投 task_completed /
// task_failed 活动（见 task_projection.go）。所有活动行由源事件派生而来，
// 写进 ActivityLog 表后再派发 EventActivityCreated 事件驱动实时流。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableActivityConsumers 注册活动流相关的可靠消费者：
//   - activity_receipt：EventActivityCreated 事件的空回执（活动行已在源事务写
//     好，这里只负责关闭事件流）；
//   - activity_log：Issue 创建/更新时投影活动条目；
//   - task_issue_projection：任务终态投影（复用 consumeTaskTerminalIssueProjection）。
func registerDurableActivityConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventActivityCreated, "activity_receipt", consumeActivityCreatedReceipt); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueCreated, "activity_log", consumeIssueCreatedActivity); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueUpdated, "activity_log", consumeIssueUpdatedActivities); err != nil {
		return err
	}
	return registerDurableEvents(dispatcher, "task_issue_projection", consumeTaskTerminalIssueProjection,
		protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled)
}

// consumeActivityCreatedReceipt 给 EventActivityCreated 事件打回执。活动行
// 由源事务写好，是提交后实时提示的持久凭证，这里不再投影第二条活动。若在投递
// 前 Issue 已被删除也是合法终态——活动行会随 Issue 一起被删，因此直接静默
// 完成消费即可。
func consumeActivityCreatedReceipt(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	if event.Type != protocol.EventActivityCreated {
		return nil, fmt.Errorf("activity-created receipt has unexpected event type %q", event.Type)
	}
	payload, ok := decodeActivityCreatedEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode activity-created receipt payload")
	}
	issueID, err := util.ParseUUID(payload.IssueID)
	if err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid issue ID: %w", err)
	}
	if _, err := util.ParseUUID(payload.Entry.ID); err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid activity ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid workspace ID: %w", err)
	}
	issue, err := queries.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue for activity-created receipt: %w", err)
	}
	if util.UUIDToString(issue.WorkspaceID) != util.UUIDToString(workspaceID) {
		return nil, fmt.Errorf("activity-created receipt workspace mismatch")
	}
	return nil, nil
}

// consumeIssueCreatedActivity 处理 Issue 创建事件：确认 Issue 存在后写一条
// created 活动（详情为空对象），并派生对应事件。
func consumeIssueCreatedActivity(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-created activity payload")
	}
	_, exists, err := getIssueForProjection(ctx, queries, payload.Issue)
	if err != nil || !exists {
		return nil, err
	}
	created, err := createIssueActivity(ctx, queries, event, payload.Issue, "created", []byte("{}"))
	if err != nil {
		return nil, err
	}
	return []events.Event{created}, nil
}

// activitySpec 描述一条待创建的活动：动作名 + 详情 JSON。详情里通常存
// "from/to" 之类的变更前后值。
type activitySpec struct {
	action  string
	details []byte
}

// consumeIssueUpdatedActivities 处理 Issue 更新事件：遍历各 *Changed 标志，
// 逐个把"确实变了"的字段投影成对应活动条目（status_changed 等），负责人变更
// 额外带上 from_type/from_id/to_type/to_id 等可选详情。一次更新可能派生多条
// 活动事件，全部返回由调度器统一外发。
func consumeIssueUpdatedActivities(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-updated activity payload")
	}
	issue := payload.Issue
	_, exists, err := getIssueForProjection(ctx, queries, issue)
	if err != nil || !exists {
		return nil, err
	}
	specs := make([]activitySpec, 0, 7)
	// appendChange：只有 Changed 位为真才产出一条"从旧值到新值"的活动。
	appendChange := func(changed bool, action string, from, to string) {
		if !changed {
			return
		}
		details, _ := json.Marshal(map[string]string{"from": from, "to": to})
		specs = append(specs, activitySpec{action: action, details: details})
	}
	appendChange(payload.StatusChanged, "status_changed", payload.PrevStatus, issue.Status)
	appendChange(payload.PriorityChanged, "priority_changed", payload.PrevPriority, issue.Priority)
	appendChange(payload.StartDateChanged, "start_date_changed", util.ValueOrEmpty(payload.PrevStartDate), util.ValueOrEmpty(issue.StartDate))
	appendChange(payload.DueDateChanged, "due_date_changed", util.ValueOrEmpty(payload.PrevDueDate), util.ValueOrEmpty(issue.DueDate))
	appendChange(payload.TitleChanged, "title_changed", payload.PrevTitle, issue.Title)
	if payload.AssigneeChanged {
		details := map[string]string{}
		setOptionalDetail(details, "from_type", payload.PrevAssigneeType)
		setOptionalDetail(details, "from_id", payload.PrevAssigneeID)
		setOptionalDetail(details, "to_type", issue.AssigneeType)
		setOptionalDetail(details, "to_id", issue.AssigneeID)
		raw, _ := json.Marshal(details)
		specs = append(specs, activitySpec{action: "assignee_changed", details: raw})
	}
	if payload.DescriptionChanged {
		specs = append(specs, activitySpec{action: "description_updated", details: []byte("{}")})
	}

	emitted := make([]events.Event, 0, len(specs))
	for _, spec := range specs {
		created, err := createIssueActivity(ctx, queries, event, issue, spec.action, spec.details)
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, created)
	}
	return emitted, nil
}

// createIssueActivity 把一条活动真正写进 ActivityLog 表：操作者取事件头的
// actor 身份，动作与详情按调用方给定，返回派生的 EventActivityCreated 事件。
func createIssueActivity(ctx context.Context, queries *db.Queries, event events.Event, issue eventIssue, action string, details []byte) (events.Event, error) {
	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: util.MustParseUUID(issue.WorkspaceID),
		IssueID:     util.MustParseUUID(issue.ID),
		ActorType:   util.StrToText(event.ActorType),
		ActorID:     optionalUUID(event.ActorID),
		Action:      action,
		Details:     details,
	})
	if err != nil {
		return events.Event{}, fmt.Errorf("record %s activity for issue %s: %w", action, issue.ID, err)
	}
	return activityCreatedEvent(event, activity), nil
}

// setOptionalDetail 仅在 value 非空时把 key 写进详情 map（"可空字段没动"和
// "被清空"在活动详情里要区分开，所以空值不写）。
func setOptionalDetail(details map[string]string, key string, value *string) {
	if value != nil {
		details[key] = *value
	}
}

// projectTaskActivity 把任务终态（completed/failed）投影成 Issue 上的一条
// 活动：确认任务所属 Issue 存在且与事件工作区一致后，以操作者为 agent 记一条
// 活动。任务不挂任何 Issue 或 Issue 已删时静默跳过。
func projectTaskActivity(ctx context.Context, queries *db.Queries, event events.Event, payload taskEventPayload, action string) ([]events.Event, error) {
	issueID := payload.IssueID
	if issueID == "" {
		return nil, nil
	}

	issue, err := queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue %s for %s activity: %w", issueID, action, err)
	}
	if util.UUIDToString(issue.WorkspaceID) != event.WorkspaceID {
		return nil, fmt.Errorf("task activity workspace mismatch")
	}

	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     util.MustParseUUID(issueID),
		ActorType:   util.StrToText("agent"),
		ActorID:     util.MustParseUUID(payload.AgentID),
		Action:      action,
		Details:     []byte("{}"),
	})
	if err != nil {
		return nil, fmt.Errorf("record %s activity for issue %s: %w", action, issueID, err)
	}
	return []events.Event{activityCreatedEvent(event, activity)}, nil
}

// activityCreatedEvent 把刚落库的活动行包装成 EventActivityCreated 事件：
// 幂等键用 "activity:<id>" 保证同一活动只会被消费一次；StreamKey 指向对应
// Issue 的实时流，让前端能收到增量。活动行本身就是持久凭证，事件是它的实时
// 提示副本。
func activityCreatedEvent(original events.Event, activity db.ActivityLog) events.Event {
	actorType := ""
	if activity.ActorType.Valid {
		actorType = activity.ActorType.String
	}
	return events.Event{
		Type:           protocol.EventActivityCreated,
		IdempotencyKey: "activity:" + util.UUIDToString(activity.ID),
		StreamKey:      "issue:" + util.UUIDToString(activity.IssueID),
		WorkspaceID:    original.WorkspaceID,
		ActorType:      original.ActorType,
		ActorID:        original.ActorID,
		Payload: map[string]any{
			"issue_id": util.UUIDToString(activity.IssueID),
			"entry": map[string]any{
				"type":       "activity",
				"id":         util.UUIDToString(activity.ID),
				"actor_type": actorType,
				"actor_id":   util.UUIDToString(activity.ActorID),
				"action":     activity.Action,
				"details":    json.RawMessage(activity.Details),
				"created_at": util.TimestampToString(activity.CreatedAt),
			},
		},
	}
}
