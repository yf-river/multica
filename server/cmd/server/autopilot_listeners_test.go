package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type autopilotListenerFixture struct {
	queries      *db.Queries
	taskSvc      *service.TaskService
	autopilotSvc *service.AutopilotService
	agentID      string
}

func setupAutopilotListenerFixture(t *testing.T) autopilotListenerFixture {
	t.Helper()
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)
	autopilotSvc := service.NewAutopilotService(queries, testPool, bus, taskSvc)

	var agentID string
	if err := testPool.QueryRow(ctx,
		`SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID,
	).Scan(&agentID); err != nil {
		t.Fatalf("load fixture agent: %v", err)
	}

	return autopilotListenerFixture{
		queries:      queries,
		taskSvc:      taskSvc,
		autopilotSvc: autopilotSvc,
		agentID:      agentID,
	}
}

func latestTaskTerminalEvent(t *testing.T, taskID pgtype.UUID) events.Event {
	t.Helper()
	var event events.Event
	var payload []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT event_type,
		       COALESCE(stream_key, ''),
		       COALESCE(workspace_id::text, ''),
		       COALESCE(actor_type, ''),
		       COALESCE(actor_id, ''),
		       COALESCE(task_id, ''),
		       COALESCE(chat_session_id, ''),
		       payload
		FROM domain_event_outbox
		WHERE payload->>'task_id' = $1::text
		  AND event_type IN ('task:completed', 'task:failed', 'task:cancelled')
		ORDER BY sequence_no DESC
		LIMIT 1
	`, util.UUIDToString(taskID)).Scan(
		&event.Type,
		&event.StreamKey,
		&event.WorkspaceID,
		&event.ActorType,
		&event.ActorID,
		&event.TaskID,
		&event.ChatSessionID,
		&payload,
	); err != nil {
		t.Fatalf("load terminal task event: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode terminal task event: %v", err)
	}
	event.Payload = object
	return event
}

func projectTaskTerminalEvent(t *testing.T, queries *db.Queries, taskID pgtype.UUID) []events.Event {
	t.Helper()
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin autopilot projection: %v", err)
	}
	defer tx.Rollback(context.Background())
	emitted, err := consumeAutopilotRunProjection(context.Background(), queries.WithTx(tx), latestTaskTerminalEvent(t, taskID))
	if err != nil {
		t.Fatalf("project terminal task: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit autopilot projection: %v", err)
	}
	return emitted
}

