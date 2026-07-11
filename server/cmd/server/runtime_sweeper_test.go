package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setupSweeperTestFixture creates an issue and a task in the given status with
// timestamps old enough to trigger the sweeper. Returns (issueID, agentID, taskID).
func setupSweeperTestFixture(t *testing.T, taskStatus string) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	// Find the integration test agent
	var agentID, runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.account = $1
		LIMIT 1
	`, integrationTestAccount).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	// Create an issue assigned to the agent
	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, 'Sweeper test issue', 'todo', 'none', 'member', m.user_id, 'agent', $2
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}

	// Create a task in the desired status with old timestamps
	var taskID string
	switch taskStatus {
	case "running":
		err = testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
			VALUES ($1, $2, $3, 'running', 0, now() - interval '3 hours', now() - interval '3 hours')
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID)
	case "dispatched":
		err = testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at)
			VALUES ($1, $2, $3, 'dispatched', 0, now() - interval '10 minutes')
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID)
	}
	if err != nil {
		t.Fatalf("failed to create test task: %v", err)
	}

	// Set agent status to "working"
	_, err = testPool.Exec(ctx, `UPDATE agent SET status = 'working' WHERE id = $1`, agentID)
	if err != nil {
		t.Fatalf("failed to set agent status: %v", err)
	}

	return issueID, agentID, taskID
}

func cleanupSweeperFixture(t *testing.T, issueID, agentID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	_, _ = testPool.Exec(ctx, `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, issueID)
	_, _ = testPool.Exec(ctx, `UPDATE agent SET status = 'idle' WHERE id = $1`, agentID)
}

func failStaleTasksForTest(t *testing.T, queries *db.Queries, bus *events.Bus, params db.FailStaleTasksParams) []db.AgentTaskQueue {
	t.Helper()
	taskService := service.NewTaskService(queries, testPool, nil, bus)
	failed, err := taskService.FailStaleTasks(context.Background(), params)
	if err != nil {
		t.Fatalf("FailStaleTasks: %v", err)
	}
	taskService.HandleFailedTasks(context.Background(), failed)
	return failed
}

func setupStaleRunningIssueFixture(t *testing.T, issueStatus, title string) (string, string) {
	t.Helper()
	ctx := context.Background()

	var agentID, runtimeID string
	err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.account = $1
		LIMIT 1
	`, integrationTestAccount).Scan(&agentID, &runtimeID)
	if err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	var issueID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
		SELECT $1, $2, $3, 'none', 'member', m.user_id, 'agent', $4
		FROM member m WHERE m.workspace_id = $1 LIMIT 1
		RETURNING id
	`, testWorkspaceID, title, issueStatus, agentID).Scan(&issueID)
	if err != nil {
		t.Fatalf("failed to create test issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, issueID)
	})

	var taskID string
	err = testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 0, now() - interval '3 hours', now() - interval '3 hours')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID)
	if err != nil {
		t.Fatalf("failed to create stale task: %v", err)
	}

	return issueID, taskID
}

