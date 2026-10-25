// notification_listeners.go
//
// 职责：把领域事件翻译成"收件箱通知"并实时推给相关订阅者。
// 这是通知系统的中枢：Issue 创建/更新、评论创建、任务失败等事件进来后，先按
// 场景算清楚"该通知谁"（订阅者、被 @ 的人、负责人、创建者…），再用可靠的
// 投影式插入（ID 由源事件推导，重试幂等）写收件箱条目，并派发 inbox:new 事件
// 让客户端实时弹出。整个文件贯穿几个关键策略：排除操作者本人、跳过订阅者的
// 静默偏好、把子 Issue 的关键变更"冒泡"给父 Issue 的 watcher、对委派订阅
// （delegated）只投递重要里程碑。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// emptyDetails 是最常见的空详情 JSON（很多通知类型不需要额外细节）。
var emptyDetails = []byte("{}")

// projectedInboxID 给一条通知投影一个稳定的行 ID。消费者的数据库事务可能在
// 插完行后回滚（例如登记投递回执时失败）；用"由源事件推导出的 UUID"重试，
// 就能把重试变成幂等的 upsert 而不是重复的收件箱条目。
func projectedInboxID(event events.Event, recipientType, recipientID, notifType, issueID string) pgtype.UUID {
	return service.ProjectedInboxID(eventIdentity(event), recipientType, recipientID, notifType, issueID)
}

// eventIdentity 为事件生成稳定标识：优先用显式事件 ID，其次幂等键；两者都空
// 就退化为"事件类型+负载 JSON"。它用于把派生通知"钉"在源事件上，保证重放时
// 同一源事件不会重复产生通知。
func eventIdentity(event events.Event) string {
	if event.ID != "" {
		return event.ID
	}
	if event.IdempotencyKey != "" {
		return event.IdempotencyKey
	}
	payload, _ := json.Marshal(event.Payload)
	return event.Type + ":" + string(payload)
}

// parentBubbleNotifTypes 是"从子 Issue 冒泡给父 Issue 订阅者"的通知类型白名单。
// 白名单之外的其它事件类型只通知子 Issue 自己的订阅者——把父 Issue watcher 的
// 收件箱留给最关键的信号：状态流转。
var parentBubbleNotifTypes = map[string]bool{
	"status_changed": true,
}

// delegatedAlwaysNotifTypes 是"委派订阅者（delegated）无论如何都要收到"的
// 通知类型：被 @、任务失败、agent 卡住。这些对委派人（把活派给 agent 的人）
// 始终是必须知晓的。
var delegatedAlwaysNotifTypes = map[string]bool{
	"mentioned":     true,
	"task_failed":   true,
	"agent_blocked": true,
}

// delegatedStatusNotify 是委派订阅者对"状态变更通知"放行的目标状态：只有走到
// in_review / done / cancelled / blocked 这类里程碑才通知，日常的前进式状态
// 不打扰。
var delegatedStatusNotify = map[string]bool{
	"in_review": true,
	"done":      true,
	"cancelled": true,
	"blocked":   true,
}

// deliverToSubscriber 决定某条通知要不要投给某订阅者：非委派订阅者收到完整
// 通知流；委派订阅者只收 delegatedAlwaysNotifTypes 里的类型，以及"走向里程碑"
// 的状态变更（delegatedStatusNotify 判定）。
func deliverToSubscriber(reason, notifType, issueStatus string) bool {
	if reason != "delegated" {
		return true
	}
	if delegatedAlwaysNotifTypes[notifType] {
		return true
	}
	if notifType != "status_changed" {
		return false
	}
	return delegatedStatusNotify[issueStatus]
}

// notifTypeToGroup 把每种收件箱通知类型映射到"用户可配置的偏好分组"。不在表
// 里的类型总是投递（不可配置静默）。
var notifTypeToGroup = map[string]string{
	"issue_assigned":     "assignments",
	"unassigned":         "assignments",
	"assignee_changed":   "assignments",
	"status_changed":     "status_changes",
	"new_comment":        "comments",
	"mentioned":          "mentions",
	"priority_changed":   "updates",
	"start_date_changed": "updates",
	"due_date_changed":   "updates",
	"task_completed":     "agent_activity",
	"task_failed":        "agent_activity",
	"agent_blocked":      "agent_activity",
	"agent_completed":    "agent_activity",
}