func TestAutopilotRunOnlyTaskTerminalEventsUpdateRun(t *testing.T) {
	ctx := context.Background()
	f := setupAutopilotListenerFixture(t)

	tests := []struct {
		name       string
		finalize   func(task db.AgentTaskQueue)
		wantStatus string
		wantResult string
		wantReason string
	}{
		{
			name: "completed",
			finalize: func(task db.AgentTaskQueue) {
				if _, err := f.taskSvc.CompleteTask(ctx, task.ID, []byte(`{"output":"done"}`), "", ""); err != nil {
					t.Fatalf("CompleteTask: %v", err)
				}
			},
			wantStatus: "completed",
			wantResult: "done",
		},
		{
			name: "failed",
			finalize: func(task db.AgentTaskQueue) {
				if _, err := f.taskSvc.FailTask(ctx, task.ID, "boom", "", "", "agent_error"); err != nil {
					t.Fatalf("FailTask: %v", err)
				}
			},
			wantStatus: "failed",
			wantReason: "boom",
		},
		{
			name: "cancelled",
			finalize: func(task db.AgentTaskQueue) {
				if _, err := f.taskSvc.CancelTask(ctx, task.ID); err != nil {
					t.Fatalf("CancelTask: %v", err)
				}
			},
			wantStatus: "failed",
			wantReason: "task cancelled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ap, err := f.queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
				WorkspaceID:        parseUUID(testWorkspaceID),
				Title:              "Run-only listener " + tc.name,
				Description:        pgtype.Text{String: "Run listener regression test", Valid: true},
				AssigneeType:       "agent",
				AssigneeID:         parseUUID(f.agentID),
				Status:             "active",
				ExecutionMode:      "run_only",
				IssueTitleTemplate: pgtype.Text{},
				CreatedByType:      "member",
				CreatedByID:        parseUUID(testUserID),
			})
			if err != nil {
				t.Fatalf("CreateAutopilot: %v", err)
			}
			t.Cleanup(func() {
				if _, err := testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID); err != nil {
					t.Logf("cleanup autopilot: %v", err)
				}
			})

			run, err := f.autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
			if err != nil {
				t.Fatalf("DispatchAutopilot: %v", err)
			}
			if !run.TaskID.Valid {
				t.Fatal("run_only dispatch did not link a task")
			}

			if _, err := testPool.Exec(ctx,
				`UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`,
				run.TaskID,
			); err != nil {
				t.Fatalf("mark task dispatched: %v", err)
			}
			task, err := f.queries.StartAgentTask(ctx, run.TaskID)
			if err != nil {
				t.Fatalf("StartAgentTask: %v", err)
			}

			tc.finalize(task)
			beforeProjection, err := f.queries.GetAutopilotRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("GetAutopilotRun before projection: %v", err)
			}
			if beforeProjection.Status != "running" {
				t.Fatalf("task completion mutated run before durable projection: %q", beforeProjection.Status)
			}
			if emitted := projectTaskTerminalEvent(t, f.queries, task.ID); len(emitted) != 1 || emitted[0].Type != "autopilot:run_done" {
				t.Fatalf("autopilot projection emitted %+v, want one run_done event", emitted)
			}

			updatedRun, err := f.queries.GetAutopilotRun(ctx, run.ID)
			if err != nil {
				t.Fatalf("GetAutopilotRun: %v", err)
			}
			if updatedRun.Status != tc.wantStatus {
				t.Fatalf("expected run status %q, got %q", tc.wantStatus, updatedRun.Status)
			}
			if tc.wantResult != "" && !strings.Contains(string(updatedRun.Result), tc.wantResult) {
				t.Fatalf("expected run result to contain %q, got %s", tc.wantResult, string(updatedRun.Result))
			}
			if tc.wantReason != "" {
				if !updatedRun.FailureReason.Valid {
					t.Fatalf("expected failure reason %q, got invalid", tc.wantReason)
				}
				if updatedRun.FailureReason.String != tc.wantReason {
					t.Fatalf("expected failure reason %q, got %q", tc.wantReason, updatedRun.FailureReason.String)
				}
			}
		})
	}
}

// linkedIssueAutopilotFixture is the starting state every create_issue
// linked-issue listener test shares: a dispatched create_issue run sitting in
// issue_created with exactly one issue task that carries no autopilot_run_id
// (so the durable projection must reach it through the issue_id lookup).
type linkedIssueAutopilotFixture struct {
	taskSvc *service.TaskService
	queries *db.Queries
	run     *db.AutopilotRun
	taskID  pgtype.UUID
}

// dispatchCreateIssueAutopilot creates an active create_issue autopilot,
// dispatches it, and returns the linked run plus its single issue task.
// Cleanup (autopilot, issue, tasks, comments) is registered on t.
func dispatchCreateIssueAutopilot(t *testing.T, title string) linkedIssueAutopilotFixture {
	t.Helper()
	ctx := context.Background()
	f := setupAutopilotListenerFixture(t)

	ap, err := f.queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Title:              title,
		Description:        pgtype.Text{String: "VEN-661 / VEN-662 regression test", Valid: true},
		AssigneeType:       "agent",
		AssigneeID:         parseUUID(f.agentID),
		Status:             "active",
		ExecutionMode:      "create_issue",
		IssueTitleTemplate: pgtype.Text{String: "Linked issue", Valid: true},
		CreatedByType:      "member",
		CreatedByID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("CreateAutopilot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID)
	})

	run, err := f.autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "schedule", nil)
	if err != nil {
		t.Fatalf("DispatchAutopilot: %v", err)
	}
	if !run.IssueID.Valid {
		t.Fatal("create_issue dispatch did not link an issue")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, run.IssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, run.IssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, run.IssueID)
	})

	tasks, err := f.queries.ListTasksByIssue(ctx, run.IssueID)
	if err != nil {
		t.Fatalf("ListTasksByIssue: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one issue task, got %d", len(tasks))
	}
	if tasks[0].AutopilotRunID.Valid {
		t.Fatal("create_issue issue task unexpectedly has autopilot_run_id; test must exercise linked issue lookup")
	}
	if run.Status != "issue_created" {
		t.Fatalf("expected pre-failure run status issue_created, got %q", run.Status)
	}

	return linkedIssueAutopilotFixture{taskSvc: f.taskSvc, queries: f.queries, run: run, taskID: tasks[0].ID}
}

