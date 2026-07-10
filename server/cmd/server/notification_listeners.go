package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// mention represents a parsed @mention from markdown content (local alias).
type mention struct {
	Type string // "member", "agent", "issue", or "all"
	ID   string // user_id, agent_id, issue_id, or "all"
}

// statusLabels maps DB status values to human-readable labels for notifications.
var statusLabels = map[string]string{
	"backlog":     "Backlog",
	"todo":        "Todo",
	"in_progress": "In Progress",
	"in_review":   "In Review",
	"done":        "Done",
	"blocked":     "Blocked",
	"cancelled":   "Cancelled",
}

// priorityLabels maps DB priority values to human-readable labels for notifications.
var priorityLabels = map[string]string{
	"urgent": "Urgent",
	"high":   "High",
	"medium": "Medium",
	"low":    "Low",
	"none":   "No priority",
}

func statusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

func priorityLabel(p string) string {
	if l, ok := priorityLabels[p]; ok {
		return l
	}
	return p
}

var emptyDetails = []byte("{}")

// parseMentions extracts mentions from markdown content.
// Delegates to the shared util.ParseMentions and converts to the local type.
func parseMentions(content string) []mention {
	parsed := util.ParseMentions(content)
	result := make([]mention, len(parsed))
	for i, m := range parsed {
		result[i] = mention{Type: m.Type, ID: m.ID}
	}
	return result
}

// parentBubbleNotifTypes is the allowlist of inbox notification types that
// bubble up from a sub-issue to subscribers of its parent. Other event types
// only notify subscribers of the sub-issue itself, to keep parent watchers'
// inboxes focused on the signal that matters most: status transitions.
var parentBubbleNotifTypes = map[string]bool{
	"status_changed": true,
}

// notifTypeToGroup maps each InboxItemType to a user-configurable preference
// group. Types not in this map are always delivered (not configurable).
var notifTypeToGroup = map[string]string{
	"issue_assigned":     "assignments",
	"unassigned":         "assignments",
	"assignee_changed":   "assignments",
	"status_changed":     "status_changes",
	"new_comment":        "comments",
	"mentioned":          "comments",
	"priority_changed":   "updates",
	"start_date_changed": "updates",
	"due_date_changed":   "updates",
	"task_completed":     "agent_activity",
	"task_failed":        "agent_activity",
	"agent_blocked":      "agent_activity",
	"agent_completed":    "agent_activity",
}

// isNotifMuted returns true if the given notification type is muted for a user
// based on their parsed preferences map.
func isNotifMuted(prefs map[string]string, notifType string) bool {
	group, ok := notifTypeToGroup[notifType]
	if !ok {
		return false // unconfigurable types are always delivered
	}
	return prefs[group] == "muted"
}

// loadUserPrefs loads notification preferences for a set of user IDs in a
// workspace. Returns a map from user_id string to parsed preferences.
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
		WorkspaceID: parseUUID(workspaceID),
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

// terminalStatusForTaskFailedDismiss is the set of issue statuses that mark
// the issue as "the user no longer needs to triage past failures." When a
// status change lands on one of these, any pre-existing task_failed inbox
// rows for the issue are archived so the inbox stays a fresh-signal surface.
// `in_review` is included because in Multica's agent flow that's the most
// reliable "work delivered" handoff — and a status flip back to in_progress
// will simply produce new task_failed rows that surface normally.
var terminalStatusForTaskFailedDismiss = map[string]bool{
	"in_review": true,
	"done":      true,
	"cancelled": true,
}

