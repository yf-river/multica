package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type agentPlaygroundRunFixture struct {
	experimentID string
	agentID      string
	titlePrefix  string
}

func newAgentPlaygroundRunFixture(t *testing.T, inputCount int) agentPlaygroundRunFixture {
	t.Helper()
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "playground-run-agent-"+uuid.NewString(), nil)
	titlePrefix := "atomic playground " + uuid.NewString()
	var experimentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_playground_experiment (workspace_id, name, status, created_by)
		VALUES ($1, $2, 'ready', $3) RETURNING id
	`, testWorkspaceID, titlePrefix, testUserID).Scan(&experimentID); err != nil {
		t.Fatalf("create playground experiment: %v", err)
	}
	var experimentAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_playground_agent (experiment_id, workspace_id, agent_id, display_order)
		VALUES ($1, $2, $3, 0) RETURNING id
	`, experimentID, testWorkspaceID, agentID).Scan(&experimentAgentID); err != nil {
		t.Fatalf("create playground agent: %v", err)
	}
	for i := 0; i < inputCount; i++ {
		mustExec(t, ctx, `
			INSERT INTO agent_playground_input (experiment_id, workspace_id, row_index, name, input)
			VALUES ($1, $2, $3, $4, $5)
		`, experimentID, testWorkspaceID, i, fmt.Sprintf("case-%d", i), fmt.Sprintf("input-%d", i))
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_playground_experiment WHERE id = $1`, experimentID)
		mustExec(t, context.Background(), `
			DELETE FROM agent_task_queue WHERE chat_session_id IN (
				SELECT id FROM chat_session WHERE title LIKE $1 OR title LIKE $2
			)
		`, "Agent 调试场 · "+titlePrefix+"%", "Agent 调试场裁判 · "+titlePrefix+"%")
		mustExec(t, context.Background(), `DELETE FROM chat_session WHERE title LIKE $1 OR title LIKE $2`, "Agent 调试场 · "+titlePrefix+"%", "Agent 调试场裁判 · "+titlePrefix+"%")
	})
	return agentPlaygroundRunFixture{experimentID: experimentID, agentID: agentID, titlePrefix: titlePrefix}
}

func installAgentPlaygroundStartFailure(t *testing.T, experimentID string) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_playground_start_failure_" + suffix
	triggerName := "test_playground_start_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.experiment_id = '%s'::uuid AND NEW.task_id IS NOT NULL THEN
				RAISE EXCEPTION 'forced playground result start failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON agent_playground_result
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, experimentID, triggerName, functionName)); err != nil {
		t.Fatalf("install playground start failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_playground_result`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func runAgentPlaygroundFixture(t *testing.T, fixture agentPlaygroundRunFixture) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/agent-playground/experiments/"+fixture.experimentID+"/run?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", fixture.experimentID)
	testHandler.RunAgentPlaygroundExperiment(w, req)
	return w
}

func syncAgentPlaygroundFixture(t *testing.T, fixture agentPlaygroundRunFixture) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/agent-playground/experiments/"+fixture.experimentID+"/sync?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", fixture.experimentID)
	testHandler.SyncAgentPlaygroundExperiment(w, req)
	return w
}

