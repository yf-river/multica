// quick_create_projection.go
//
// 职责：以可靠事件消费的方式收尾"快速创建（quick create）"流程。
// 用户给一句话让 Agent 干活（不先建 Issue），任务完成后若 Agent 真建出了
// Issue，这里负责把任务与 Issue 关联、把发起者加为订阅者（creator），再往
// 发起者的收件箱投一条 quick_create_done；若 Agent 空手而归或任务失败，则
// 投一条 quick_create_failed 让用户知道要人工介入。所有通知只发给发起者本人。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// quickCreateProjectionConsumer 是"快速创建投影"这个可靠消费者的名字。
const quickCreateProjectionConsumer = "quick_create_projection"

// quickCreateProjection 是投影所需的上下文聚合：任务行、从任务上下文里解出的
// 原始请求、工作区/发起者 ID，以及发起者当前是否仍是该工作区成员
// （requesterActive）——不是的话收尾通知就不发了（人已不在，投了也是打扰）。
type quickCreateProjection struct {
	task            db.AgentTaskQueue
	request         service.QuickCreateContext
	workspaceID     pgtype.UUID
	requesterID     pgtype.UUID
	requesterActive bool
}

// registerDurableQuickCreateConsumers 注册快速创建终态投影消费者，订阅任务
// 完成/失败两类事件。
func registerDurableQuickCreateConsumers(dispatcher *eventoutbox.Dispatcher) error {
	return registerDurableEvents(dispatcher, quickCreateProjectionConsumer, consumeQuickCreateTerminalProjection,
		protocol.EventTaskCompleted, protocol.EventTaskFailed)
}

// consumeQuickCreateTerminalProjection 处理快速创建任务的终态事件：
//   - 完成：投影"成功收尾"（建了 Issue 才发完成通知，否则按失败处理）；
//   - 失败：发起者已不是成员就不打扰，否则投影失败收尾通知。
func consumeQuickCreateTerminalProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	projection, exists, err := loadQuickCreateProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}

	switch event.Type {
	case protocol.EventTaskCompleted:
		return projectQuickCreateCompleted(ctx, queries, event, projection)
	case protocol.EventTaskFailed:
		if !projection.requesterActive {
			return nil, nil
		}
		message := ""
		if projection.task.Error.Valid {
			message = projection.task.Error.String
		}
		return projectQuickCreateFailed(ctx, queries, event, projection, message)
	default:
		return nil, fmt.Errorf("unsupported quick-create terminal event %q", event.Type)
	}
}

// loadQuickCreateProjection 加载快速创建投影上下文：先按通用任务规则校验任务
// 行，再确认任务确实带 quick-create 上下文，事件与上下文/Agent 的工作区都一致，
// 最后查发起者是否仍是该工作区成员（查不到视为非活跃，不报错）。
func loadQuickCreateProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
) (quickCreateProjection, bool, error) {
	_, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists {
		return quickCreateProjection{}, exists, err
	}
	quickCreate, ok := service.ParseQuickCreateContext(task)
	if !ok {
		return quickCreateProjection{}, false, nil
	}
	workspaceID, err := util.ParseUUID(quickCreate.WorkspaceID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection has invalid workspace ID: %w", err)
	}
	requesterID, err := util.ParseUUID(quickCreate.RequesterID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection has invalid requester ID: %w", err)
	}
	if event.WorkspaceID != quickCreate.WorkspaceID {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection workspace mismatch")
	}
	agent, err := queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("load quick-create agent: %w", err)
	}
	if util.UUIDToString(agent.WorkspaceID) != quickCreate.WorkspaceID {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create agent workspace mismatch")
	}
	projection := quickCreateProjection{
		task:        task,
		request:     quickCreate,
		workspaceID: workspaceID,
		requesterID: requesterID,
	}
	_, err = queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      requesterID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projection, true, nil
	}
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("load quick-create requester membership: %w", err)
	}
	projection.requesterActive = true
	return projection, true, nil
}