// runTaskWithBudget marks the issue task dispatched with the given attempt
// budget and transitions it to running, mirroring the daemon claim → start
// flow so FailTask sees a realistic row (and so the auto-retry budget is
// whatever the test wants).
func runTaskWithBudget(t *testing.T, queries *db.Queries, taskID pgtype.UUID, maxAttempts int) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now(), max_attempts = $2 WHERE id = $1`,
		taskID, maxAttempts,
	); err != nil {
		t.Fatalf("mark task dispatched: %v", err)
	}
	if _, err := queries.StartAgentTask(context.Background(), taskID); err != nil {
		t.Fatalf("StartAgentTask: %v", err)
	}
}

func createOfflineLocalAgent(t *testing.T, ctx context.Context, runtimeName, provider, agentName string) string {
	t.Helper()
	var runtimeID, agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'local', $3, 'offline', '{}'::jsonb, '{}'::jsonb, now())
		RETURNING id::text
	`, parseUUID(testWorkspaceID), runtimeName, provider).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id::text
	`, parseUUID(testWorkspaceID), agentName, runtimeID, parseUUID(testUserID)).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}

func createRunOnlyAutopilotForAgent(t *testing.T, ctx context.Context, queries *db.Queries, title, description, agentID string) db.Autopilot {
	t.Helper()
	ap, err := queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Title:              title,
		Description:        pgtype.Text{String: description, Valid: true},
		AssigneeType:       "agent",
		AssigneeID:         parseUUID(agentID),
		Status:             "active",
		ExecutionMode:      "run_only",
		IssueTitleTemplate: pgtype.Text{},
		CreatedByType:      "member",
		CreatedByID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("CreateAutopilot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID)
	})
	return ap
}

// TestAutopilotCreateIssueTaskNoProgressFailureUpdatesRun is the original
// VEN-661 regression: a Codex no-progress failure with no retries left fails
// the linked run.
func TestAutopilotCreateIssueTaskNoProgressFailureUpdatesRun(t *testing.T) {
	ctx := context.Background()
	f := dispatchCreateIssueAutopilot(t, "Create-issue no-progress listener")

	// max_attempts = 1 means the failed attempt has no retry budget left.
	runTaskWithBudget(t, f.queries, f.taskID, 1)

	const errMsg = "codex app-server no progress timeout after 30s"
	if _, err := f.taskSvc.FailTask(ctx, f.taskID, errMsg, "", "", "codex_semantic_inactivity"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	projectTaskTerminalEvent(t, f.queries, f.taskID)

	updatedRun, err := f.queries.GetAutopilotRun(ctx, f.run.ID)
	if err != nil {
		t.Fatalf("GetAutopilotRun: %v", err)
	}
	if updatedRun.Status != "failed" {
		t.Fatalf("expected run status failed, got %q", updatedRun.Status)
	}
	if !updatedRun.FailureReason.Valid || !strings.Contains(updatedRun.FailureReason.String, "no progress timeout") {
		t.Fatalf("expected no-progress failure reason, got %+v", updatedRun.FailureReason)
	}
}

// TestAutopilotCreateIssueTaskAgentErrorFailureUpdatesRun covers the VEN-662
// generalization: an ordinary, non-retryable agent failure must also close the
// linked run instead of leaving it stuck in issue_created.
func TestAutopilotCreateIssueTaskAgentErrorFailureUpdatesRun(t *testing.T) {
	ctx := context.Background()
	f := dispatchCreateIssueAutopilot(t, "Create-issue agent-error listener")

	runTaskWithBudget(t, f.queries, f.taskID, 1)

	// agent_error is not in retryableReasons, so the first terminal failure is
	// final — the run must fail carrying the agent's error text.
	const errMsg = "build failed: ./pkg/foo: undefined: Bar"
	if _, err := f.taskSvc.FailTask(ctx, f.taskID, errMsg, "", "", "agent_error"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	projectTaskTerminalEvent(t, f.queries, f.taskID)

	updatedRun, err := f.queries.GetAutopilotRun(ctx, f.run.ID)
	if err != nil {
		t.Fatalf("GetAutopilotRun: %v", err)
	}
	if updatedRun.Status != "failed" {
		t.Fatalf("expected run status failed, got %q", updatedRun.Status)
	}
	if !updatedRun.FailureReason.Valid || !strings.Contains(updatedRun.FailureReason.String, "build failed") {
		t.Fatalf("expected agent-error failure reason, got %+v", updatedRun.FailureReason)
	}
}

// TestAutopilotCreateIssueTaskRetryPendingKeepsRunOpen locks in the wait guard:
// when FailTask auto-retries a retryable failure (attempt budget remaining), an
// active retry task still exists for the issue, so the run must stay open until
// the final attempt resolves.
func TestAutopilotCreateIssueTaskRetryPendingKeepsRunOpen(t *testing.T) {
	ctx := context.Background()
	f := dispatchCreateIssueAutopilot(t, "Create-issue retry-pending listener")

	// max_attempts = 2 with attempt = 1 leaves budget for one auto-retry.
	runTaskWithBudget(t, f.queries, f.taskID, 2)

	// timeout is retryable, so FailTask enqueues a fresh attempt before it
	// broadcasts the failure event.
	if _, err := f.taskSvc.FailTask(ctx, f.taskID, "runtime went offline", "", "", "timeout"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if emitted := projectTaskTerminalEvent(t, f.queries, f.taskID); len(emitted) != 0 {
		t.Fatalf("retry-pending task emitted terminal autopilot event: %+v", emitted)
	}

	hasActive, err := f.queries.HasActiveTaskForIssue(ctx, f.run.IssueID)
	if err != nil {
		t.Fatalf("HasActiveTaskForIssue: %v", err)
	}
	if !hasActive {
		t.Fatal("expected an active retry task for the issue after a retryable failure")
	}

	updatedRun, err := f.queries.GetAutopilotRun(ctx, f.run.ID)
	if err != nil {
		t.Fatalf("GetAutopilotRun: %v", err)
	}
	if updatedRun.Status != "issue_created" {
		t.Fatalf("expected run to stay issue_created while a retry is pending, got %q", updatedRun.Status)
	}
}

func TestAutopilotDelayedParentFailureDoesNotOverrideCompletedRetry(t *testing.T) {
	ctx := context.Background()
	f := dispatchCreateIssueAutopilot(t, "Delayed parent failure")
	runTaskWithBudget(t, f.queries, f.taskID, 2)
	if _, err := f.taskSvc.FailTask(ctx, f.taskID, "runtime went offline", "", "", "timeout"); err != nil {
		t.Fatalf("fail parent task: %v", err)
	}

	tasks, err := f.queries.ListTasksByIssue(ctx, f.run.IssueID)
	if err != nil {
		t.Fatalf("list retry tasks: %v", err)
	}
	var child db.AgentTaskQueue
	for _, task := range tasks {
		if task.ParentTaskID.Valid && util.UUIDToString(task.ParentTaskID) == util.UUIDToString(f.taskID) {
			child = task
			break
		}
	}
	if !child.ID.Valid {
		t.Fatal("retry child was not created")
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`, child.ID); err != nil {
		t.Fatalf("dispatch retry child: %v", err)
	}
	if _, err := f.queries.StartAgentTask(ctx, child.ID); err != nil {
		t.Fatalf("start retry child: %v", err)
	}
	if _, err := f.taskSvc.CompleteTask(ctx, child.ID, []byte(`{"output":"retry succeeded"}`), "", ""); err != nil {
		t.Fatalf("complete retry child: %v", err)
	}

	if emitted := projectTaskTerminalEvent(t, f.queries, f.taskID); len(emitted) != 0 {
		t.Fatalf("delayed parent failure emitted terminal run event after retry succeeded: %+v", emitted)
	}
	run, err := f.queries.GetAutopilotRun(ctx, f.run.ID)
	if err != nil {
		t.Fatalf("load run after delayed parent event: %v", err)
	}
	if run.Status != "issue_created" {
		t.Fatalf("delayed parent event changed run to %q, want issue_created", run.Status)
	}
}

