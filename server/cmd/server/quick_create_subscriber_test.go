package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type quickCreateTaskFixture struct {
	queries *db.Queries
	taskSvc *service.TaskService
	task    db.AgentTaskQueue
	agentID string
}

func TestQuickCreateCompletionLeavesProjectionToOutbox(t *testing.T) {
	ctx := context.Background()
	fixture := setupDispatchedQuickCreateTask(t, ctx, "atomic quick-create projection")
	issue := createQuickCreateIssue(t, ctx, fixture, "agent-filed atomic issue")
	installQuickCreateInboxFailure(t, "quick_create_done")

	if _, err := fixture.taskSvc.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	persisted, err := fixture.queries.GetAgentTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if persisted.IssueID.Valid {
		t.Fatalf("CompleteTask linked issue before durable projection: %s", util.UUIDToString(persisted.IssueID))
	}
	if isSubscribed(t, fixture.queries, util.UUIDToString(issue.ID), "member", testUserID) {
		t.Fatal("CompleteTask subscribed requester before durable projection")
	}
	var inboxCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND recipient_id = $2 AND type = 'quick_create_done'
		  AND details->>'task_id' = $3
	`, testWorkspaceID, testUserID, util.UUIDToString(fixture.task.ID)).Scan(&inboxCount); err != nil {
		t.Fatalf("count quick-create inbox: %v", err)
	}
	if inboxCount != 0 {
		t.Fatalf("CompleteTask created %d quick-create inbox rows before projection", inboxCount)
	}
}

func TestQuickCreateCompletionProjectionRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	fixture := setupDispatchedQuickCreateTask(t, ctx, "retry atomic quick-create projection")
	issue := createQuickCreateIssue(t, ctx, fixture, "agent-filed retry issue")
	if _, err := fixture.taskSvc.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	event := latestTaskTerminalEvent(t, fixture.task.ID)
	removeFailure := installQuickCreateInboxFailure(t, "quick_create_done")

	if _, err := runQuickCreateProjection(ctx, fixture.queries, event); err == nil {
		t.Fatal("quick-create projection succeeded despite forced inbox failure")
	}
	persisted, err := fixture.queries.GetAgentTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after failed projection: %v", err)
	}
	if persisted.IssueID.Valid {
		t.Fatalf("failed projection left task linked to issue %s", util.UUIDToString(persisted.IssueID))
	}
	if isSubscribed(t, fixture.queries, util.UUIDToString(issue.ID), "member", testUserID) {
		t.Fatal("failed projection left requester subscribed")
	}

	removeFailure()
	emitted, err := runQuickCreateProjection(ctx, fixture.queries, event)
	if err != nil {
		t.Fatalf("retry quick-create projection: %v", err)
	}
	if len(emitted) != 2 || emitted[0].Type != protocol.EventSubscriberAdded || emitted[1].Type != protocol.EventInboxNew {
		t.Fatalf("projected events = %#v", emitted)
	}
	persisted, err = fixture.queries.GetAgentTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after projection retry: %v", err)
	}
	if !persisted.IssueID.Valid || util.UUIDToString(persisted.IssueID) != util.UUIDToString(issue.ID) {
		t.Fatalf("projected issue link = %s, want %s", util.UUIDToString(persisted.IssueID), util.UUIDToString(issue.ID))
	}
	if !isSubscribed(t, fixture.queries, util.UUIDToString(issue.ID), "member", testUserID) {
		t.Fatal("projection retry did not subscribe requester")
	}
	assertQuickCreateInboxCount(t, ctx, fixture.task.ID, "quick_create_done", 1)
}

func TestQuickCreateTaskFailureProjectsRedactedInbox(t *testing.T) {
	ctx := context.Background()
	fixture := setupDispatchedQuickCreateTask(t, ctx, "failed quick-create projection")
	const secret = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJxdWljay1jcmVhdGUifQ.signature"
	if _, err := fixture.taskSvc.FailTask(
		ctx,
		fixture.task.ID,
		"Authorization: Bearer "+secret,
		"",
		"",
		"api_invalid_request",
	); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	emitted, err := runQuickCreateProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID))
	if err != nil {
		t.Fatalf("project quick-create task failure: %v", err)
	}
	if len(emitted) != 1 || emitted[0].Type != protocol.EventInboxNew {
		t.Fatalf("failure projected events = %#v", emitted)
	}
	assertQuickCreateInboxCount(t, ctx, fixture.task.ID, "quick_create_failed", 1)
	var body string
	var details []byte
	if err := testPool.QueryRow(ctx, `
		SELECT body, details FROM inbox_item
		WHERE type = 'quick_create_failed' AND details->>'task_id' = $1
	`, util.UUIDToString(fixture.task.ID)).Scan(&body, &details); err != nil {
		t.Fatalf("load projected failure inbox: %v", err)
	}
	if strings.Contains(body, secret) || strings.Contains(string(details), secret) {
		t.Fatal("projected quick-create failure inbox leaked bearer token")
	}
}

func runQuickCreateProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	emitted, err := consumeQuickCreateTerminalProjection(ctx, queries.WithTx(tx), event)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return emitted, nil
}

func assertQuickCreateInboxCount(t *testing.T, ctx context.Context, taskID pgtype.UUID, inboxType string, want int) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND recipient_id = $2 AND type = $3
		  AND details->>'task_id' = $4
	`, testWorkspaceID, testUserID, inboxType, util.UUIDToString(taskID)).Scan(&count); err != nil {
		t.Fatalf("count %s inbox rows: %v", inboxType, err)
	}
	if count != want {
		t.Fatalf("%s inbox rows = %d, want %d", inboxType, count, want)
	}
}