// archiveStaleTaskFailedInbox archives all task_failed inbox rows for the
// given issue and notifies each affected member recipient via
// inbox:batch-archived so connected clients self-heal.
func archiveStaleTaskFailedInbox(
	ctx context.Context,
	queries *db.Queries,
	bus *events.Bus,
	workspaceID string,
	issueID string,
) error {
	rows, err := queries.ArchiveInboxByIssueAndType(ctx, db.ArchiveInboxByIssueAndTypeParams{
		WorkspaceID: parseUUID(workspaceID),
		IssueID:     parseUUID(issueID),
		Type:        "task_failed",
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	// Dedupe recipients: the listener creates one row per failure event per
	// subscriber, so a long-running issue can yield several rows for the
	// same recipient.
	counts := map[string]int{}
	for _, row := range rows {
		// Inbox rows for task_failed only target member recipients today
		// (notifySubscribers skips agent subscribers), but defend the WS
		// layer against future widening — only members get a personal feed.
		if row.RecipientType != "member" {
			continue
		}
		counts[util.UUIDToString(row.RecipientID)]++
	}

	for recipientID, count := range counts {
		bus.Publish(events.Event{
			Type:        protocol.EventInboxBatchArchived,
			WorkspaceID: workspaceID,
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
	return nil
}

// notifySubscribers queries the subscriber table for an issue, excludes the
// actor and any extra IDs, and creates inbox items for each remaining member
// subscriber. Publishes an inbox:new event for each notification.
// If the issue has a parent and the notification type is in the bubble
// allowlist, parent issue subscribers are also notified (deduplicated
// against direct subscribers).
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

	// Only a small allowlist of event types bubbles to parent subscribers.
	if !parentBubbleNotifTypes[notifType] {
		return nil
	}

	// Also notify parent issue subscribers if this is a sub-issue.
	issue, err := queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		return err
	}
	if !issue.ParentIssueID.Valid {
		return nil
	}

	// Merge already-notified IDs into exclude set for parent subscribers.
	parentExclude := make(map[string]bool, len(exclude)+len(notified))
	for id := range exclude {
		parentExclude[id] = true
	}
	for id := range notified {
		parentExclude[id] = true
	}

	// Query subscribers from the parent issue, but the inbox item still
	// points to the sub-issue so the user navigates to the actual change.
	parentID := util.UUIDToString(issue.ParentIssueID)
	_, err = notifyIssueSubscribers(ctx, queries, bus,
		parentID, issueID, issueStatus, workspaceID, e, parentExclude,
		notifType, severity, title, body, details)
	return err
}

// notifyIssueSubscribers sends inbox notifications to subscribers of
// subscriberIssueID, but creates inbox items pointing to targetIssueID.
// This allows querying subscribers from a parent issue while the notification
// links to the sub-issue where the change actually occurred.
// Returns the set of member IDs that were notified.
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

	subs, err := queries.ListIssueSubscribers(ctx, parseUUID(subscriberIssueID))
	if err != nil {
		return nil, err
	}

	// Batch-load notification preferences for all member subscribers.
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
		// Only notify member-type subscribers (not agents)
		if sub.UserType != "member" {
			continue
		}

		subID := util.UUIDToString(sub.UserID)

		// Skip the actor
		if subID == e.ActorID {
			continue
		}

		// Skip any extra excluded IDs
		if exclude[subID] {
			continue
		}

		// Skip if this notification type is muted by the user
		if prefs, ok := userPrefs[subID]; ok && isNotifMuted(prefs, notifType) {
			continue
		}

		item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			WorkspaceID:   parseUUID(workspaceID),
			RecipientType: "member",
			RecipientID:   sub.UserID,
			Type:          notifType,
			Severity:      severity,
			IssueID:       parseUUID(targetIssueID),
			Title:         title,
			Body:          util.StrToText(body),
			ActorType:     util.StrToText(e.ActorType),
			ActorID:       optionalUUID(e.ActorID),
			Details:       details,
		})
		if err != nil {
			return nil, err
		}

		notified[subID] = true
		resp := inboxItemToResponse(item)
		resp["issue_status"] = issueStatus
		bus.Publish(events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: workspaceID,
			ActorType:   e.ActorType,
			ActorID:     e.ActorID,
			Payload:     map[string]any{"item": resp},
		})
	}

	return notified, nil
}

// notifyDirect creates an inbox item for a specific recipient. Skips if the
// recipient is the actor. Publishes an inbox:new event on success.
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
	body string,
	details []byte,
) error {
	if !supportsInboxRecipientType(recipientType) {
		return nil
	}
	// Skip if recipient is the actor
	if recipientID == e.ActorID {
		return nil
	}

	// Check notification preferences for member recipients.
	if recipientType == "member" {
		prefs, err := loadUserPrefs(ctx, queries, workspaceID, []string{recipientID})
		if err != nil {
			return err
		}
		if p, ok := prefs[recipientID]; ok && isNotifMuted(p, notifType) {
			return nil
		}
	}

	item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   parseUUID(workspaceID),
		RecipientType: recipientType,
		RecipientID:   parseUUID(recipientID),
		Type:          notifType,
		Severity:      severity,
		IssueID:       parseUUID(issueID),
		Title:         title,
		Body:          util.StrToText(body),
		ActorType:     util.StrToText(e.ActorType),
		ActorID:       optionalUUID(e.ActorID),
		Details:       details,
	})
	if err != nil {
		return err
	}

	resp := inboxItemToResponse(item)
	resp["issue_status"] = issueStatus
	bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: workspaceID,
		ActorType:   e.ActorType,
		ActorID:     e.ActorID,
		Payload:     map[string]any{"item": resp},
	})
	return nil
}