func TestAutopilotIssueTerminalEventsProjectRun(t *testing.T) {
	for _, tc := range []struct {
		issueStatus string
		wantRun     string
	}{
		{issueStatus: "done", wantRun: "completed"},
		{issueStatus: "blocked", wantRun: "failed"},
	} {
		t.Run(tc.issueStatus, func(t *testing.T) {
			f := dispatchCreateIssueAutopilot(t, "Issue terminal "+tc.issueStatus)
			if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = $2 WHERE id = $1`, f.run.IssueID, tc.issueStatus); err != nil {
				t.Fatalf("set issue terminal status: %v", err)
			}
			event := events.Event{
				Type:        "issue:updated",
				WorkspaceID: testWorkspaceID,
				ActorType:   "member",
				ActorID:     testUserID,
				Payload: issueEventPayload{
					Issue: eventIssue{
						ID:          util.UUIDToString(f.run.IssueID),
						WorkspaceID: testWorkspaceID,
						Status:      tc.issueStatus,
					},
					StatusChanged: true,
				},
			}
			tx, err := testPool.Begin(context.Background())
			if err != nil {
				t.Fatalf("begin issue projection: %v", err)
			}
			defer tx.Rollback(context.Background())
			emitted, err := consumeAutopilotRunProjection(context.Background(), f.queries.WithTx(tx), event)
			if err != nil {
				t.Fatalf("project terminal issue: %v", err)
			}
			if len(emitted) != 1 {
				t.Fatalf("terminal issue emitted %d events, want 1", len(emitted))
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatalf("commit issue projection: %v", err)
			}
			updated, err := f.queries.GetAutopilotRun(context.Background(), f.run.ID)
			if err != nil {
				t.Fatalf("load projected run: %v", err)
			}
			if updated.Status != tc.wantRun {
				t.Fatalf("projected run status = %q, want %q", updated.Status, tc.wantRun)
			}
		})
	}
}

func TestAutopilotTaskProjectionReturnsTransientDatabaseFailure(t *testing.T) {
	ctx := context.Background()
	f := setupAutopilotListenerFixture(t)
	ap, err := f.queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Title:              "Durable projection retry",
		Description:        pgtype.Text{String: "failure propagation", Valid: true},
		AssigneeType:       "agent",
		AssigneeID:         parseUUID(f.agentID),
		Status:             "active",
		ExecutionMode:      "run_only",
		IssueTitleTemplate: pgtype.Text{},
		CreatedByType:      "member",
		CreatedByID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID) })
	run, err := f.autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
	if err != nil {
		t.Fatalf("dispatch autopilot: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`, run.TaskID); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}
	if _, err := f.queries.StartAgentTask(ctx, run.TaskID); err != nil {
		t.Fatalf("start task: %v", err)
	}
	if _, err := f.taskSvc.CompleteTask(ctx, run.TaskID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	blocker, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM autopilot_run WHERE id = $1 FOR UPDATE`, run.ID); err != nil {
		blocker.Rollback(ctx)
		t.Fatalf("lock autopilot run: %v", err)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, projectionErr := consumeAutopilotRunProjection(timeoutCtx, f.queries, latestTaskTerminalEvent(t, run.TaskID))
	if projectionErr == nil {
		blocker.Rollback(ctx)
		t.Fatal("locked run update returned nil error; event would be falsely acknowledged")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release blocker: %v", err)
	}
	unchanged, err := f.queries.GetAutopilotRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("load run after failed projection: %v", err)
	}
	if unchanged.Status != "running" {
		t.Fatalf("failed projection left run status %q, want running", unchanged.Status)
	}
	projectTaskTerminalEvent(t, f.queries, run.TaskID)
	updated, err := f.queries.GetAutopilotRun(ctx, run.ID)
	if err != nil || updated.Status != "completed" {
		t.Fatalf("retry projection status = %q, err = %v", updated.Status, err)
	}
}

func TestAutopilotRunOnlyRollsBackTaskWhenRunLinkFails(t *testing.T) {
	ctx := context.Background()
	f := setupAutopilotListenerFixture(t)
	ap, err := f.queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Title:              "Atomic run-only dispatch",
		Description:        pgtype.Text{String: "task and run link must commit together", Valid: true},
		AssigneeType:       "agent",
		AssigneeID:         parseUUID(f.agentID),
		Status:             "active",
		ExecutionMode:      "run_only",
		IssueTitleTemplate: pgtype.Text{},
		CreatedByType:      "member",
		CreatedByID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID) })
	installAutopilotTaskLinkFailure(t, util.UUIDToString(ap.ID))

	run, dispatchErr := f.autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
	if dispatchErr == nil {
		t.Fatal("run-only dispatch returned success after run link failure")
	}
	if run == nil {
		t.Fatal("failed dispatch did not return its audit run")
	}
	var taskCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id = $1`, run.ID).Scan(&taskCount); err != nil {
		t.Fatalf("count orphaned run-only tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("run-link failure left %d executable tasks", taskCount)
	}
	persisted, err := f.queries.GetAutopilotRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("load failed dispatch run: %v", err)
	}
	if persisted.Status != "failed" || !persisted.FailureReason.Valid || !strings.Contains(persisted.FailureReason.String, "link run-only task") {
		t.Fatalf("failed dispatch run = status %q reason %+v", persisted.Status, persisted.FailureReason)
	}
	updatedAutopilot, err := f.queries.GetAutopilot(ctx, ap.ID)
	if err != nil {
		t.Fatalf("load autopilot after failed dispatch: %v", err)
	}
	if !updatedAutopilot.LastRunAt.Valid {
		t.Fatal("failed dispatch did not record last_run_at")
	}
}

func installAutopilotTaskLinkFailure(t *testing.T, autopilotID string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "autopilot_task_link_fail_fn_" + suffix
	triggerName := "autopilot_task_link_fail_" + suffix
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot_run`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced autopilot task link failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE UPDATE ON autopilot_run
		FOR EACH ROW WHEN (OLD.autopilot_id = '%s' AND NEW.task_id IS NOT NULL)
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, autopilotID, functionName)); err != nil {
		t.Fatalf("install autopilot task link failure: %v", err)
	}
}

func TestAutopilotCreateIssueRollsBackIssueWhenTaskInsertFails(t *testing.T) {
	ctx := context.Background()
	f := setupAutopilotListenerFixture(t)
	ap, err := f.queries.CreateAutopilot(ctx, db.CreateAutopilotParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Title:              "Atomic create-issue dispatch",
		Description:        pgtype.Text{String: "issue and task must commit together", Valid: true},
		AssigneeType:       "agent",
		AssigneeID:         parseUUID(f.agentID),
		Status:             "active",
		ExecutionMode:      "create_issue",
		IssueTitleTemplate: pgtype.Text{String: "Atomic create-issue task", Valid: true},
		CreatedByType:      "member",
		CreatedByID:        parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID) })
	installAutopilotIssueTaskFailure(t, util.UUIDToString(ap.ID))

	run, dispatchErr := f.autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
	if dispatchErr == nil {
		t.Fatal("create-issue dispatch returned success after task insert failure")
	}
	if run == nil {
		t.Fatal("failed create-issue dispatch did not return its audit run")
	}
	var issueCount, taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM issue WHERE origin_type = 'autopilot' AND origin_id = $1
	`, ap.ID).Scan(&issueCount); err != nil {
		t.Fatalf("count partial autopilot issues: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE autopilot_run_id = $1 OR issue_id = $2`, run.ID, run.IssueID).Scan(&taskCount); err != nil {
		t.Fatalf("count partial autopilot tasks: %v", err)
	}
	if issueCount != 0 || taskCount != 0 {
		t.Fatalf("failed create-issue dispatch left issue=%d task=%d", issueCount, taskCount)
	}
	persisted, err := f.queries.GetAutopilotRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("load failed create-issue run: %v", err)
	}
	if persisted.Status != "failed" || persisted.IssueID.Valid {
		t.Fatalf("failed create-issue run = status %q issue_id %+v", persisted.Status, persisted.IssueID)
	}
}

