package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// listActivitiesForIssue is a test helper that fetches up to 100 activity_log
// records for an issue. Uses the same query that backs the timeline endpoint.
func listActivitiesForIssue(t *testing.T, queries *db.Queries, issueID string) []db.ActivityLog {
	t.Helper()
	activities, err := queries.ListActivitiesForIssue(context.Background(), db.ListActivitiesForIssueParams{
		IssueID: util.MustParseUUID(issueID),
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("ListActivitiesForIssue: %v", err)
	}
	return activities
}

func cleanupActivities(t *testing.T, issueID string) {
	t.Helper()
	_, _ = testPool.Exec(context.Background(), `DELETE FROM activity_log WHERE issue_id = $1`, issueID)
}

type activityIssueTestFixture struct {
	t       *testing.T
	queries *db.Queries
	bus     *events.Bus
	issueID string
}

func setupActivityIssueTest(t *testing.T) activityIssueTestFixture {
	t.Helper()
	queries := db.New(testPool)
	bus := events.New()
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupActivities(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	return activityIssueTestFixture{t: t, queries: queries, bus: bus, issueID: issueID}
}

func (fixture activityIssueTestFixture) publish(event events.Event) {
	fixture.t.Helper()
	var (
		emitted []events.Event
		err     error
	)
	switch event.Type {
	case protocol.EventIssueCreated:
		emitted, err = consumeIssueCreatedActivity(context.Background(), fixture.queries, event)
	case protocol.EventIssueUpdated:
		emitted, err = consumeIssueUpdatedActivities(context.Background(), fixture.queries, event)
	case protocol.EventTaskCompleted, protocol.EventTaskFailed:
		payload, ok := decodeTaskEvent(event)
		if !ok {
			fixture.t.Fatalf("decode task activity event")
		}
		action := "task_completed"
		if event.Type == protocol.EventTaskFailed {
			action = "task_failed"
		}
		emitted, err = projectTaskActivity(context.Background(), fixture.queries, event, payload, action)
	default:
		fixture.bus.Publish(event)
		return
	}
	if err != nil {
		fixture.t.Fatalf("consume %s activity: %v", event.Type, err)
	}
	for _, emittedEvent := range emitted {
		fixture.bus.Publish(emittedEvent)
	}
}

func TestActivityIssueCreated(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	fixture.publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: replayedPayload(t, map[string]any{
			"issue": handler.IssueResponse{
				ID:          fixture.issueID,
				WorkspaceID: testWorkspaceID,
				Title:       "activity test issue",
				Status:      "todo",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
		}),
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "created" {
		t.Fatalf("expected action 'created', got %q", activities[0].Action)
	}
	if util.UUIDToString(activities[0].ActorID) != testUserID {
		t.Fatalf("expected actor_id %s, got %s", testUserID, util.UUIDToString(activities[0].ActorID))
	}
}

func TestActivityIssueUpdated_StatusChanged(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	fixture.publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          fixture.issueID,
				WorkspaceID: testWorkspaceID,
				Title:       "activity test issue",
				Status:      "in_progress",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
			"status_changed": true,
			"prev_status":    "todo",
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "status_changed" {
		t.Fatalf("expected action 'status_changed', got %q", activities[0].Action)
	}

	var details map[string]string
	if err := json.Unmarshal(activities[0].Details, &details); err != nil {
		t.Fatalf("failed to unmarshal details: %v", err)
	}
	if details["from"] != "todo" {
		t.Fatalf("expected from 'todo', got %q", details["from"])
	}
	if details["to"] != "in_progress" {
		t.Fatalf("expected to 'in_progress', got %q", details["to"])
	}
}

func TestActivityIssueUpdated_AssigneeChanged(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	assigneeAccount := "activity-assignee-test@multica"
	assigneeID := createTestUser(t, assigneeAccount)
	t.Cleanup(func() { cleanupTestUser(t, assigneeAccount) })

	assigneeType := "member"
	fixture.publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:           fixture.issueID,
				WorkspaceID:  testWorkspaceID,
				Title:        "activity test issue",
				Status:       "todo",
				Priority:     "medium",
				CreatorType:  "member",
				CreatorID:    testUserID,
				AssigneeType: &assigneeType,
				AssigneeID:   &assigneeID,
			},
			"assignee_changed":   true,
			"prev_assignee_type": (*string)(nil),
			"prev_assignee_id":   (*string)(nil),
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "assignee_changed" {
		t.Fatalf("expected action 'assignee_changed', got %q", activities[0].Action)
	}

	var details map[string]string
	if err := json.Unmarshal(activities[0].Details, &details); err != nil {
		t.Fatalf("failed to unmarshal details: %v", err)
	}
	if details["to_type"] != "member" {
		t.Fatalf("expected to_type 'member', got %q", details["to_type"])
	}
	if details["to_id"] != assigneeID {
		t.Fatalf("expected to_id %q, got %q", assigneeID, details["to_id"])
	}
}

func TestActivityIssueUpdated_NoChangeFlags(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	// Publish issue:updated with no change flags set
	fixture.publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          fixture.issueID,
				WorkspaceID: testWorkspaceID,
				Title:       "activity test issue",
				Status:      "todo",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
			"assignee_changed":    false,
			"status_changed":      false,
			"description_changed": false,
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 0 {
		t.Fatalf("expected 0 activities when no change flags, got %d", len(activities))
	}
}

func TestActivityIssueUpdated_TitleChanged(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	fixture.publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          fixture.issueID,
				WorkspaceID: testWorkspaceID,
				Title:       "renamed issue",
				Status:      "todo",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
			"title_changed": true,
			"prev_title":    "activity test issue",
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "title_changed" {
		t.Fatalf("expected action 'title_changed', got %q", activities[0].Action)
	}

	var details map[string]string
	if err := json.Unmarshal(activities[0].Details, &details); err != nil {
		t.Fatalf("failed to unmarshal details: %v", err)
	}
	if details["from"] != "activity test issue" {
		t.Fatalf("expected from 'activity test issue', got %q", details["from"])
	}
	if details["to"] != "renamed issue" {
		t.Fatalf("expected to 'renamed issue', got %q", details["to"])
	}
}

func TestActivityIssueCreatedSkipsIssueDeletedBeforeProjection(t *testing.T) {
	fixture := setupActivityIssueTest(t)
	event := events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          fixture.issueID,
				WorkspaceID: testWorkspaceID,
				Title:       "deleted before projection",
				Status:      "todo",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
		},
	}
	cleanupTestIssue(t, fixture.issueID)

	emitted, err := consumeIssueCreatedActivity(context.Background(), fixture.queries, event)
	if err != nil {
		t.Fatalf("deleted issue should not poison activity projection: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("deleted issue emitted %d activity events, want 0", len(emitted))
	}
}

func TestActivityTaskCompleted(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	agentID := testUserID // reuse as a stand-in for agent ID

	fixture.publish(events.Event{
		Type:        protocol.EventTaskCompleted,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"task_id":  "00000000-0000-0000-0000-000000000001",
			"agent_id": agentID,
			"issue_id": fixture.issueID,
			"status":   "completed",
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "task_completed" {
		t.Fatalf("expected action 'task_completed', got %q", activities[0].Action)
	}
	if util.UUIDToString(activities[0].ActorID) != agentID {
		t.Fatalf("expected actor_id %s, got %s", agentID, util.UUIDToString(activities[0].ActorID))
	}
}

func TestActivityTaskFailed(t *testing.T) {
	fixture := setupActivityIssueTest(t)

	agentID := testUserID

	fixture.publish(events.Event{
		Type:        protocol.EventTaskFailed,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"task_id":  "00000000-0000-0000-0000-000000000002",
			"agent_id": agentID,
			"issue_id": fixture.issueID,
			"status":   "failed",
		},
	})

	activities := listActivitiesForIssue(t, fixture.queries, fixture.issueID)
	if len(activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(activities))
	}
	if activities[0].Action != "task_failed" {
		t.Fatalf("expected action 'task_failed', got %q", activities[0].Action)
	}
}

func TestDurableTaskFailedProjectsActivityAndInboxTogether(t *testing.T) {
	queries := db.New(testPool)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupActivities(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	addTestSubscriber(t, issueID, "member", testUserID, "creator")

	var taskID, agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, started_at, completed_at, error, failure_reason)
		SELECT id, runtime_id, $2, 'failed', now(), now(), 'durable failure', 'agent_error'
		FROM agent
		WHERE workspace_id = $1 AND archived_at IS NULL
		ORDER BY created_at, id
		LIMIT 1
		RETURNING id, agent_id
	`, testWorkspaceID, issueID).Scan(&taskID, &agentID); err != nil {
		t.Fatalf("insert failed task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	event := events.Event{
		Type:        protocol.EventTaskFailed,
		StreamKey:   "issue:" + issueID,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"task_id":  taskID,
			"agent_id": agentID,
			"issue_id": issueID,
			"status":   "failed",
		},
	}
	emitted, err := consumeTaskTerminalIssueProjection(context.Background(), queries, event)
	if err != nil {
		t.Fatalf("consume durable failed task: %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("durable failed task emitted %d events, want activity + inbox", len(emitted))
	}
	activities := listActivitiesForIssue(t, queries, issueID)
	if len(activities) != 1 || activities[0].Action != "task_failed" {
		t.Fatalf("durable failed task activities = %+v", activities)
	}
	items := inboxItemsForRecipient(t, queries, testUserID)
	if len(items) != 1 || items[0].Type != "task_failed" {
		t.Fatalf("durable failed task inbox = %+v", items)
	}
}

func TestDurableTaskProjectionRejectsStatusMismatch(t *testing.T) {
	queries := db.New(testPool)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupActivities(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	var taskID, agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, started_at, completed_at)
		SELECT id, runtime_id, $2, 'completed', now(), now()
		FROM agent
		WHERE workspace_id = $1 AND archived_at IS NULL
		ORDER BY created_at, id
		LIMIT 1
		RETURNING id, agent_id
	`, testWorkspaceID, issueID).Scan(&taskID, &agentID); err != nil {
		t.Fatalf("insert completed task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	_, err := consumeTaskTerminalIssueProjection(context.Background(), queries, events.Event{
		Type:        protocol.EventTaskFailed,
		WorkspaceID: testWorkspaceID,
		Payload: map[string]any{
			"task_id":  taskID,
			"agent_id": agentID,
			"issue_id": issueID,
			"status":   "failed",
		},
	})
	if err == nil {
		t.Fatal("status-mismatched task event was accepted")
	}
	if activities := listActivitiesForIssue(t, queries, issueID); len(activities) != 0 {
		t.Fatalf("status-mismatched event created %d activities", len(activities))
	}
}