func TestRefreshAgentStatusFromTasks(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()
	issueID, agentID, taskID := setupSweeperTestFixture(t, "dispatched")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	queries := db.New(testPool)

	if _, err := testPool.Exec(ctx, `UPDATE agent SET status = 'idle' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("failed to seed idle agent status: %v", err)
	}

	agent, err := queries.RefreshAgentStatusFromTasks(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("RefreshAgentStatusFromTasks with dispatched task failed: %v", err)
	}
	if agent.Status != "working" {
		t.Fatalf("expected dispatched task to refresh agent status to working, got %q", agent.Status)
	}

	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'cancelled', completed_at = now()
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("failed to cancel seeded task: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET status = 'working' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("failed to reseed working agent status: %v", err)
	}

	agent, err = queries.RefreshAgentStatusFromTasks(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("RefreshAgentStatusFromTasks with no active tasks failed: %v", err)
	}
	if agent.Status != "idle" {
		t.Fatalf("expected cancelled-only task set to refresh agent status to idle, got %q", agent.Status)
	}
}

// TestSweepStaleTasksBroadcastsWithWorkspaceID verifies that when the task sweeper
// fails a stale running task, the task:failed event is broadcast with the correct
// WorkspaceID so it reaches frontend WebSocket clients (events without WorkspaceID
// are silently dropped by the WS listener — that was the original bug).
func TestSweepStaleTasksBroadcastsWithWorkspaceID(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	issueID, agentID, taskID := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	queries := db.New(testPool)
	bus := events.New()

	// Capture task:failed events to verify WorkspaceID is set
	var taskEvents []events.Event
	var mu sync.Mutex
	bus.Subscribe("task:failed", func(e events.Event) {
		mu.Lock()
		taskEvents = append(taskEvents, e)
		mu.Unlock()
	})

	// Use very short timeouts to trigger the sweep on our test task
	failedTasks := failStaleTasksForTest(t, queries, bus, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 300.0,
		RunningTimeoutSecs:  1.0, // 1 second — our task is 3 hours old
	})
	if len(failedTasks) == 0 {
		t.Fatal("expected at least 1 stale task to be failed")
	}

	// Verify our task was included
	found := false
	for _, ft := range failedTasks {
		if ft.ID.Bytes == parseUUIDBytes(taskID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected task %s to be in failed tasks list", taskID)
	}

	// Verify the event was published with WorkspaceID (the core of the bug fix)
	mu.Lock()
	defer mu.Unlock()
	var foundEvent bool
	for _, e := range taskEvents {
		payload, _ := e.Payload.(map[string]any)
		if payload["task_id"] == taskID {
			if e.WorkspaceID == "" {
				t.Fatal("task:failed event is missing WorkspaceID — this was the original bug")
			}
			if e.WorkspaceID != testWorkspaceID {
				t.Fatalf("expected WorkspaceID %s, got %s", testWorkspaceID, e.WorkspaceID)
			}
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Fatalf("expected task:failed event for task %s", taskID)
	}

	// Verify DB: task should be failed
	var status string
	err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query task status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("expected task status 'failed', got '%s'", status)
	}
}

// TestSweepStaleTasksReconcileAgentStatus verifies the current retry-aware
// contract: the failed attempt is replaced by a queued retry, while agent
// status returns to idle until a daemon actually dispatches that retry.
func TestSweepStaleTasksReconcileAgentStatus(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	issueID, agentID, _ := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	queries := db.New(testPool)
	bus := events.New()

	// Capture agent:status events
	var agentStatusEvents []events.Event
	var mu sync.Mutex
	bus.Subscribe("agent:status", func(e events.Event) {
		mu.Lock()
		agentStatusEvents = append(agentStatusEvents, e)
		mu.Unlock()
	})

	// Fail stale tasks with short timeout
	failedTasks := failStaleTasksForTest(t, queries, bus, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 300.0,
		RunningTimeoutSecs:  1.0,
	})
	if len(failedTasks) == 0 {
		t.Fatal("expected at least 1 stale task")
	}

	// Queued work is not yet executing, so the agent returns to idle.
	var agentStatus string
	err := testPool.QueryRow(context.Background(), `SELECT status FROM agent WHERE id = $1`, agentID).Scan(&agentStatus)
	if err != nil {
		t.Fatalf("failed to query agent status: %v", err)
	}
	if agentStatus != "idle" {
		t.Fatalf("expected retrying agent status 'idle' before dispatch, got '%s'", agentStatus)
	}

	// Verify agent:status event was published with correct WorkspaceID
	mu.Lock()
	defer mu.Unlock()
	if len(agentStatusEvents) == 0 {
		t.Fatal("expected agent:status event to be published")
	}
	lastEvent := agentStatusEvents[len(agentStatusEvents)-1]
	if lastEvent.WorkspaceID == "" {
		t.Fatal("agent:status event should have WorkspaceID set")
	}
	if lastEvent.WorkspaceID != testWorkspaceID {
		t.Fatalf("expected WorkspaceID %s, got %s", testWorkspaceID, lastEvent.WorkspaceID)
	}
}

// TestSweepDispatchedStaleTask verifies the sweeper handles dispatched tasks
// stuck beyond the dispatch timeout.
func TestSweepDispatchedStaleTask(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	issueID, agentID, taskID := setupSweeperTestFixture(t, "dispatched")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	queries := db.New(testPool)
	bus := events.New()

	// Capture task:failed events
	var taskEvents []events.Event
	var mu sync.Mutex
	bus.Subscribe("task:failed", func(e events.Event) {
		mu.Lock()
		taskEvents = append(taskEvents, e)
		mu.Unlock()
	})

	// Fail stale tasks — dispatch timeout of 1 second (our task is 10 minutes old)
	failedTasks := failStaleTasksForTest(t, queries, bus, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 1.0,
		RunningTimeoutSecs:  9000.0,
	})
	if len(failedTasks) == 0 {
		t.Fatal("expected at least 1 stale dispatched task")
	}

	// Verify DB: task should be failed
	var status string
	err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query task: %v", err)
	}
	if status != "failed" {
		t.Fatalf("expected task status 'failed', got '%s'", status)
	}

	// Verify task:failed event was published WITH WorkspaceID
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, e := range taskEvents {
		payload, _ := e.Payload.(map[string]any)
		if payload["task_id"] == taskID {
			if e.WorkspaceID == "" {
				t.Fatal("task:failed event is missing WorkspaceID — this was the bug")
			}
			if e.WorkspaceID != testWorkspaceID {
				t.Fatalf("expected WorkspaceID %s, got %s", testWorkspaceID, e.WorkspaceID)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected task:failed event for task %s", taskID)
	}

	// The timeout is retryable, but a queued replacement is not yet working.
	var agentStatus string
	err = testPool.QueryRow(context.Background(), `SELECT status FROM agent WHERE id = $1`, agentID).Scan(&agentStatus)
	if err != nil {
		t.Fatalf("failed to query agent: %v", err)
	}
	if agentStatus != "idle" {
		t.Fatalf("expected agent status 'idle' before retry dispatch, got '%s'", agentStatus)
	}
}

func TestOfflineRuntimeTaskSweepRetriesAfterRuntimeAlreadyOffline(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	runtimeID := seedStaleRuntime(t, ctx, "offline-task-recovery-runtime")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET status = 'offline' WHERE id = $1`, runtimeID); err != nil {
		t.Fatalf("mark runtime offline: %v", err)
	}

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, "offline-sweep-"+runtimeID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create offline-runtime agent: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		WITH bumped AS (
			UPDATE workspace SET issue_counter = issue_counter + 1
			WHERE id = $1 RETURNING issue_counter
		)
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id, number)
		VALUES ($1, 'Offline task recovery', 'in_progress', 'none', 'member', $2, 'agent', $3, (SELECT issue_counter FROM bumped))
		RETURNING id
	`, testWorkspaceID, testUserID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("create offline-runtime issue: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', now() - interval '5 minutes', now() - interval '5 minutes')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create offline-runtime task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, issueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})

	bus := events.New()
	var failedEvent events.Event
	bus.Subscribe("task:failed", func(event events.Event) {
		if payload, ok := event.Payload.(map[string]any); ok && payload["task_id"] == taskID {
			failedEvent = event
		}
	})
	taskService := service.NewTaskService(db.New(testPool), testPool, nil, bus)
	sweepOfflineRuntimeTasks(ctx, taskService)

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("load recovered task: %v", err)
	}
	if status != "failed" {
		t.Fatalf("already-offline runtime task status = %q, want failed", status)
	}
	if failedEvent.WorkspaceID != testWorkspaceID {
		t.Fatalf("already-offline task event workspace = %q, want %q", failedEvent.WorkspaceID, testWorkspaceID)
	}
	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'task:failed' AND payload ->> 'task_id' = $1
	`, taskID).Scan(&eventCount); err != nil {
		t.Fatalf("count recovered task events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("already-offline task durable events = %d, want 1", eventCount)
	}
}

