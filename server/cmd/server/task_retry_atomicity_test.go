package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFailTaskRollsBackWhenRetryCreationFails(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET max_attempts = 2 WHERE id = $1
	`, fixture.task.ID); err != nil {
		t.Fatalf("increase retry budget: %v", err)
	}
	installRetryInsertFailure(t, fixture.task.ID)

	if _, err := fixture.taskService.FailTask(ctx, fixture.task.ID, "task timed out", "", "", "timeout"); err == nil {
		t.Fatal("FailTask returned success despite retry insert failure")
	}
	persisted, err := fixture.queries.GetAgentTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if persisted.Status != "running" {
		t.Fatalf("retry insert failure left parent status %q, want running", persisted.Status)
	}
	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'task:failed' AND payload->>'task_id' = $1
	`, util.UUIDToString(fixture.task.ID)).Scan(&eventCount); err != nil {
		t.Fatalf("count failed task events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("retry insert failure left %d terminal events", eventCount)
	}
}

func TestFailTaskMaterializesOneRetryWithTerminalEvent(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET max_attempts = 2 WHERE id = $1`, fixture.task.ID); err != nil {
		t.Fatalf("increase retry budget: %v", err)
	}
	failed, err := fixture.taskService.FailTask(ctx, fixture.task.ID, "task timed out", "", "", "timeout")
	if err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	firstChild, err := fixture.queries.GetRetryTaskForParent(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetRetryTaskForParent: %v", err)
	}
	secondChild, err := fixture.taskService.MaybeRetryFailedTask(ctx, *failed)
	if err != nil {
		t.Fatalf("MaybeRetryFailedTask: %v", err)
	}
	if secondChild == nil || util.UUIDToString(secondChild.ID) != util.UUIDToString(firstChild.ID) {
		t.Fatalf("retry decision changed: first=%s second=%v", util.UUIDToString(firstChild.ID), secondChild)
	}
	var retryCount, eventCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, fixture.task.ID).Scan(&retryCount); err != nil {
		t.Fatalf("count retry tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'task:failed' AND payload->>'task_id' = $1
	`, util.UUIDToString(fixture.task.ID)).Scan(&eventCount); err != nil {
		t.Fatalf("count terminal events: %v", err)
	}
	if retryCount != 1 || eventCount != 1 {
		t.Fatalf("atomic failure state = retries %d events %d, want 1/1", retryCount, eventCount)
	}
}

func TestMaybeRetryFailedTaskSerializesConcurrentCallers(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET max_attempts = 2 WHERE id = $1`, fixture.task.ID); err != nil {
		t.Fatalf("increase retry budget: %v", err)
	}
	failed, err := fixture.queries.FailAgentTask(ctx, db.FailAgentTaskParams{
		ID:            fixture.task.ID,
		Error:         pgtype.Text{String: "task timed out", Valid: true},
		FailureReason: pgtype.Text{String: "timeout", Valid: true},
	})
	if err != nil {
		t.Fatalf("FailAgentTask: %v", err)
	}

	type retryResult struct {
		child *db.AgentTaskQueue
		err   error
	}
	results := make(chan retryResult, 2)
	for range 2 {
		go func() {
			child, err := fixture.taskService.MaybeRetryFailedTask(ctx, failed)
			results <- retryResult{child: child, err: err}
		}()
	}
	first := <-results
	second := <-results
	for _, result := range []retryResult{first, second} {
		if result.err != nil {
			t.Fatalf("MaybeRetryFailedTask: %v", result.err)
		}
		if result.child == nil {
			t.Fatal("concurrent retry caller received nil child")
		}
	}
	if util.UUIDToString(first.child.ID) != util.UUIDToString(second.child.ID) {
		t.Fatalf("concurrent retries diverged: %s vs %s", util.UUIDToString(first.child.ID), util.UUIDToString(second.child.ID))
	}
	var retryCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, fixture.task.ID).Scan(&retryCount); err != nil {
		t.Fatalf("count retry tasks: %v", err)
	}
	if retryCount != 1 {
		t.Fatalf("concurrent retry callers created %d children, want 1", retryCount)
	}
}

func TestFailStaleTasksRollsBackBatchWhenRetryCreationFails(t *testing.T) {
	ctx := context.Background()
	fixture := setupChatCompletionFixture(t, ctx)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET max_attempts = 2, started_at = now() - interval '2 hours'
		WHERE id = $1
	`, fixture.task.ID); err != nil {
		t.Fatalf("age retryable task: %v", err)
	}
	installRetryInsertFailure(t, fixture.task.ID)

	if _, err := fixture.taskService.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 24 * 60 * 60,
		RunningTimeoutSecs:  60 * 60,
	}); err == nil {
		t.Fatal("FailStaleTasks returned success despite retry insert failure")
	}
	persisted, err := fixture.queries.GetAgentTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if persisted.Status != "running" {
		t.Fatalf("batch retry failure left task status %q, want running", persisted.Status)
	}
	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'task:failed' AND payload->>'task_id' = $1
	`, util.UUIDToString(fixture.task.ID)).Scan(&eventCount); err != nil {
		t.Fatalf("count batch terminal events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("batch retry failure left %d terminal events", eventCount)
	}
}

func installRetryInsertFailure(t *testing.T, parentTaskID pgtype.UUID) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "retry_insert_fail_fn_" + suffix
	triggerName := "retry_insert_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_task_queue`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.parent_task_id = '%s' THEN
				RAISE EXCEPTION 'forced retry insert failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON agent_task_queue
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, util.UUIDToString(parentTaskID), triggerName, functionName)); err != nil {
		t.Fatalf("install retry insert failure: %v", err)
	}
	return remove
}
