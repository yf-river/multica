package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type terminalTaskFixture struct {
	IssueID string
	TaskID  string
}

func newTerminalTaskFixture(t *testing.T, title string) terminalTaskFixture {
	t.Helper()
	issue := createHandlerCommentIssueFixture(t, title)
	cleanupCommentOutboxForIssue(t, issue.ID)

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, started_at)
		SELECT id, runtime_id, $2, 'running', now()
		FROM agent
		WHERE workspace_id = $1 AND archived_at IS NULL
		ORDER BY created_at, id
		LIMIT 1
		RETURNING id
	`, testWorkspaceID, issue.ID).Scan(&taskID); err != nil {
		t.Fatalf("create running terminal task: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return terminalTaskFixture{IssueID: issue.ID, TaskID: taskID}
}

func TestCompleteTaskCommitsDurableTerminalEvent(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task complete durable event")
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		util.MustParseUUID(fixture.TaskID),
		[]byte(`{}`),
		"",
		"",
	); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	assertTerminalTaskEvent(t, fixture, protocol.EventTaskCompleted, "completed")
}

func TestCompleteTaskRollsBackWhenTerminalEventCannotBeInserted(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task complete outbox rollback")
	installOutboxStreamFailure(t, "issue:"+fixture.IssueID)

	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		util.MustParseUUID(fixture.TaskID),
		[]byte(`{}`),
		"",
		"",
	); err == nil {
		t.Fatal("CompleteTask succeeded without a durable terminal event")
	}
	assertTaskStatus(t, fixture.TaskID, "running")
	assertNoTerminalTaskEvent(t, fixture.TaskID)
}

func TestFailTaskCommitsDurableTerminalEvent(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task failure durable event")
	if _, err := testHandler.TaskService.FailTask(
		context.Background(),
		util.MustParseUUID(fixture.TaskID),
		"daemon reported a deterministic failure",
		"",
		"",
		"agent_error",
	); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	assertTerminalTaskEvent(t, fixture, protocol.EventTaskFailed, "failed")
}

func TestFailTaskRollsBackWhenTerminalEventCannotBeInserted(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task failure outbox rollback")
	installOutboxStreamFailure(t, "issue:"+fixture.IssueID)

	if _, err := testHandler.TaskService.FailTask(
		context.Background(),
		util.MustParseUUID(fixture.TaskID),
		"forced daemon failure",
		"",
		"",
		"agent_error",
	); err == nil {
		t.Fatal("FailTask succeeded without a durable terminal event")
	}
	assertTaskStatus(t, fixture.TaskID, "running")
	assertNoTerminalTaskEvent(t, fixture.TaskID)
}

func TestFailStaleTasksRollsBackWhenTerminalEventCannotBeInserted(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Stale task outbox rollback")
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET dispatched_at = now() - interval '3 hours',
		    started_at = now() - interval '3 hours'
		WHERE id = $1
	`, fixture.TaskID); err != nil {
		t.Fatalf("age running task: %v", err)
	}
	installOutboxStreamFailure(t, "issue:"+fixture.IssueID)

	if _, err := testHandler.TaskService.FailStaleTasks(context.Background(), db.FailStaleTasksParams{
		DispatchTimeoutSecs: 300,
		RunningTimeoutSecs:  1,
	}); err == nil {
		t.Fatal("FailStaleTasks succeeded without durable terminal events")
	}
	assertTaskStatus(t, fixture.TaskID, "running")
	assertNoTerminalTaskEvent(t, fixture.TaskID)
}

func TestCancelTaskRollsBackWhenTerminalEventCannotBeInserted(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task cancellation outbox rollback")
	installOutboxStreamFailure(t, "issue:"+fixture.IssueID)

	if _, err := testHandler.TaskService.CancelTask(context.Background(), util.MustParseUUID(fixture.TaskID)); err == nil {
		t.Fatal("CancelTask succeeded without a durable terminal event")
	}
	assertTaskStatus(t, fixture.TaskID, "running")
	assertNoTerminalTaskEvent(t, fixture.TaskID)
}

func TestCancelTaskCommitsDurableTerminalEvent(t *testing.T) {
	fixture := newTerminalTaskFixture(t, "Task cancellation durable event")
	if _, err := testHandler.TaskService.CancelTask(context.Background(), util.MustParseUUID(fixture.TaskID)); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	assertTerminalTaskEvent(t, fixture, protocol.EventTaskCancelled, "cancelled")
}

func assertTerminalTaskEvent(t *testing.T, fixture terminalTaskFixture, eventType, status string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = $1
		  AND stream_key = 'issue:' || $2
		  AND payload ->> 'task_id' = $3
		  AND payload ->> 'status' = $4
	`, eventType, fixture.IssueID, fixture.TaskID, status).Scan(&count); err != nil {
		t.Fatalf("count terminal task event: %v", err)
	}
	if count != 1 {
		t.Fatalf("terminal task events = %d, want 1", count)
	}
}

func assertNoTerminalTaskEvent(t *testing.T, taskID string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type IN ($1, $2, $3) AND payload ->> 'task_id' = $4
	`, protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled, taskID).Scan(&count); err != nil {
		t.Fatalf("count rolled-back terminal task events: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back terminal task left %d events", count)
	}
}

func assertTaskStatus(t *testing.T, taskID, want string) {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM agent_task_queue WHERE id = $1
	`, taskID).Scan(&status); err != nil {
		t.Fatalf("load task status: %v", err)
	}
	if status != want {
		t.Fatalf("task status = %q, want %q", status, want)
	}
}
