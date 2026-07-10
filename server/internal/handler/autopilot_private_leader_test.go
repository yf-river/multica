package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateAutopilot_SquadPrivateLeader_PlainMemberBlocked verifies that a
// plain member cannot create an autopilot assigned to a squad whose leader
// is a personal agent.
func TestCreateAutopilot_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP Private Leader Create', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
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

// TestUpdateAutopilot_SquadPrivateLeader_PlainMemberBlocked verifies that a
// plain member cannot update an autopilot to point at a personal-leader squad.
func TestUpdateAutopilot_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	// Create a non-personal agent for the initial autopilot.
	publicAgentID := createHandlerTestAgent(t, "ap-personal-leader-public", nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP Private Leader Update', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create autopilot as workspace owner assigned to the public agent.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "update target ap",
		"assignee_id":    publicAgentID,
		"execution_mode": "create_issue",
	})
	testHandler.CreateAutopilot(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAutopilot: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ap AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&ap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID)
	})

	// Plain member tries to update to the personal-leader squad.
	squadType := "squad"
	w = httptest.NewRecorder()
	r = newRequestAs(memberID, "PATCH", "/api/autopilots/"+ap.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"assignee_type": squadType,
		"assignee_id":   squadID,
	})
	r = withURLParam(r, "id", ap.ID)
	testHandler.UpdateAutopilot(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAutopilot_SquadPrivateLeader_OwnerAllowed verifies that a
// workspace owner CAN create an autopilot assigned to a personal-leader squad.
func TestCreateAutopilot_SquadPrivateLeader_OwnerAllowed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, _ := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP Private Leader Owner', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// testUserID is workspace owner — should succeed.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "owner creates personal-leader squad ap",
		"assignee_type":  "squad",
		"assignee_id":    squadID,
		"execution_mode": "create_issue",
	})
	testHandler.CreateAutopilot(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ap AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&ap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID)
	})
}

// TestTriggerAutopilot_SquadPrivateLeader_OwnerCanDispatch verifies that a
// squad autopilot with personal leader configured by an owner triggers
// correctly at dispatch time.
func TestTriggerAutopilot_SquadPrivateLeader_OwnerCanDispatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, _ := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP Private Leader Dispatch', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create autopilot as owner.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "dispatch test personal leader squad",
		"assignee_type":  "squad",
		"assignee_id":    squadID,
		"execution_mode": "create_issue",
	})
	testHandler.CreateAutopilot(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAutopilot: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ap AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&ap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, ap.ID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title LIKE 'dispatch test personal leader squad%')`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'dispatch test personal leader squad%'`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, ap.ID)
	})

	// Trigger — should succeed since owner created it.
	w = httptest.NewRecorder()
	r = newRequest("POST", "/api/autopilots/"+ap.ID+"/trigger?workspace_id="+testWorkspaceID, nil)
	r = withURLParam(r, "id", ap.ID)
	testHandler.TriggerAutopilot(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("TriggerAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var run AutopilotRunResponse
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
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

// TestTriggerAutopilot_SquadPrivateLeader_PlainMemberCreator_Blocked verifies
// that if an autopilot pointing to a personal-leader squad was somehow saved
// by a plain member (legacy data), dispatch is blocked at runtime.
func TestTriggerAutopilot_SquadPrivateLeader_PlainMemberCreator_Blocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP Private Leader Blocked Dispatch', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Directly insert an autopilot with the plain member as creator
	// (simulating legacy data before the save-time gate).
	var apID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO autopilot (workspace_id, title, assignee_type, assignee_id,
		                       execution_mode, created_by_type, created_by_id, status)
		VALUES ($1, 'legacy illegal ap', 'squad', $2, 'create_issue', 'member', $3, 'active')
		RETURNING id
	`, testWorkspaceID, squadID, memberID).Scan(&apID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, apID)
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, apID)
	})

	// Trigger as workspace owner — the dispatch should fail because the
	// autopilot's creator (plain member) cannot access the personal leader.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/autopilots/"+apID+"/trigger?workspace_id="+testWorkspaceID, nil)
	r = withURLParam(r, "id", apID)
	testHandler.TriggerAutopilot(w, r)
	// Dispatch returns 200 with status=skipped (or failed) — the run is created
	// but the dispatch is blocked by the personal-leader gate.
	if w.Code != http.StatusOK {
		t.Fatalf("TriggerAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var run AutopilotRunResponse
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	// The dispatch-time gate should cause a skipped or failed run.
	if run.Status == "issue_created" || run.Status == "running" {
		t.Fatalf("run status = %q; want skipped/failed since creator is plain member", run.Status)
	}
}

// TestTriggerAutopilot_RunOnly_SquadPrivateLeader_PlainMemberCreator_Blocked
// mirrors the create_issue dispatch test above but exercises the run_only
// dispatch path (dispatchRunOnly), ensuring both dispatch branches gate
// personal-leader access.
func TestTriggerAutopilot_RunOnly_SquadPrivateLeader_PlainMemberCreator_Blocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'AP RunOnly Private Leader Blocked', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Legacy autopilot: run_only mode, plain member creator, personal-leader squad.
	var apID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO autopilot (workspace_id, title, assignee_type, assignee_id,
		                       execution_mode, created_by_type, created_by_id, status)
		VALUES ($1, 'legacy run_only illegal ap', 'squad', $2, 'run_only', 'member', $3, 'active')
		RETURNING id
	`, testWorkspaceID, squadID, memberID).Scan(&apID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, apID)
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, apID)
	})

	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/autopilots/"+apID+"/trigger?workspace_id="+testWorkspaceID, nil)
	r = withURLParam(r, "id", apID)
	testHandler.TriggerAutopilot(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("TriggerAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var run AutopilotRunResponse
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Status == "running" {
		t.Fatalf("run status = %q; want skipped/failed since creator is plain member and leader is private", run.Status)
	}
}
