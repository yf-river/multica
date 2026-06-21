package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateIssueAssignedToSquadEnqueuesLeader verifies that creating an
// issue with assignee_type=squad immediately enqueues a task for the squad
// leader (mirrors the agent-assignee parking-lot rule: skip backlog only).
func TestCreateIssueAssignedToSquadEnqueuesLeader(t *testing.T) {
	ctx := context.Background()

	// Look up the seeded test agent — it has a runtime, so it can lead a squad.
	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	// Create a squad with that agent as leader.
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, '{"profile_key":"user-center-test","steps":[{"key":"clarify","name":"需求澄清","role_key":"captain"}]}'::jsonb)
		RETURNING id
	`, testWorkspaceID, "Trigger Test Squad", leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	defer testPool.Exec(ctx, `DELETE FROM squad WHERE id = $1`, squadID)

	// Create an issue assigned to the squad.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Squad-assigned at creation",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	defer func() {
		cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
		cleanupReq = withURLParam(cleanupReq, "id", created.ID)
		testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
	}()

	// A task for the squad leader should now exist for this issue.
	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, created.ID, leaderID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount == 0 {
		t.Fatalf("expected squad-leader task to be enqueued after squad-assigned create, got 0")
	}

	var runID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM squad_sop_run
		WHERE issue_id = $1 AND squad_id = $2 AND profile_key = 'user-center-test'
	`, created.ID, squadID).Scan(&runID); err != nil {
		t.Fatalf("expected squad SOP run to be created: %v", err)
	}

	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND step_key = 'clarify' AND event_type = '步骤开始'
	`, runID).Scan(&eventCount); err != nil {
		t.Fatalf("count SOP step events: %v", err)
	}
	if eventCount == 0 {
		t.Fatalf("expected initial SOP step event after squad leader task enqueue, got 0")
	}

	listReq := newRequest("GET", "/api/issues/"+created.ID+"/sop-runs?workspace_id="+testWorkspaceID, nil)
	listReq = withURLParam(listReq, "id", created.ID)
	listW := httptest.NewRecorder()
	testHandler.ListIssueSOPRuns(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("ListIssueSOPRuns: expected 200, got %d: %s", listW.Code, listW.Body.String())
	}

	eventReq := newRequest("POST", "/api/sop-runs/"+runID+"/steps/acceptance/events?workspace_id="+testWorkspaceID, map[string]any{
		"event_type":  "测试结果",
		"status":      "进行中",
		"step_name":   "验收",
		"role_key":    "acceptor",
		"evidence":    map[string]any{"测试命令": "go test ./internal/handler", "结果": "通过"},
		"reason":      "补充测试证据",
		"duration_ms": 123,
	})
	eventReq = withURLParam(eventReq, "runId", runID)
	eventReq = withURLParam(eventReq, "stepId", "acceptance")
	eventW := httptest.NewRecorder()
	testHandler.RecordSOPStepEvent(eventW, eventReq)
	if eventW.Code != http.StatusCreated {
		t.Fatalf("RecordSOPStepEvent: expected 201, got %d: %s", eventW.Code, eventW.Body.String())
	}

	summaryReq := newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/observability/summary", nil)
	summaryReq = withURLParam(summaryReq, "id", testWorkspaceID)
	summaryW := httptest.NewRecorder()
	testHandler.GetWorkspaceObservabilitySummary(summaryW, summaryReq)
	if summaryW.Code != http.StatusOK {
		t.Fatalf("GetWorkspaceObservabilitySummary: expected 200, got %d: %s", summaryW.Code, summaryW.Body.String())
	}
}