func supportsInboxRecipientType(recipientType string) bool {
	return recipientType == "member" || recipientType == "agent"
}

// notifyMentionedMembers creates inbox items for each @mentioned member,
// excluding the actor and any IDs in the skip set. When an @all mention is
// present, all workspace members are notified (excluding agents).
func notifyMentionedMembers(
	ctx context.Context,
	bus *events.Bus,
	queries *db.Queries,
	e events.Event,
	mentions []mention,
	issueID string,
	issueTitle string,
	issueStatus string,
	title string,
	skip map[string]bool,
	details []byte,
) error {
	// Collect the set of member IDs to notify.
	recipientIDs := map[string]bool{}

	hasAll := false
	var squadIDs []string
	for _, m := range mentions {
		if m.Type == "all" {
			hasAll = true
			continue
		}
		if m.Type == "member" {
			recipientIDs[m.ID] = true
		}
		if m.Type == "squad" {
			squadIDs = append(squadIDs, m.ID)
		}
	}

	// Expand each @squad mention to its human members. Agent members of a
	// squad are reached via comment-trigger / assignment paths, not the
	// mention-inbox path, so we only seed member-typed recipients here.
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

	// If @all is present, expand to all workspace members.
	if hasAll {
		members, err := queries.ListMembers(ctx, parseUUID(e.WorkspaceID))
		if err != nil {
			return err
		}
		for _, m := range members {
			recipientIDs[util.UUIDToString(m.UserID)] = true
		}
	}

	// Batch-load notification preferences for all mention recipients.
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
		// Skip if mentions/comments are muted by this user
		if p, ok := mentionPrefs[id]; ok && isNotifMuted(p, "mentioned") {
			continue
		}
		item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			WorkspaceID:   parseUUID(e.WorkspaceID),
			RecipientType: "member",
			RecipientID:   parseUUID(id),
			Type:          "mentioned",
			Severity:      "info",
			IssueID:       parseUUID(issueID),
			Title:         title,
			ActorType:     util.StrToText(e.ActorType),
			ActorID:       optionalUUID(e.ActorID),
			Details:       details,
		})
		if err != nil {
			return err
		}
		resp := inboxItemToResponse(item)
		resp["issue_status"] = issueStatus
		bus.Publish(events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: e.WorkspaceID,
			ActorType:   e.ActorType,
			ActorID:     e.ActorID,
			Payload:     map[string]any{"item": resp},
		})
	}
	return nil
}

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

func consumeIssueCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-created")
	if err != nil || !exists {
		return nil, err
	}
	return projectIssueCreatedNotifications(ctx, queries, event, payload)
}

func projectIssueCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	collector := newNotificationEventCollector()
	skip := map[string]bool{event.ActorID: true}
	if issue.AssigneeType != nil && issue.AssigneeID != nil {
		skip[*issue.AssigneeID] = true
		if err := notifyDirect(ctx, queries, collector.bus,
			*issue.AssigneeType, *issue.AssigneeID,
			issue.WorkspaceID, event, issue.ID, issue.Status,
			"issue_assigned", "action_required", issue.Title, "", emptyDetails,
		); err != nil {
			return nil, err
		}
	}
	if issue.Description != nil {
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, parseMentions(*issue.Description),
			issue.ID, issue.Title, issue.Status, issue.Title, skip, emptyDetails); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

