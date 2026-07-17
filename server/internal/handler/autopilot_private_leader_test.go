package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newPrivateLeaderSquadFixture(t *testing.T, squadName string) (leaderID, memberID, squadID string) {
	t.Helper()
	requireHandlerDatabase(t)
	leaderID, _, memberID = personalAgentTestFixture(t)
	squadID = createHandlerTestSquad(t, squadName, leaderID)
	return leaderID, memberID, squadID
}

func createPrivateLeaderAutopilot(t *testing.T, title, squadID string) AutopilotResponse {
	t.Helper()
	return createAutopilotFixture(t, map[string]any{
		"title": title, "assignee_type": "squad", "assignee_id": squadID, "execution_mode": "create_issue",
	})
}

func createLegacyPrivateLeaderAutopilot(t *testing.T, squadID, creatorID, executionMode string) string {
	t.Helper()
	var autopilotID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO autopilot (workspace_id, title, assignee_type, assignee_id,
		                       execution_mode, created_by_type, created_by_id, status)
		VALUES ($1, 'private leader dispatch fixture', 'squad', $2, $3, 'member', $4, 'active')
		RETURNING id
	`, testWorkspaceID, squadID, executionMode, creatorID).Scan(&autopilotID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, autopilotID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, autopilotID)
	})
	return autopilotID
}

func triggerPrivateLeaderAutopilot(t *testing.T, autopilotID, idempotencyKey string) AutopilotRunResponse {
	t.Helper()
	response := httptest.NewRecorder()
	request := withURLParam(newRequest("POST", "/api/autopilots/"+autopilotID+"/trigger?workspace_id="+testWorkspaceID, nil), "id", autopilotID)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	testHandler.TriggerAutopilot(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("TriggerAutopilot: expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var run AutopilotRunResponse
	if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	return run
}

func TestCreateAutopilot_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "AP Private Leader Create")

	w := httptest.NewRecorder()
	r := newAutopilotCreateRequestAs(memberID, "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "should be blocked",
		"assignee_type":  "squad",
		"assignee_id":    squadID,
		"execution_mode": "create_issue",
	})
	testHandler.CreateAutopilot(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateAutopilot_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "AP Private Leader Update")
	publicAgentID := createHandlerTestAgent(t, "ap-personal-leader-public", nil)
	ap := createAutopilotFixture(t, map[string]any{
		"title":          "update target ap",
		"assignee_id":    publicAgentID,
		"execution_mode": "create_issue",
	})

	squadType := "squad"
	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "PATCH", "/api/autopilots/"+ap.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"assignee_type": squadType,
		"assignee_id":   squadID,
	})
	r = withURLParam(r, "id", ap.ID)
	testHandler.UpdateAutopilot(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAutopilot_SquadPrivateLeader_OwnerAllowed(t *testing.T) {
	_, _, squadID := newPrivateLeaderSquadFixture(t, "AP Private Leader Owner")
	createPrivateLeaderAutopilot(t, "owner creates personal-leader squad ap", squadID)
}

func TestTriggerAutopilot_SquadPrivateLeader_OwnerCanDispatch(t *testing.T) {
	ctx := context.Background()
	agentID, _, squadID := newPrivateLeaderSquadFixture(t, "AP Private Leader Dispatch")
	ap := createPrivateLeaderAutopilot(t, "dispatch test personal leader squad", squadID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, ap.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title LIKE 'dispatch test personal leader squad%')`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'dispatch test personal leader squad%'`, testWorkspaceID)
	})
	run := triggerPrivateLeaderAutopilot(t, ap.ID, "10000000-0000-4000-8000-000000000102")
	if run.Status != "issue_created" {
		t.Fatalf("run status = %q, want issue_created", run.Status)
	}
	if run.IssueID == nil {
		t.Fatal("create_issue run has no issue_id")
	}
	var leaderTaskID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		  AND is_leader_task = true AND force_fresh_session = true
	`, *run.IssueID, agentID).Scan(&leaderTaskID); err != nil {
		t.Fatalf("load atomic squad leader task: %v", err)
	}
	var sopRunCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_run
		WHERE issue_id = $1 AND leader_task_id = $2
	`, *run.IssueID, leaderTaskID).Scan(&sopRunCount); err != nil {
		t.Fatalf("count squad SOP runs: %v", err)
	}
	if sopRunCount != 1 {
		t.Fatalf("atomic squad dispatch created %d SOP runs, want 1", sopRunCount)
	}
}

func TestTriggerAutopilot_SquadPrivateLeader_PlainMemberCreator_Blocked(t *testing.T) {
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "AP Private Leader Blocked Dispatch")
	autopilotID := createLegacyPrivateLeaderAutopilot(t, squadID, memberID, "create_issue")
	run := triggerPrivateLeaderAutopilot(t, autopilotID, "10000000-0000-4000-8000-000000000103")
	if run.Status == "issue_created" || run.Status == "running" {
		t.Fatalf("run status = %q; want skipped/failed since creator is plain member", run.Status)
	}
}

func TestTriggerAutopilot_RunOnly_SquadPrivateLeader_PlainMemberCreator_Blocked(t *testing.T) {
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "AP RunOnly Private Leader Blocked")
	autopilotID := createLegacyPrivateLeaderAutopilot(t, squadID, memberID, "run_only")
	run := triggerPrivateLeaderAutopilot(t, autopilotID, "10000000-0000-4000-8000-000000000104")
	if run.Status == "running" {
		t.Fatalf("run status = %q; want skipped/failed since creator is plain member and leader is private", run.Status)
	}
}