// TestSweepRetriesStaleTaskWithoutFlappingIssue exercises the production
// pipeline rather than the removed test-only fallback: timeout-shaped failures
// enqueue a bounded retry first, so the issue stays in_progress while the next
// attempt is queued.
func TestSweepRetriesStaleTaskWithoutFlappingIssue(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()
	// Create an in_progress issue with a stale running task, simulating a
	// daemon crash mid-run.
	issueID, taskID := setupStaleRunningIssueFixture(t, "in_progress", "Stuck in_progress issue")

	queries := db.New(testPool)
	bus := events.New()

	// Fail the stale task (running timeout of 1 second — our task is 3 hours old).
	failedTasks := failStaleTasksForTest(t, queries, bus, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 300.0,
		RunningTimeoutSecs:  1.0,
	})

	// Confirm our task was swept.
	found := false
	for _, ft := range failedTasks {
		if ft.ID.Bytes == parseUUIDBytes(taskID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected task %s to be in failed tasks, got %v", taskID, failedTasks)
	}

	var issueStatus string
	err := testPool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus)
	if err != nil {
		t.Fatalf("failed to query issue status: %v", err)
	}
	if issueStatus != "in_progress" {
		t.Fatalf("expected retrying issue to stay in_progress, got %q", issueStatus)
	}
	var retryCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND parent_task_id = $2 AND status = 'queued'
	`, issueID, taskID).Scan(&retryCount); err != nil {
		t.Fatalf("count stale-task retries: %v", err)
	}
	if retryCount != 1 {
		t.Fatalf("queued retries = %d, want 1", retryCount)
	}
}

// TestSweepDoesNotResetIssueAlreadyInReview verifies that the sweeper only resets
// issues that are truly stuck in in_progress — it must not clobber issues whose
// agents already moved them forward (e.g. to in_review) before the task timed out.
func TestSweepDoesNotResetIssueAlreadyInReview(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()
	// Issue already advanced to in_review by the agent before the task timed out.
	issueID, _ := setupStaleRunningIssueFixture(t, "in_review", "Already in_review issue")

	queries := db.New(testPool)
	bus := events.New()

	failedTasks := failStaleTasksForTest(t, queries, bus, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 300.0,
		RunningTimeoutSecs:  1.0,
	})
	if len(failedTasks) == 0 {
		t.Fatal("expected at least one stale task")
	}

	// Issue should remain in_review — the sweeper must not clobber agent progress.
	var issueStatus string
	err := testPool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus)
	if err != nil {
		t.Fatalf("failed to query issue status: %v", err)
	}
	if issueStatus != "in_review" {
		t.Fatalf("expected issue status 'in_review' to be preserved, got '%s'", issueStatus)
	}
}

// TestExpireStaleQueuedTasks verifies the MUL-1899 queued-TTL sweeper:
// tasks that have been sitting in 'queued' beyond the TTL are transitioned
// to 'failed' with failure_reason='queued_expired', while fresh queued tasks
// are left alone and the per-tick batch limit is respected.
func TestExpireStaleQueuedTasks(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()

	// Find the integration test agent
	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.account = $1
		LIMIT 1
	`, integrationTestAccount).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	// One ancient queued task (should expire) and one fresh queued task (should not).
	// Constraint: idx_one_pending_task_per_issue_agent → use distinct issues.
	mkIssue := func(label string) string {
		var issueID string
		if err := testPool.QueryRow(ctx, `
			WITH bumped AS (
				UPDATE workspace SET issue_counter = issue_counter + 1
				WHERE id = $1 RETURNING issue_counter
			)
			INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id, number)
			SELECT $1, $3, 'todo', 'none', 'member', m.user_id, 'agent', $2, (SELECT issue_counter FROM bumped)
			FROM member m WHERE m.workspace_id = $1 LIMIT 1
			RETURNING id
		`, testWorkspaceID, agentID, label).Scan(&issueID); err != nil {
			t.Fatalf("failed to create %s issue: %v", label, err)
		}
		return issueID
	}
	oldIssueID := mkIssue("Queued TTL test (old)")
	freshIssueID := mkIssue("Queued TTL test (fresh)")
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id IN ($1, $2)`, oldIssueID, freshIssueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id IN ($1, $2)`, oldIssueID, freshIssueID)
		_, _ = testPool.Exec(ctx, `DELETE FROM domain_event_outbox WHERE stream_key IN ('issue:' || $1, 'issue:' || $2)`, oldIssueID, freshIssueID)
	})

	var oldTaskID, freshTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, created_at)
		VALUES ($1, $2, $3, 'queued', 0, now() - interval '5 hours')
		RETURNING id
	`, agentID, runtimeID, oldIssueID).Scan(&oldTaskID); err != nil {
		t.Fatalf("failed to insert old queued task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, created_at)
		VALUES ($1, $2, $3, 'queued', 0, now())
		RETURNING id
	`, agentID, runtimeID, freshIssueID).Scan(&freshTaskID); err != nil {
		t.Fatalf("failed to insert fresh queued task: %v", err)
	}

	queries := db.New(testPool)
	taskService := service.NewTaskService(queries, testPool, nil, events.New())
	failed, err := taskService.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:    3600.0, // 1h TTL — old task is 5h, fresh task is 0s
		MaxPerTick: 100,
	})
	if err != nil {
		t.Fatalf("ExpireStaleQueuedTasks failed: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("expected exactly 1 expired task, got %d", len(failed))
	}
	taskService.HandleFailedTasks(ctx, failed)
	if failed[0].ID.Bytes != parseUUIDBytes(oldTaskID) {
		t.Fatalf("expired the wrong task: got %x", failed[0].ID.Bytes)
	}

	// DB assertions: old → failed/queued_expired, fresh → still queued.
	var oldStatus, oldReason, oldErr string
	if err := testPool.QueryRow(ctx, `
		SELECT status, COALESCE(failure_reason, ''), COALESCE(error, '')
		FROM agent_task_queue WHERE id = $1
	`, oldTaskID).Scan(&oldStatus, &oldReason, &oldErr); err != nil {
		t.Fatalf("failed to read old task: %v", err)
	}
	if oldStatus != "failed" {
		t.Fatalf("old task: expected status=failed, got %q", oldStatus)
	}
	if oldReason != "queued_expired" {
		t.Fatalf("old task: expected failure_reason=queued_expired, got %q", oldReason)
	}
	if !strings.Contains(oldErr, "expired in queue") {
		t.Fatalf("old task: expected error to mention expiry, got %q", oldErr)
	}

	var freshStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT status FROM agent_task_queue WHERE id = $1
	`, freshTaskID).Scan(&freshStatus); err != nil {
		t.Fatalf("failed to read fresh task: %v", err)
	}
	if freshStatus != "queued" {
		t.Fatalf("fresh task: expected status=queued, got %q", freshStatus)
	}
}

// TestExpireStaleQueuedTasksRespectsBatchLimit verifies the per-tick cap so
// that a large historical backlog cannot monopolise a single sweep.
func TestExpireStaleQueuedTasksRespectsBatchLimit(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}

	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		JOIN member m ON m.workspace_id = a.workspace_id
		JOIN "user" u ON u.id = m.user_id
		WHERE u.account = $1
		LIMIT 1
	`, integrationTestAccount).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("failed to find test agent: %v", err)
	}

	// Create 5 issues, each with one stale queued task — necessary because of the
	// idx_one_pending_task_per_issue_agent unique constraint.
	var issueIDs []string
	t.Cleanup(func() {
		for _, id := range issueIDs {
			_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, id)
			mustExec(t, ctx, `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, id)
		}
	})
	for i := 0; i < 5; i++ {
		var issueID string
		if err := testPool.QueryRow(ctx, `
			WITH bumped AS (
				UPDATE workspace SET issue_counter = issue_counter + 1
				WHERE id = $1 RETURNING issue_counter
			)
			INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_type, assignee_id, number)
			SELECT $1, 'Queued TTL batch test', 'todo', 'none', 'member', m.user_id, 'agent', $2, (SELECT issue_counter FROM bumped)
			FROM member m WHERE m.workspace_id = $1 LIMIT 1
			RETURNING id
		`, testWorkspaceID, agentID).Scan(&issueID); err != nil {
			t.Fatalf("failed to create issue %d: %v", i, err)
		}
		issueIDs = append(issueIDs, issueID)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, created_at)
			VALUES ($1, $2, $3, 'queued', 0, now() - interval '5 hours')
		`, agentID, runtimeID, issueID); err != nil {
			t.Fatalf("failed to insert backlog task %d: %v", i, err)
		}
	}

	queries := db.New(testPool)
	taskService := service.NewTaskService(queries, testPool, nil, events.New())
	failed, err := taskService.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:    3600.0,
		MaxPerTick: 2, // cap below the backlog
	})
	if err != nil {
		t.Fatalf("ExpireStaleQueuedTasks failed: %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("expected batch cap of 2, got %d", len(failed))
	}
	taskService.HandleFailedTasks(ctx, failed)

	var remaining int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agent_task_queue
		WHERE issue_id = ANY($1::uuid[]) AND status = 'queued'
	`, issueIDs).Scan(&remaining); err != nil {
		t.Fatalf("failed to count remaining queued: %v", err)
	}
	if remaining != 3 {
		t.Fatalf("expected 3 queued tasks remaining after batched sweep, got %d", remaining)
	}
}

// parseUUIDBytes converts a UUID string to the 16-byte array used by pgtype.UUID.
func parseUUIDBytes(s string) [16]byte {
	s = strings.ReplaceAll(s, "-", "")
	var b [16]byte
	for i := 0; i < 16; i++ {
		hi := unhex(s[i*2])
		lo := unhex(s[i*2+1])
		b[i] = hi<<4 | lo
	}
	return b
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