// projectQuickCreateCompleted 处理快速创建"任务完成"：
//  1. 按 origin=quick_create 找到 Agent 建出的 Issue；若根本没有建出 Issue，
//     说明这次快速创建名存实亡，按失败路径发通知；
//  2. 把任务链接到该 Issue（LinkTaskToIssue）并回读确认生效；
//  3. 发起者仍是成员时，把他加为该 Issue 的 creator 订阅者，并投一条
//     quick_create_done 收件箱消息（带 Issue 编号与原提示词）；
//  4. 派发订阅者与收件箱两个事件。
func projectQuickCreateCompleted(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	projection quickCreateProjection,
) ([]events.Event, error) {
	issue, err := queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: projection.workspaceID,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    projection.task.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if !projection.requesterActive {
			return nil, nil
		}
		return projectQuickCreateFailed(ctx, queries, event, projection, "agent finished without creating an issue")
	}
	if err != nil {
		return nil, fmt.Errorf("load quick-created issue: %w", err)
	}
	if err := queries.LinkTaskToIssue(ctx, db.LinkTaskToIssueParams{ID: projection.task.ID, IssueID: issue.ID}); err != nil {
		return nil, fmt.Errorf("link quick-create task to issue: %w", err)
	}
	linked, err := queries.GetAgentTask(ctx, projection.task.ID)
	if err != nil {
		return nil, fmt.Errorf("reload linked quick-create task: %w", err)
	}
	if !linked.IssueID.Valid || util.UUIDToString(linked.IssueID) != util.UUIDToString(issue.ID) {
		return nil, fmt.Errorf("quick-create task link was not applied")
	}
	if !projection.requesterActive {
		return nil, nil
	}
	if _, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID:  issue.ID,
		UserType: "member",
		UserID:   projection.requesterID,
		Reason:   "creator",
	}); err != nil {
		return nil, fmt.Errorf("subscribe quick-create requester: %w", err)
	}
	workspace, err := queries.GetWorkspace(ctx, projection.workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load quick-create workspace: %w", err)
	}
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(projection.task.ID),
		"agent_id":        util.UUIDToString(projection.task.AgentID),
		"issue_id":        util.UUIDToString(issue.ID),
		"identifier":      fmt.Sprintf("%s-%d", workspace.IssuePrefix, issue.Number),
		"original_prompt": projection.request.Prompt,
	})
	item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   projection.workspaceID,
		RecipientType: "member",
		RecipientID:   projection.requesterID,
		Type:          "quick_create_done",
		Severity:      "info",
		IssueID:       issue.ID,
		Title:         issue.Title,
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       projection.task.AgentID,
		Details:       details,
	})
	if err != nil {
		return nil, fmt.Errorf("create quick-create completion inbox: %w", err)
	}
	return []events.Event{
		quickCreateSubscriberEvent(event, projection, issue),
		quickCreateInboxEvent(event, projection.task, item, issue.Status),
	}, nil
}

// projectQuickCreateFailed 处理快速创建失败（或"完成了却没建出 Issue"）：
// 错误信息去空白、脱敏后放进收件箱消息的正文与详情，投一条
// quick_create_failed（severity=action_required，提醒用户要人工处理）。
func projectQuickCreateFailed(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	projection quickCreateProjection,
	message string,
) ([]events.Event, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Quick create did not finish successfully"
	}
	message = redact.Text(message)
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(projection.task.ID),
		"agent_id":        util.UUIDToString(projection.task.AgentID),
		"original_prompt": projection.request.Prompt,
		"error":           message,
	})
	item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   projection.workspaceID,
		RecipientType: "member",
		RecipientID:   projection.requesterID,
		Type:          "quick_create_failed",
		Severity:      "action_required",
		Title:         "Quick create failed",
		Body:          pgtype.Text{String: message, Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       projection.task.AgentID,
		Details:       details,
	})
	if err != nil {
		return nil, fmt.Errorf("create quick-create failure inbox: %w", err)
	}
	return []events.Event{quickCreateInboxEvent(event, projection.task, item, "")}, nil
}

// quickCreateSubscriberEvent 组装"发起者已成为 Issue 订阅者"的事件，通知各
// 端更新订阅关系。actor 记成 agent（毕竟真正"建 Issue"的是它）。
func quickCreateSubscriberEvent(event events.Event, projection quickCreateProjection, issue db.Issue) events.Event {
	return events.Event{
		Type:        protocol.EventSubscriberAdded,
		WorkspaceID: event.WorkspaceID,
		ActorType:   "agent",
		ActorID:     util.UUIDToString(projection.task.AgentID),
		Payload: map[string]any{
			"issue_id":  util.UUIDToString(issue.ID),
			"user_type": "member",
			"user_id":   projection.request.RequesterID,
			"reason":    "creator",
		},
	}
}

// quickCreateInboxEvent 把刚落库的收件箱条目包装成 EventInboxNew 事件，附上
// Issue 状态（失败路径没有 Issue，传空串）。收件箱实时推送按 recipient 定向。
func quickCreateInboxEvent(event events.Event, task db.AgentTaskQueue, item db.InboxItem, issueStatus string) events.Event {
	response := service.InboxItemFields(item)
	response["issue_status"] = issueStatus
	return events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: event.WorkspaceID,
		ActorType:   "agent",
		ActorID:     util.UUIDToString(task.AgentID),
		Payload:     map[string]any{"item": response},
	}
}