// isNotifMuted 依据用户解析后的偏好表判断某通知类型是否被该用户静默。
func isNotifMuted(prefs map[string]string, notifType string) bool {
	group, ok := notifTypeToGroup[notifType]
	if !ok {
		return false // 不可配置的类型总是投递
	}
	return prefs[group] == "muted"
}

// loadUserPrefs 批量加载一批用户在工作区里的通知偏好。返回 user_id 字符串 →
// 解析后的偏好 map；某个用户没配偏好就不在结果里。单条偏好 JSON 解析失败就
// 跳过该用户（当没配处理）。
func loadUserPrefs(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	userIDs []string,
) (map[string]map[string]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	uuids := make([]pgtype.UUID, len(userIDs))
	for i, id := range userIDs {
		parsed, err := util.ParseUUID(id)
		if err != nil {
			return nil, err
		}
		uuids[i] = parsed
	}

	rows, err := queries.ListNotificationPreferencesByUsers(ctx, db.ListNotificationPreferencesByUsersParams{
		WorkspaceID: util.MustParseUUID(workspaceID),
		UserIds:     uuids,
	})
	if err != nil {
		return nil, err
	}

	result := make(map[string]map[string]string, len(rows))
	for _, row := range rows {
		var prefs map[string]string
		if err := json.Unmarshal(row.Preferences, &prefs); err != nil {
			continue
		}
		result[util.UUIDToString(row.UserID)] = prefs
	}
	return result, nil
}

// terminalStatusForTaskFailedDismiss 是"能消除历史 task_failed 通知"的 Issue
// 终态集合：状态走到这些终态之一时，把该 Issue 上先前遗留的 task_failed 收件箱
// 条目全部归档，让收件箱保持"只呈现新鲜信号"。包含 in_review 是因为在 Multica
// 的 agent 流程里那是最可靠的"活已交付"交接点——若之后状态又翻回 in_progress，
// 新产生的 task_failed 行会照常冒出来。
var terminalStatusForTaskFailedDismiss = map[string]bool{
	"in_review": true,
	"done":      true,
	"cancelled": true,
}