func createQuickCreateIssue(t *testing.T, ctx context.Context, fixture quickCreateTaskFixture, title string) db.Issue {
	t.Helper()
	number, err := fixture.queries.IncrementIssueCounter(ctx, parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatalf("IncrementIssueCounter: %v", err)
	}
	issue, err := fixture.queries.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		Title:       title,
		Status:      "todo",
		Priority:    "none",
		CreatorType: "agent",
		CreatorID:   parseUUID(fixture.agentID),
		Number:      number,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    fixture.task.ID,
	})
	if err != nil {
		t.Fatalf("CreateIssueWithOrigin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func installQuickCreateInboxFailure(t *testing.T, inboxType string) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "quick_create_inbox_fail_fn_" + suffix
	triggerName := "quick_create_inbox_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON inbox_item`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.type = '%s' THEN
				RAISE EXCEPTION 'forced quick-create inbox failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON inbox_item
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, inboxType, triggerName, functionName)); err != nil {
		t.Fatalf("install quick-create inbox failure: %v", err)
	}
	return remove
}

func setupDispatchedQuickCreateTask(t *testing.T, ctx context.Context, prompt string) quickCreateTaskFixture {
	t.Helper()

	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)

	var agentID string
	if err := testPool.QueryRow(ctx,
		`SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID,
	).Scan(&agentID); err != nil {
		t.Fatalf("load fixture agent: %v", err)
	}

	task, err := taskSvc.EnqueueQuickCreateTask(ctx, service.EnqueueQuickCreateTaskParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RequesterID: parseUUID(testUserID),
		AgentID:     parseUUID(agentID),
		Prompt:      prompt,
	})
	if err != nil {
		t.Fatalf("EnqueueQuickCreateTask: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
	})

	if _, err := testPool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`,
		task.ID,
	); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}
	if _, err := queries.StartAgentTask(ctx, task.ID); err != nil {
		t.Fatalf("StartAgentTask: %v", err)
	}

	return quickCreateTaskFixture{
		queries: queries,
		taskSvc: taskSvc,
		task:    task,
		agentID: agentID,
	}
}

// TestQuickCreateCompletion_SubscribesRequester locks in the fix for the
// quick-create requester not being subscribed to the issue: the agent runs
// the CLI and is recorded as the issue's creator, so the issue:created event
// only auto-subscribes the agent. The completion path must explicitly
// subscribe the human requester so they receive follow-up notifications.
func TestQuickCreateCompletion_SubscribesRequester(t *testing.T) {
	ctx := context.Background()
	fixture := setupDispatchedQuickCreateTask(t, ctx, "please file a bug")
	issue := createQuickCreateIssue(t, ctx, fixture, "agent-filed bug")

	if _, err := fixture.taskSvc.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, err := runQuickCreateProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID)); err != nil {
		t.Fatalf("project quick-create completion: %v", err)
	}

	if !isSubscribed(t, fixture.queries, util.UUIDToString(issue.ID), "member", testUserID) {
		t.Fatal("expected requester to be subscribed after quick-create completion")
	}
}

// TestQuickCreateFailure_DoesNotSubscribeRequester confirms the failure path
// (agent finished without producing an issue) does not invent a subscriber
// row — there is nothing to subscribe to.
func TestQuickCreateFailure_DoesNotSubscribeRequester(t *testing.T) {
	ctx := context.Background()
	fixture := setupDispatchedQuickCreateTask(t, ctx, "another bug")

	// No issue with origin_type=quick_create + this task id exists. Completion
	// hits the failure branch and writes a failure inbox; no subscriber row.
	if _, err := fixture.taskSvc.CompleteTask(ctx, fixture.task.ID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, err := runQuickCreateProjection(ctx, fixture.queries, latestTaskTerminalEvent(t, fixture.task.ID)); err != nil {
		t.Fatalf("project quick-create failure: %v", err)
	}

	var leaked int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM issue_subscriber s
		JOIN issue i ON i.id = s.issue_id
		WHERE s.user_type = 'member' AND s.user_id = $1
		  AND i.origin_type = 'quick_create' AND i.origin_id = $2
	`, testUserID, fixture.task.ID).Scan(&leaked); err != nil {
		t.Fatalf("count leaked subscribers: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("expected no subscriber rows for failed quick-create, got %d", leaked)
	}
	assertQuickCreateInboxCount(t, ctx, fixture.task.ID, "quick_create_failed", 1)
}
