package handler

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestPromptEvaluationCompletionWaitsForDurableProjection(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, run, _ := createPromptEvaluationAgentRunFixture(t, "持久化终态投影实验", "完成态投影")
	markPromptEvaluationTaskRunning(t, run.TaskID)

	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		parseUUID(run.TaskID),
		[]byte(`{"output":"completed without structured verdict"}`),
		"",
		"",
	); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM prompt_evaluation_run WHERE id = $1
	`, run.Run.ID).Scan(&status); err != nil {
		t.Fatalf("load prompt evaluation run: %v", err)
	}
	if status != "已入队" {
		t.Fatalf("CompleteTask updated prompt evaluation run before durable projection: %q", status)
	}
	removeFailure := installPromptEvaluationTrialFailure(t, run.Run.ID)
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), run.TaskID); err == nil {
		t.Fatal("prompt evaluation projection succeeded despite forced trial update failure")
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM prompt_evaluation_run WHERE id = $1
	`, run.Run.ID).Scan(&status); err != nil {
		t.Fatalf("reload prompt evaluation run after rollback: %v", err)
	}
	if status != "已入队" {
		t.Fatalf("failed projection left run status %q, want 已入队", status)
	}

	removeFailure()
	projected, err := projectPromptEvaluationTerminalTask(context.Background(), run.TaskID)
	if err != nil {
		t.Fatalf("retry prompt evaluation projection: %v", err)
	}
	if !projected {
		t.Fatal("prompt evaluation task was not recognized by projection")
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM prompt_evaluation_run WHERE id = $1
	`, run.Run.ID).Scan(&status); err != nil {
		t.Fatalf("load projected prompt evaluation run: %v", err)
	}
	if status != "需人工复核" {
		t.Fatalf("projected run status = %q, want 需人工复核", status)
	}
	var mismatchedTrials int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM prompt_evaluation_trial
		WHERE run_id = $1 AND status <> '需人工复核'
	`, run.Run.ID).Scan(&mismatchedTrials); err != nil {
		t.Fatalf("count projected trials: %v", err)
	}
	if mismatchedTrials != 0 {
		t.Fatalf("projection left %d trials outside 需人工复核", mismatchedTrials)
	}
}

func TestPromptEvaluationCancellationWaitsForDurableProjection(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, run, _ := createPromptEvaluationAgentRunFixture(t, "取消终态投影实验", "取消态投影")
	markPromptEvaluationTaskRunning(t, run.TaskID)

	if _, err := testHandler.TaskService.CancelTask(context.Background(), parseUUID(run.TaskID)); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM prompt_evaluation_run WHERE id = $1`, run.Run.ID).Scan(&status); err != nil {
		t.Fatalf("load prompt evaluation run: %v", err)
	}
	if status != "已入队" {
		t.Fatalf("CancelTask updated prompt evaluation run before durable projection: %q", status)
	}
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), run.TaskID); err != nil {
		t.Fatalf("project cancelled evaluation task: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM prompt_evaluation_run WHERE id = $1`, run.Run.ID).Scan(&status); err != nil {
		t.Fatalf("load cancelled prompt evaluation run: %v", err)
	}
	if status != "已取消" {
		t.Fatalf("projected cancelled run status = %q, want 已取消", status)
	}
}

func projectPromptEvaluationTerminalTask(ctx context.Context, taskID string) (bool, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := testHandler.Queries.WithTx(tx)
	task, err := queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		return false, err
	}
	projected, err := service.ProjectPromptEvaluationTerminalTask(ctx, queries, task)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return projected, nil
}

func installPromptEvaluationTrialFailure(t *testing.T, runID string) func() {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "prompt_eval_trial_fail_fn_" + suffix
	triggerName := "prompt_eval_trial_fail_" + suffix
	ctx := context.Background()
	remove := func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_trial`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(remove)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.run_id = '%s' THEN
				RAISE EXCEPTION 'forced prompt evaluation trial failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE UPDATE ON prompt_evaluation_trial
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, runID, triggerName, functionName)); err != nil {
		t.Fatalf("install prompt evaluation trial failure: %v", err)
	}
	return remove
}
