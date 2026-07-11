package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestStartTask_SquadSOPEventFailureRollsBackTaskAndIssue(t *testing.T) {
	ctx := context.Background()
	fixture := createStartedSquadSOPRunFixture(t, ctx, startedSquadSOPRunOptions{
		agentDescription: "SOP start rollback leader",
		profileKey:       "start-atomicity",
		squadName:        "SOP start rollback " + uuid.NewString(),
		issueTitle:       "SOP start rollback " + uuid.NewString(),
		issueStatus:      "todo",
		daemonName:       "sop-start-rollback-daemon",
		skipStart:        true,
	})

	var workerID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id, instructions
		) VALUES ($1, '05-verify', 'SOP start rollback worker', 'local', '{}'::jsonb,
			$2, 'personal', 1, $3, '')
		RETURNING id
	`, testWorkspaceID, testRuntimeID, testUserID).Scan(&workerID); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, workerID) })

	var workerTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, is_leader_task
		) VALUES ($1, $2, $3, 'dispatched', 0, false)
		RETURNING id
	`, workerID, testRuntimeID, fixture.issueID).Scan(&workerTaskID); err != nil {
		t.Fatalf("create worker task: %v", err)
	}

	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_squad_sop_start_event_" + suffix)
	triggerName := quoteIdentifier("fail_squad_sop_start_event_trigger_" + suffix)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.task_id = %s::uuid AND NEW.event_type = '步骤开始' THEN
				RAISE EXCEPTION 'forced Squad SOP start event failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON squad_sop_step_event
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(workerTaskID), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_sop_step_event`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+workerTaskID+"/start", nil, testWorkspaceID, "sop-start-rollback-daemon")
	req = withURLParam(req, "taskId", workerTaskID)
	testHandler.StartTask(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("StartTask = %d %s, want 500", w.Code, w.Body.String())
	}

	var taskStatus, issueStatus, runStatus, currentStep string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, workerTaskID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, fixture.issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status, current_step_key FROM squad_sop_run WHERE id = $1`, fixture.runID).Scan(&runStatus, &currentStep); err != nil {
		t.Fatalf("load run: %v", err)
	}
	var startEvents int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE task_id = $1 AND event_type = '步骤开始'
	`, workerTaskID).Scan(&startEvents); err != nil {
		t.Fatalf("count start events: %v", err)
	}
	if taskStatus != "dispatched" || issueStatus != "todo" || startEvents != 0 {
		t.Fatalf("partial start: task=%s issue=%s events=%d", taskStatus, issueStatus, startEvents)
	}
	if runStatus != "进行中" || currentStep != "pm" {
		t.Fatalf("run changed to %s/%s, want 进行中/pm", runStatus, currentStep)
	}
}