func installAutopilotIssueTaskFailure(t *testing.T, autopilotID string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "autopilot_issue_task_fail_fn_" + suffix
	triggerName := "autopilot_issue_task_fail_" + suffix
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_task_queue`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.issue_id IS NOT NULL AND EXISTS (
				SELECT 1 FROM issue
				WHERE id = NEW.issue_id AND origin_type = 'autopilot' AND origin_id = '%s'
			) THEN
				RAISE EXCEPTION 'forced autopilot issue task failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON agent_task_queue
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, autopilotID, triggerName, functionName)); err != nil {
		t.Fatalf("install autopilot issue task failure: %v", err)
	}
}

// TestAutopilotDispatchSkipsWhenRuntimeOffline locks in the MUL-1899
// admission gate: when the assignee agent's runtime is not online we must
// record a `skipped` autopilot_run with a failure_reason and NOT enqueue an
// agent_task_queue row. This is the fix for "活跃 schedule 持续给离线 local
// agent 入队".
func TestAutopilotDispatchSkipsWhenRuntimeOffline(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)
	autopilotSvc := service.NewAutopilotService(queries, testPool, bus, taskSvc)

	// Spin up a dedicated runtime + agent so we can flip the runtime to
	// offline without affecting the shared fixture used by other tests.
	agentID := createOfflineLocalAgent(t, ctx, "Offline runtime", "mul1899_offline_runtime", "mul1899-offline-agent")
	ap := createRunOnlyAutopilotForAgent(t, ctx, queries, "Offline-runtime autopilot", "MUL-1899 admission test", agentID)

	run, err := autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "schedule", nil)
	if err != nil {
		t.Fatalf("DispatchAutopilot: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run, got nil")
	}
	if run.Status != "skipped" {
		t.Fatalf("expected run status 'skipped', got %q", run.Status)
	}
	if !run.FailureReason.Valid || !strings.Contains(run.FailureReason.String, "offline") {
		t.Fatalf("expected failure reason mentioning 'offline', got %+v", run.FailureReason)
	}
	if run.TaskID.Valid {
		t.Fatalf("expected no task to be enqueued, got task_id %v", run.TaskID)
	}
	updatedAutopilot, err := queries.GetAutopilot(ctx, ap.ID)
	if err != nil {
		t.Fatalf("load skipped autopilot: %v", err)
	}
	if !updatedAutopilot.LastRunAt.Valid {
		t.Fatal("skipped dispatch did not record last_run_at")
	}

	// Defensive: confirm at the DB layer that nothing landed on the queue.
	var taskCount int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE agent_id = $1`,
		agentID,
	).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("expected 0 queued tasks for offline-runtime agent, got %d", taskCount)
	}
}

