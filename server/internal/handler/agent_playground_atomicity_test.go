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
				SELECT id FROM chat_session WHERE title LIKE $1
			)
		`, "Agent 调试场 · "+titlePrefix+"%")
		mustExec(t, context.Background(), `DELETE FROM chat_session WHERE title LIKE $1`, "Agent 调试场 · "+titlePrefix+"%")
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