func consumeIssueUpdatedNotifications(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-updated")
	if err != nil || !exists {
		return nil, err
	}
	return projectIssueUpdatedNotifications(ctx, queries, event, payload)
}

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
				"issue_assigned", "action_required", issue.Title, "", assigneeDetails,
			); err != nil {
				return nil, err
			}
		}
		if payload.PrevAssigneeType != nil && payload.PrevAssigneeID != nil && *payload.PrevAssigneeType == "member" {
			if err := notifyDirect(ctx, queries, collector.bus,
				"member", *payload.PrevAssigneeID,
				event.WorkspaceID, event, issue.ID, issue.Status,
				"unassigned", "info", issue.Title, "", assigneeDetails,
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
		if terminalStatusForTaskFailedDismiss[issue.Status] {
			if err := archiveStaleTaskFailedInbox(ctx, queries, collector.bus, event.WorkspaceID, issue.ID); err != nil {
				return nil, err
			}
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
		details, _ := json.Marshal(map[string]string{"from": valueOrEmpty(payload.PrevStartDate), "to": valueOrEmpty(issue.StartDate)})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "start_date_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
	}
	if payload.DueDateChanged {
		details, _ := json.Marshal(map[string]string{"from": valueOrEmpty(payload.PrevDueDate), "to": valueOrEmpty(issue.DueDate)})
		if err := notifySubscribers(ctx, queries, collector.bus, issue.ID, issue.Status, event.WorkspaceID, event,
			nil, "due_date_changed", "info", issue.Title, "", details); err != nil {
			return nil, err
		}
	}
	if payload.DescriptionChanged && issue.Description != nil {
		previous := map[string]bool{}
		if payload.PrevDescription != nil {
			for _, mentioned := range parseMentions(*payload.PrevDescription) {
				previous[mentioned.Type+":"+mentioned.ID] = true
			}
		}
		added := make([]mention, 0)
		for _, mentioned := range parseMentions(*issue.Description) {
			if !previous[mentioned.Type+":"+mentioned.ID] {
				added = append(added, mentioned)
			}
		}
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, added,
			issue.ID, issue.Title, issue.Status, issue.Title,
			map[string]bool{event.ActorID: true}, emptyDetails); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

func consumeCommentCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadCommentProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	if payload.Comment.AuthorType == "system" {
		return nil, nil
	}
	return projectCommentCreatedNotifications(ctx, queries, event, payload)
}

func projectCommentCreatedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload commentEventPayload) ([]events.Event, error) {
	comment := payload.Comment
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
	mentions := parseMentions(comment.Content)
	if len(mentions) > 0 {
		if err := notifyMentionedMembers(ctx, collector.bus, queries, event, mentions,
			comment.IssueID, payload.IssueTitle, payload.IssueStatus, payload.IssueTitle,
			map[string]bool{event.ActorID: true}, details); err != nil {
			return nil, err
		}
	}
	return collector.events, nil
}

func projectTaskFailedNotifications(ctx context.Context, queries *db.Queries, event events.Event, payload taskEventPayload) ([]events.Event, error) {
	if payload.IssueID == "" {
		return nil, nil
	}
	issue, err := queries.GetIssue(ctx, parseUUID(payload.IssueID))
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
	if err := notifySubscribers(ctx, queries, collector.bus,
		payload.IssueID,
		issue.Status,
		event.WorkspaceID,
		events.Event{
			Type:        event.Type,
			WorkspaceID: event.WorkspaceID,
			ActorType:   "agent",
			ActorID:     payload.AgentID,
		},
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

func setAnyOptionalDetail(details map[string]any, key string, value *string) {
	if value != nil {
		details[key] = *value
	}
}

// inboxItemToResponse converts a db.InboxItem into a map suitable for
// JSON-serializable event payloads (mirrors handler.inboxToResponse fields).
func inboxItemToResponse(item db.InboxItem) map[string]any {
	return map[string]any{
		"id":             util.UUIDToString(item.ID),
		"workspace_id":   util.UUIDToString(item.WorkspaceID),
		"recipient_type": item.RecipientType,
		"recipient_id":   util.UUIDToString(item.RecipientID),
		"type":           item.Type,
		"severity":       item.Severity,
		"issue_id":       util.UUIDToPtr(item.IssueID),
		"title":          item.Title,
		"body":           util.TextToPtr(item.Body),
		"read":           item.Read,
		"archived":       item.Archived,
		"created_at":     util.TimestampToString(item.CreatedAt),
		"actor_type":     util.TextToPtr(item.ActorType),
		"actor_id":       util.UUIDToPtr(item.ActorID),
		"details":        json.RawMessage(item.Details),
	}
}