func installAgentPlaygroundCompletionFailure(t *testing.T, experimentID string) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_playground_completion_failure_" + suffix
	triggerName := "test_playground_completion_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s'::uuid AND NEW.status = 'completed' THEN
				RAISE EXCEPTION 'forced playground completion failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON agent_playground_experiment
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, experimentID, triggerName, functionName)); err != nil {
		t.Fatalf("install playground completion failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_playground_experiment`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func installAgentPlaygroundJudgementStartFailure(t *testing.T, experimentID string) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_playground_judgement_start_failure_" + suffix
	triggerName := "test_playground_judgement_start_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.experiment_id = '%s'::uuid AND NEW.task_id IS NOT NULL THEN
				RAISE EXCEPTION 'forced playground judgement start failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON agent_playground_judgement
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, experimentID, triggerName, functionName)); err != nil {
		t.Fatalf("install playground judgement start failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_playground_judgement`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func completeAgentPlaygroundRun(t *testing.T, fixture agentPlaygroundRunFixture) {
	t.Helper()
	w := runAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run playground: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	mustExec(t, ctx, `
		UPDATE agent_task_queue task
		SET status = 'completed', completed_at = now()
		FROM agent_playground_result result
		WHERE result.experiment_id = $1 AND result.task_id = task.id
	`, fixture.experimentID)
	mustExec(t, ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		SELECT chat_session_id, 'assistant', 'completed playground output'
		FROM agent_playground_result
		WHERE experiment_id = $1
	`, fixture.experimentID)
	w = syncAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusOK {
		t.Fatalf("sync completed playground: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func judgeAgentPlaygroundFixture(t *testing.T, fixture agentPlaygroundRunFixture, judgeAgentID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	var body any
	if judgeAgentID != "" {
		body = SetAgentPlaygroundJudgeRequest{JudgeAgentID: judgeAgentID}
	}
	req := newRequest(http.MethodPost, "/api/agent-playground/experiments/"+fixture.experimentID+"/judge?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "id", fixture.experimentID)
	testHandler.JudgeAgentPlaygroundExperiment(w, req)
	return w
}

func TestRunAgentPlaygroundRollsBackWholeMatrixWhenResultLinkFails(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 2)
	installAgentPlaygroundStartFailure(t, fixture.experimentID)
	w := runAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	var results, sessions, tasks int
	var status string
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_playground_result WHERE experiment_id = $1`, fixture.experimentID).Scan(&results); err != nil {
		t.Fatalf("count playground results: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE title LIKE $1`, "Agent 调试场 · "+fixture.titlePrefix+"%").Scan(&sessions); err != nil {
		t.Fatalf("count playground sessions: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1 AND chat_session_id IS NOT NULL`, fixture.agentID).Scan(&tasks); err != nil {
		t.Fatalf("count playground tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_experiment WHERE id = $1`, fixture.experimentID).Scan(&status); err != nil {
		t.Fatalf("load playground status: %v", err)
	}
	if results != 0 || sessions != 0 || tasks != 0 || status != "ready" {
		t.Fatalf("partial playground run survived rollback: results=%d sessions=%d tasks=%d status=%q", results, sessions, tasks, status)
	}
}

func TestRunAgentPlaygroundCommitsCompleteMatrix(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 2)
	w := runAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	var linkedResults, sessions int
	var status string
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_playground_result
		WHERE experiment_id = $1 AND task_id IS NOT NULL AND chat_session_id IS NOT NULL
	`, fixture.experimentID).Scan(&linkedResults); err != nil {
		t.Fatalf("count linked playground results: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE title LIKE $1`, "Agent 调试场 · "+fixture.titlePrefix+"%").Scan(&sessions); err != nil {
		t.Fatalf("count playground sessions: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_experiment WHERE id = $1`, fixture.experimentID).Scan(&status); err != nil {
		t.Fatalf("load playground status: %v", err)
	}
	if linkedResults != 2 || sessions != 2 || status != "running" {
		t.Fatalf("playground matrix = results:%d sessions:%d status:%q, want 2/2/running", linkedResults, sessions, status)
	}
}

func TestSyncAgentPlaygroundRollsBackResultWhenCompletionFails(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 1)
	w := runAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run playground: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	var resultID, taskID, initialStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT id, task_id, status FROM agent_playground_result WHERE experiment_id = $1
	`, fixture.experimentID).Scan(&resultID, &taskID, &initialStatus); err != nil {
		t.Fatalf("load playground result: %v", err)
	}
	mustExec(t, ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
	installAgentPlaygroundCompletionFailure(t, fixture.experimentID)

	w = syncAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var resultStatus, experimentStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_result WHERE id = $1`, resultID).Scan(&resultStatus); err != nil {
		t.Fatalf("load result status: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_experiment WHERE id = $1`, fixture.experimentID).Scan(&experimentStatus); err != nil {
		t.Fatalf("load experiment status: %v", err)
	}
	if resultStatus != initialStatus || experimentStatus != "running" {
		t.Fatalf("partial sync survived rollback: result=%q (want %q), experiment=%q", resultStatus, initialStatus, experimentStatus)
	}
}

func TestSyncAgentPlaygroundCompletesFailedResultWithoutTask(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 1)
	ctx := context.Background()
	mustExec(t, ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, fixture.agentID)
	w := runAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run playground: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	w = syncAgentPlaygroundFixture(t, fixture)
	if w.Code != http.StatusOK {
		t.Fatalf("sync playground: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resultStatus, experimentStatus string
	var taskCount int
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_result WHERE experiment_id = $1`, fixture.experimentID).Scan(&resultStatus); err != nil {
		t.Fatalf("load result status: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_playground_experiment WHERE id = $1`, fixture.experimentID).Scan(&experimentStatus); err != nil {
		t.Fatalf("load experiment status: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1 AND chat_session_id IS NOT NULL`, fixture.agentID).Scan(&taskCount); err != nil {
		t.Fatalf("count playground tasks: %v", err)
	}
	if resultStatus != "failed" || experimentStatus != "completed" || taskCount != 0 {
		t.Fatalf("failed no-task result not completed: result=%q experiment=%q tasks=%d", resultStatus, experimentStatus, taskCount)
	}
}

func TestJudgeAgentPlaygroundRollsBackWholeMatrixWhenResultLinkFails(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 2)
	completeAgentPlaygroundRun(t, fixture)
	installAgentPlaygroundJudgementStartFailure(t, fixture.experimentID)

	w := judgeAgentPlaygroundFixture(t, fixture, fixture.agentID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	var judgements, sessions, tasks int
	var judgeAgentIsNull bool
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_playground_judgement WHERE experiment_id = $1`, fixture.experimentID).Scan(&judgements); err != nil {
		t.Fatalf("count playground judgements: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE title LIKE $1`, "Agent 调试场裁判 · "+fixture.titlePrefix+"%").Scan(&sessions); err != nil {
		t.Fatalf("count judge sessions: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (
			SELECT id FROM chat_session WHERE title LIKE $1
		)
	`, "Agent 调试场裁判 · "+fixture.titlePrefix+"%").Scan(&tasks); err != nil {
		t.Fatalf("count judge tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT judge_agent_id IS NULL FROM agent_playground_experiment WHERE id = $1`, fixture.experimentID).Scan(&judgeAgentIsNull); err != nil {
		t.Fatalf("load experiment judge: %v", err)
	}
	if judgements != 0 || sessions != 0 || tasks != 0 || !judgeAgentIsNull {
		t.Fatalf("partial judgement survived rollback: judgements=%d sessions=%d tasks=%d judge_is_null=%v", judgements, sessions, tasks, judgeAgentIsNull)
	}
}

func TestJudgeAgentPlaygroundCommitsCompleteMatrix(t *testing.T) {
	fixture := newAgentPlaygroundRunFixture(t, 2)
	completeAgentPlaygroundRun(t, fixture)
	w := judgeAgentPlaygroundFixture(t, fixture, fixture.agentID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	ctx := context.Background()
	var linkedJudgements, sessions int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_playground_judgement
		WHERE experiment_id = $1 AND task_id IS NOT NULL AND chat_session_id IS NOT NULL
	`, fixture.experimentID).Scan(&linkedJudgements); err != nil {
		t.Fatalf("count linked judgements: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE title LIKE $1`, "Agent 调试场裁判 · "+fixture.titlePrefix+"%").Scan(&sessions); err != nil {
		t.Fatalf("count judge sessions: %v", err)
	}
	if linkedJudgements != 2 || sessions != 2 {
		t.Fatalf("playground judgement matrix = judgements:%d sessions:%d, want 2/2", linkedJudgements, sessions)
	}
}
