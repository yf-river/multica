package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestRecordSOPStepEvent_RunUpdateFailureRollsBackEvent(t *testing.T) {
	ctx := context.Background()
	fixture := createStartedSquadSOPRunFixture(t, ctx, startedSquadSOPRunOptions{
		agentDescription: "manual SOP event atomicity",
		profileKey:       "manual-event-atomicity",
		squadName:        "Manual event atomicity " + uuid.NewString(),
		issueTitle:       "Manual event atomicity " + uuid.NewString(),
		issueStatus:      "todo",
		daemonName:       "manual-event-atomicity-daemon",
	})

	var eventsBefore int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND event_type = '步骤完成'
	`, fixture.runID).Scan(&eventsBefore); err != nil {
		t.Fatalf("count events before: %v", err)
	}
	var statusBefore, stepBefore string
	if err := testPool.QueryRow(ctx, `
		SELECT status, current_step_key FROM squad_sop_run WHERE id = $1
	`, fixture.runID).Scan(&statusBefore, &stepBefore); err != nil {
		t.Fatalf("load run before: %v", err)
	}

	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_manual_sop_run_update_" + suffix)
	triggerName := quoteIdentifier("fail_manual_sop_run_update_trigger_" + suffix)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.id = %s::uuid THEN
				RAISE EXCEPTION 'forced manual SOP run update failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON squad_sop_run
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(fixture.runID), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_sop_run`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/sop-runs/"+fixture.runID+"/steps/pm/events?workspace_id="+testWorkspaceID, map[string]any{
		"event_type": "步骤完成",
		"reason":     "forced rollback proof",
	})
	req = withURLParams(req, "runId", fixture.runID, "stepId", "pm")
	testHandler.RecordSOPStepEvent(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("record = %d %s, want 500", w.Code, w.Body.String())
	}

	var eventsAfter int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND event_type = '步骤完成'
	`, fixture.runID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after: %v", err)
	}
	var statusAfter, stepAfter string
	if err := testPool.QueryRow(ctx, `
		SELECT status, current_step_key FROM squad_sop_run WHERE id = $1
	`, fixture.runID).Scan(&statusAfter, &stepAfter); err != nil {
		t.Fatalf("load run after: %v", err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("failed run update left %d new events", eventsAfter-eventsBefore)
	}
	if statusAfter != statusBefore || stepAfter != stepBefore {
		t.Fatalf("run changed from %s/%s to %s/%s", statusBefore, stepBefore, statusAfter, stepAfter)
	}
}