// archiveStaleTaskFailedInbox 归档某 Issue 上全部 task_failed 收件箱行，并返回
// 描述这次投影的实时事件。它在 outbox 消费者内执行，所以这些事件由 dispatcher
// 持久化（而不是在消费事务提交前就发布出去）。
func archiveStaleTaskFailedInbox(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	issueID string,
	source events.Event,
) ([]events.Event, error) {
	rows, err := queries.ArchiveInboxByIssueAndType(ctx, db.ArchiveInboxByIssueAndTypeParams{
		WorkspaceID: util.MustParseUUID(workspaceID),
		IssueID:     util.MustParseUUID(issueID),
		Type:        "task_failed",
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 接收者去重：每条失败事件会对每个订阅者产生一行，长期存活的 Issue 可能给
	// 同一接收者攒下多行，归档时按人合并成一条批量事件即可。
	counts := map[string]int{}
	for _, row := range rows {
		// 目前 task_failed 的收件箱行只投给 member 类接收者（notifySubscribers
		// 会跳过 agent 订阅者），但这里仍给 WS 层留一道防线：只有 member 有个人
		// 信息流，未来若扩大到 agent 也不会把事件推给没有个人流的对象。
		if row.RecipientType != "member" {
			continue
		}
		counts[util.UUIDToString(row.RecipientID)]++
	}

	emitted := make([]events.Event, 0, len(counts))
	for recipientID, count := range counts {
		emitted = append(emitted, events.Event{
			Type:           protocol.EventInboxBatchArchived,
			IdempotencyKey: "inbox:batch-archived:" + eventIdentity(source) + ":" + recipientID,
			StreamKey:      "inbox:" + workspaceID + ":" + recipientID,
			WorkspaceID:    workspaceID,
			Payload: map[string]any{
				"recipient_id": recipientID,
				"count":        int64(count),
				"issue_id":     issueID,
				"reason":       "issue_status_terminal",
			},
		})
	}

	slog.Info("auto-archive task_failed inbox: archived stale rows",
		"workspace_id", workspaceID, "issue_id", issueID,
		"row_count", len(rows), "recipient_count", len(counts))
	return emitted, nil
}

// notifySubscribers 查询某 Issue 的订阅者，排除操作者与 extra 指定的 ID，给
// 其余每个 member 订阅者建收件箱条目，并为每条通知发布 inbox:new 事件。若
// Issue 有父 Issue 且通知类型在"冒泡白名单"里，父 Issue 的订阅者也会被通知
// （与直接订阅者去重）。
func notifySubscribers(
	ctx context.Context,
	queries *db.Queries,
	bus *events.Bus,
	issueID string,
	issueStatus string,
	workspaceID string,
	e events.Event,
	exclude map[string]bool,
	notifType string,
	severity string,
	title string,
	body string,
	details []byte,
) error {
	notified, err := notifyIssueSubscribers(ctx, queries, bus,
		issueID, issueID, issueStatus, workspaceID, e, exclude,
		notifType, severity, title, body, details)
	if err != nil {
		return err
	}

	// 只有白名单里的小众事件类型才冒泡给父订阅者。
	if !parentBubbleNotifTypes[notifType] {
		return nil
	}

	// 若这是子 Issue，还要通知父 Issue 的订阅者。
	issue, err := queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		return err
	}
	if !issue.ParentIssueID.Valid {
		return nil
	}

	// 把"已经通知过的人"并入父订阅者的排除集合，避免同一个人被投两遍。
	parentExclude := make(map[string]bool, len(exclude)+len(notified))
	for id := range exclude {
		parentExclude[id] = true
	}
	for id := range notified {
		parentExclude[id] = true
	}
	// 委派订阅的初衷是"只接收重要子里程碑"。当例行的子 Issue 进展冒泡上来时，
	// 不能让同一个人的普通父订阅绕过这个层级。因此查子 Issue 订阅者，把其中
	// 委派订阅且按 deliverToSubscriber 不应收到该通知的人加进排除集。
	childSubscribers, err := queries.ListIssueSubscribers(ctx, util.MustParseUUID(issueID))
	if err != nil {
		return err
	}
	for _, subscriber := range childSubscribers {
		if subscriber.UserType == "member" && subscriber.Reason == "delegated" &&
			!deliverToSubscriber(subscriber.Reason, notifType, issueStatus) {
			parentExclude[util.UUIDToString(subscriber.UserID)] = true
		}
	}

	// 从父 Issue 查订阅者，但收件箱条目仍指向子 Issue——让用户点过去看到真正
	// 发生变更的地方。
	parentID := util.UUIDToString(issue.ParentIssueID)
	_, err = notifyIssueSubscribers(ctx, queries, bus,
		parentID, issueID, issueStatus, workspaceID, e, parentExclude,
		notifType, severity, title, body, details)
	return err
}

// notifyIssueSubscribers 给 subscriberIssueID 的订阅者发收件箱通知，但创建的
// 收件箱条目都指向 targetIssueID——从而可以从父 Issue 查订阅者、同时把通知链
// 到真正发生变更的子 Issue。返回被通知到的 member ID 集合（供上层去重）。
func notifyIssueSubscribers(
	ctx context.Context,
	queries *db.Queries,
	bus *events.Bus,
	subscriberIssueID string,
	targetIssueID string,
	issueStatus string,
	workspaceID string,
	e events.Event,
	exclude map[string]bool,
	notifType string,
	severity string,
	title string,
	body string,
	details []byte,
) (map[string]bool, error) {
	notified := map[string]bool{}

	subs, err := queries.ListIssueSubscribers(ctx, util.MustParseUUID(subscriberIssueID))
	if err != nil {
		return nil, err
	}

	// 先把所有 member 订阅者挑出来，批量加载他们的通知偏好。
	var memberIDs []string
	for _, sub := range subs {
		if sub.UserType == "member" {
			memberIDs = append(memberIDs, util.UUIDToString(sub.UserID))
		}
	}
	userPrefs, err := loadUserPrefs(ctx, queries, workspaceID, memberIDs)
	if err != nil {
		return nil, err
	}

	for _, sub := range subs {
		// 只通知 member 类型订阅者（agent 走别的路径）。
		if sub.UserType != "member" {
			continue
		}

		subID := util.UUIDToString(sub.UserID)

		// 跳过操作者本人
		if subID == e.ActorID {
			continue
		}

		// 跳过额外排除的人
		if exclude[subID] {
			continue
		}
		// 委派层级过滤（见 deliverToSubscriber）
		if !deliverToSubscriber(sub.Reason, notifType, issueStatus) {
			continue
		}

		// 用户把该通知类型静默了则跳过
		if prefs, ok := userPrefs[subID]; ok && isNotifMuted(prefs, notifType) {
			continue
		}

		item, err := queries.CreateProjectedInboxItem(ctx, db.CreateProjectedInboxItemParams{
			ID:            projectedInboxID(e, "member", subID, notifType, targetIssueID),
			WorkspaceID:   util.MustParseUUID(workspaceID),
			RecipientType: "member",
			RecipientID:   sub.UserID,
			Type:          notifType,
			Severity:      severity,
			IssueID:       util.MustParseUUID(targetIssueID),
			Title:         title,
			Body:          util.StrToText(body),
			ActorType:     util.StrToText(e.ActorType),
			ActorID:       optionalUUID(e.ActorID),
			Details:       details,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}

		notified[subID] = true
		resp := service.InboxItemFields(item)
		resp["issue_status"] = issueStatus
		bus.Publish(events.Event{
			Type:           protocol.EventInboxNew,
			IdempotencyKey: "inbox:new:" + eventIdentity(e) + ":" + subID + ":" + notifType,
			StreamKey:      "inbox:" + workspaceID + ":" + subID,
			WorkspaceID:    workspaceID,
			ActorType:      e.ActorType,
			ActorID:        e.ActorID,
			Payload:        map[string]any{"item": resp},
		})
	}

	return notified, nil
}

// notifyDirect 给单个指定接收者建收件箱条目。接收者是操作者本人则跳过。
// 成功则发布 inbox:new 事件。
func notifyDirect(
	ctx context.Context,
	queries *db.Queries,
	bus *events.Bus,
	recipientType string,
	recipientID string,
	workspaceID string,
	e events.Event,
	issueID string,
	issueStatus string,
	notifType string,
	severity string,
	title string,
	details []byte,
) error {
	if !isMemberOrAgentActorType(recipientType) {
		return nil
	}
	// 接收者就是操作者则跳过
	if recipientID == e.ActorID {
		return nil
	}

	// member 接收者要先查通知偏好，静默了就不打扰。
	if recipientType == "member" {
		prefs, err := loadUserPrefs(ctx, queries, workspaceID, []string{recipientID})
		if err != nil {
			return err
		}
		if p, ok := prefs[recipientID]; ok && isNotifMuted(p, notifType) {
			return nil
		}
	}

	item, err := queries.CreateProjectedInboxItem(ctx, db.CreateProjectedInboxItemParams{
		ID:            projectedInboxID(e, recipientType, recipientID, notifType, issueID),
		WorkspaceID:   util.MustParseUUID(workspaceID),
		RecipientType: recipientType,
		RecipientID:   util.MustParseUUID(recipientID),
		Type:          notifType,
		Severity:      severity,
		IssueID:       util.MustParseUUID(issueID),
		Title:         title,
		ActorType:     util.StrToText(e.ActorType),
		ActorID:       optionalUUID(e.ActorID),
		Details:       details,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	resp := service.InboxItemFields(item)
	resp["issue_status"] = issueStatus
	bus.Publish(events.Event{
		Type:           protocol.EventInboxNew,
		IdempotencyKey: "inbox:new:" + eventIdentity(e) + ":" + recipientID + ":" + notifType,
		StreamKey:      "inbox:" + workspaceID + ":" + recipientID,
		WorkspaceID:    workspaceID,
		ActorType:      e.ActorType,
		ActorID:        e.ActorID,
		Payload:        map[string]any{"item": resp},
	})
	return nil
}

// isMemberOrAgentActorType 判断主体类型是否为 member 或 agent（通知只发这两类，
// system 等其余类型不产生站内信）。
func isMemberOrAgentActorType(actorType string) bool {
	return actorType == "member" || actorType == "agent"
}

// notifyMentionedMembers 为每个被 @ 的 member 建收件箱条目，排除操作者与
// skip 集合里的人。出现 @all 时通知整个工作区的成员（agent 除外）。@squad
// 会被展开成其中的成员；成员里的 agent 走评论触发/派发路径，不经由"@ 收件箱"
// 路径。
func notifyMentionedMembers(
	ctx context.Context,
	bus *events.Bus,
	queries *db.Queries,
	e events.Event,
	mentions []util.Mention,
	issueID string,
	issueStatus string,
	title string,
	skip map[string]bool,
	details []byte,
) error {
	// 收集要通知的 member ID 集合。
	recipientIDs := map[string]bool{}

	hasAll := false
	var squadIDs []string
	for _, m := range mentions {
		if m.Type == "all" {
			hasAll = true
			continue
		}
		if m.Type == "member" {
			if _, err := util.ParseUUID(m.ID); err != nil {
				continue
			}
			recipientIDs[m.ID] = true
		}
		if m.Type == "squad" {
			squadIDs = append(squadIDs, m.ID)
		}
	}

	// 把每个 @squad 展开成它的人类成员。squad 里的 agent 成员通过评论触发 /
	// 派发路径触达，不走 @ 收件箱路径，所以这里只种下 member 类接收者。
	for _, sid := range squadIDs {
		squadUUID, err := util.ParseUUID(sid)
		if err != nil {
			continue
		}
		members, err := queries.ListSquadMembers(ctx, squadUUID)
		if err != nil {
			return err
		}
		for _, sm := range members {
			if sm.MemberType == "member" {
				recipientIDs[util.UUIDToString(sm.MemberID)] = true
			}
		}
	}

	// 出现 @all 就展开成全体工作区成员。
	if hasAll {
		members, err := queries.ListMembers(ctx, util.MustParseUUID(e.WorkspaceID))
		if err != nil {
			return err
		}
		for _, m := range members {
			recipientIDs[util.UUIDToString(m.UserID)] = true
		}
	}

	// 批量加载所有 @ 接收者的通知偏好。
	var mentionUserIDs []string
	for id := range recipientIDs {
		if id != e.ActorID && !skip[id] {
			mentionUserIDs = append(mentionUserIDs, id)
		}
	}
	mentionPrefs, err := loadUserPrefs(ctx, queries, e.WorkspaceID, mentionUserIDs)
	if err != nil {
		return err
	}

	for id := range recipientIDs {
		if id == e.ActorID || skip[id] {
			continue
		}
		// 该用户静默了 @/评论则跳过
		if p, ok := mentionPrefs[id]; ok && isNotifMuted(p, "mentioned") {
			continue
		}
		item, err := queries.CreateProjectedInboxItem(ctx, db.CreateProjectedInboxItemParams{
			ID:            projectedInboxID(e, "member", id, "mentioned", issueID),
			WorkspaceID:   util.MustParseUUID(e.WorkspaceID),
			RecipientType: "member",
			RecipientID:   util.MustParseUUID(id),
			Type:          "mentioned",
			Severity:      "info",
			IssueID:       util.MustParseUUID(issueID),
			Title:         title,
			ActorType:     util.StrToText(e.ActorType),
			ActorID:       optionalUUID(e.ActorID),
			Details:       details,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		resp := service.InboxItemFields(item)
		resp["issue_status"] = issueStatus
		bus.Publish(events.Event{
			Type:           protocol.EventInboxNew,
			IdempotencyKey: "inbox:new:" + eventIdentity(e) + ":" + id + ":mentioned",
			StreamKey:      "inbox:" + e.WorkspaceID + ":" + id,
			WorkspaceID:    e.WorkspaceID,
			ActorType:      e.ActorType,
			ActorID:        e.ActorID,
			Payload:        map[string]any{"item": resp},
		})
	}
	return nil
}

// notificationEventCollector 是一个小工具：给"在可靠消费者事务里产生的通知"
// 收集 inbox:new / inbox:batch-archived 事件。因为消费者返回的事件会由调度器
// 持久化后再发布，投影逻辑不能用真总线（会把事务未提交的通知提前发出去），
// 所以这里用一个独立的内存总线把派生事件先收进列表。
type notificationEventCollector struct {
	bus    *events.Bus
	events []events.Event
}

func newNotificationEventCollector() *notificationEventCollector {
	collector := &notificationEventCollector{
		bus:    events.New(),
		events: make([]events.Event, 0, 8),
	}
	record := func(event events.Event) { collector.events = append(collector.events, event) }
	collector.bus.Subscribe(protocol.EventInboxNew, record)
	collector.bus.Subscribe(protocol.EventInboxBatchArchived, record)
	return collector
}

// projectIssueCreatedNotifications 投影"Issue 创建"的通知：给负责人发一条
// issue_assigned（action_required），给描述里 @ 到的人发 mentioned。
func projectIssueCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	collector := newNotificationEventCollector()
	skip := map[string]bool{event.ActorID: true}
	if issue.AssigneeType != nil && issue.AssigneeID != nil {
		skip[*issue.AssigneeID] = true
		if err := notifyDirect(ctx, queries, collector.bus,
			*issue.AssigneeType, *issue.AssigneeID,
			issue.WorkspaceID, event, issue.ID, issue.Status,
			"issue_assigned", "action_required", issue.Title, emptyDetails,
		); err != nil {
			return nil, err
		}
	}
	if issue.Description != nil {
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, util.ParseMentions(*issue.Description),
			issue.ID, issue.Status, issue.Title, skip, emptyDetails); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

// projectIssueUpdatedNotifications 投影"Issue 更新"的通知：负责人变更时给新旧
// 负责人分别发 issue_assigned / unassigned，再给订阅者群发 assignee_changed；
// 状态、优先级、起止日期变更各给订阅者群发对应通知；状态走到终态时顺手归档
// 过期的 task_failed 收件箱；描述变更时只 @ 新增出现在描述里的人。
func projectIssueUpdatedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	collector := newNotificationEventCollector()

	if payload.AssigneeChanged {
		detailsMap := map[string]any{}
		setAnyOptionalDetail(detailsMap, "prev_assignee_type", payload.PrevAssigneeType)
		setAnyOptionalDetail(detailsMap, "prev_assignee_id", payload.PrevAssigneeID)
		setAnyOptionalDetail(detailsMap, "new_assignee_type", issue.AssigneeType)
		setAnyOptionalDetail(detailsMap, "new_assignee_id", issue.AssigneeID)
		assigneeDetails, _ := json.Marshal(detailsMap)

		if issue.AssigneeType != nil && issue.AssigneeID != nil {
			if err := notifyDirect(ctx, queries, collector.bus,
				*issue.AssigneeType, *issue.AssigneeID,
				event.WorkspaceID, event, issue.ID, issue.Status,
				"issue_assigned", "action_required", issue.Title, assigneeDetails,
			); err != nil {
				return nil, err
			}
		}
		if payload.PrevAssigneeType != nil && payload.PrevAssigneeID != nil && *payload.PrevAssigneeType == "member" {
			if err := notifyDirect(ctx, queries, collector.bus,
				"member", *payload.PrevAssigneeID,
				event.WorkspaceID, event, issue.ID, issue.Status,
				"unassigned", "info", issue.Title, assigneeDetails,
			); err != nil {
				return nil, err
			}
		}
		exclude := map[string]bool{}
		if payload.PrevAssigneeID != nil {
			exclude[*payload.PrevAssigneeID] = true
		}
		if issue.AssigneeID != nil {
			exclude[*issue.AssigneeID] = true
		}
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			exclude, "assignee_changed", "info", issue.Title, "", assigneeDetails); err != nil {
			return nil, err
		}
	}

	if payload.StatusChanged {
		details, _ := json.Marshal(map[string]string{"from": payload.PrevStatus, "to": issue.Status})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "status_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
		// Issue 走到终态：把历史 task_failed 通知归档，保持收件箱信号新鲜。
		if terminalStatusForTaskFailedDismiss[issue.Status] {
			archivedEvents, err := archiveStaleTaskFailedInbox(ctx, queries, event.WorkspaceID, issue.ID, event)
			if err != nil {
				return nil, err
			}
			collector.events = append(collector.events, archivedEvents...)
		}
	}
	if payload.PriorityChanged {
		details, _ := json.Marshal(map[string]string{"from": payload.PrevPriority, "to": issue.Priority})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "priority_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
	}
	if payload.StartDateChanged {
		details, _ := json.Marshal(map[string]string{"from": util.ValueOrEmpty(payload.PrevStartDate), "to": util.ValueOrEmpty(issue.StartDate)})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "start_date_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
	}
	if payload.DueDateChanged {
		details, _ := json.Marshal(map[string]string{"from": util.ValueOrEmpty(payload.PrevDueDate), "to": util.ValueOrEmpty(issue.DueDate)})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "due_date_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
	}
	// 描述变更只 @"新增出现的"对象——旧描述里已 @ 过的不重复打扰。
	if payload.DescriptionChanged && issue.Description != nil {
		previous := map[string]bool{}
		if payload.PrevDescription != nil {
			for _, mentioned := range util.ParseMentions(*payload.PrevDescription) {
				previous[mentioned.Type+":"+mentioned.ID] = true
			}
		}
		added := make([]util.Mention, 0)
		for _, mentioned := range util.ParseMentions(*issue.Description) {
			if !previous[mentioned.Type+":"+mentioned.ID] {
				added = append(added, mentioned)
			}
		}
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, added,
			issue.ID, issue.Status, issue.Title,
			map[string]bool{event.ActorID: true}, emptyDetails); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

// projectCommentCreatedNotifications 投影"评论创建"的通知：给 Issue 订阅者群发
// new_comment（正文当通知 body），并 @ 评论里新提到的人。系统账号发的评论不
// 投影（提前返回空）。
func projectCommentCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload commentEventPayload) ([]events.Event, error) {
	comment := payload.Comment
	if !isMemberOrAgentActorType(comment.AuthorType) {
		return nil, nil
	}
	details := emptyDetails
	if comment.ID != "" {
		details, _ = json.Marshal(map[string]string{"comment_id": comment.ID})
	}
	collector := newNotificationEventCollector()
	if err := notifySubscribers(ctx, queries, collector.bus,
		comment.IssueID, payload.IssueStatus, event.WorkspaceID, event,
		nil, "new_comment", "info", payload.IssueTitle, comment.Content, details,
	); err != nil {
		return nil, err
	}
	mentions := util.ParseMentions(comment.Content)
	if len(mentions) > 0 {
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, mentions,
			comment.IssueID, payload.IssueStatus, payload.IssueTitle,
			map[string]bool{event.ActorID: true}, details); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

// projectTaskFailedNotifications 投影"任务失败"的通知：确认任务所属 Issue 存在
// 且工作区一致后，给该 Issue 订阅者群发 task_failed（action_required）。通知
// 的 actor 记为执行失败的 agent（事件头原本可能是别的操作者，这里要如实归因）。
func projectTaskFailedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload taskEventPayload) ([]events.Event, error) {
	if payload.IssueID == "" {
		return nil, nil
	}
	issue, err := queries.GetIssue(ctx, util.MustParseUUID(payload.IssueID))
	if err != nil {
		return nil, err
	}
	if util.UUIDToString(issue.WorkspaceID) != event.WorkspaceID {
		return nil, fmt.Errorf("task failure notification workspace mismatch")
	}
	exclude := map[string]bool{}
	if payload.AgentID != "" {
		exclude[payload.AgentID] = true
	}
	collector := newNotificationEventCollector()
	notificationEvent := event
	notificationEvent.ActorType = "agent"
	notificationEvent.ActorID = payload.AgentID
	if err := notifySubscribers(ctx, queries, collector.bus,
		payload.IssueID,
		issue.Status,
		event.WorkspaceID,
		notificationEvent,
		exclude,
		"task_failed",
		"action_required",
		issue.Title,
		"",
		emptyDetails,
	); err != nil {
		return nil, err
	}
	return collector.events, nil
}

// setAnyOptionalDetail 仅在 value 非空时把 key 写进 map[string]any 详情。
func setAnyOptionalDetail(details map[string]any, key string, value *string) {
	if value != nil {
		details[key] = *value
	}
}