// TestManualTriggerDoesNotErrorOnPostAdmissionSkip locks in PR #2888 review
// fix #2: if the dispatcher decides to skip after the admission gate has
// already passed (e.g. the leader's runtime went offline between admission
// and task creation), DispatchAutopilot must return (run, nil) with
// status='skipped' rather than (nil, err). Without this, manual trigger
// surfaces a 500 to the user even though the work was correctly suppressed
// — the same regression Emacs flagged on the original PR.
//
// We synthesise the race by:
//  1. Creating an online runtime + agent so the admission gate passes.
//  2. Flipping the runtime to offline.
//  3. Triggering the autopilot. Admission has already loaded the agent +
//     runtime once with status='online' at row-fetch time, so the second
//     check inside dispatchRunOnly is what catches the offline state.
//
// In this implementation the admission gate also re-reads the runtime, so
// the same offline state actually fires the admission skip first. That is
// fine for the assertion we care about: the manual trigger must not 500 and
// the run must be `skipped`. The post-admission branch is exercised
// separately by the errDispatchSkipped unwrap unit test in the service
// package.
func TestManualTriggerDoesNotErrorOnPostAdmissionSkip(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)
	autopilotSvc := service.NewAutopilotService(queries, testPool, bus, taskSvc)

	agentID := createOfflineLocalAgent(t, ctx, "Manual-trigger skip runtime", "mul2429_manual_skip_runtime", "mul2429-manual-skip-agent")
	ap := createRunOnlyAutopilotForAgent(t, ctx, queries, "Manual-trigger skip autopilot", "PR #2888 review fix #2", agentID)

	run, err := autopilotSvc.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
	if err != nil {
		t.Fatalf("manual DispatchAutopilot returned error (would 500 the handler): %v", err)
	}
	if run == nil {
		t.Fatal("expected a run, got nil")
	}
	if run.Status != "skipped" {
		t.Fatalf("expected run status 'skipped', got %q", run.Status)
	}
}
