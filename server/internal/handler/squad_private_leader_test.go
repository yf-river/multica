package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateIssue_SquadPrivateLeader_PlainMemberBlocked verifies that a
// plain member cannot create an issue assigned to a squad whose leader is
// a personal agent.
func TestCreateIssue_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader Create Test', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Should be blocked",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateIssue_SquadPrivateLeader_PlainMemberBlocked verifies that a
// plain member cannot update an issue's assignee to a personal-leader squad.
func TestUpdateIssue_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader Update Test', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create an unassigned issue as workspace owner.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title)
		VALUES ($1, 'member', $2, 'update target')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "PATCH", "/api/issues/"+issueID, map[string]any{
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	r = withURLParam(r, "id", issueID)
	testHandler.UpdateIssue(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateIssue_SquadPrivateLeader_OwnerAllowed verifies that a workspace
// owner CAN assign an issue to a squad with a personal leader.
func TestCreateIssue_SquadPrivateLeader_OwnerAllowed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, _ := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader Owner Test', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// testUserID is workspace owner — should succeed.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Owner assigns personal-leader squad",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})
}

// TestComment_SquadPrivateLeader_PlainMemberNoEnqueue verifies that a plain
// member posting a comment on an issue assigned to a personal-leader squad
// does NOT trigger the leader.
func TestComment_SquadPrivateLeader_PlainMemberNoEnqueue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader Comment Test', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create issue assigned to the squad as workspace owner.
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id)
		VALUES ($1, 'member', $2, 'personal leader comment test', 'squad', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	// Plain member posts a plain comment (not a @mention).
	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "any update on this?",
	})
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The personal leader must NOT have a queued task.
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("personal leader got %d queued tasks from plain member comment; want 0", count)
	}
}

// TestChildDone_SquadPrivateLeader_PlainMemberNoEnqueue verifies that when
// a plain member completes a child issue whose parent is assigned to a
// personal-leader squad, the leader is NOT enqueued.
func TestChildDone_SquadPrivateLeader_PlainMemberNoEnqueue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, _, memberID := personalAgentTestFixture(t)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Private Leader ChildDone Test', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	// Create parent issue assigned to the squad (as workspace owner).
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "parent with personal-leader squad",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create parent: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var parent IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&parent); err != nil {
		t.Fatalf("decode parent: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE parent_issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, parent.ID)
	})

	// Clear any tasks enqueued by the create.
	mustExec(t, ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)

	// Create a child issue via API (as workspace owner, with member assignee).
	w = httptest.NewRecorder()
	r = newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "child task",
		"parent_issue_id": parent.ID,
		"assignee_type":   "member",
		"assignee_id":     memberID,
		"status":          "in_progress",
	})
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create child: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var child IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&child); err != nil {
		t.Fatalf("decode child: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, child.ID)
	})

	// Plain member moves child to done.
	w = httptest.NewRecorder()
	r = newRequestAs(memberID, "PATCH", "/api/issues/"+child.ID, map[string]any{
		"status": "done",
	})
	r = withURLParam(r, "id", child.ID)
	testHandler.UpdateIssue(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue (child done): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The personal leader must NOT have a queued task on the parent.
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		parent.ID, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("personal leader got %d queued tasks from plain member child-done; want 0", count)
	}
}

// TestComment_SquadPrivateLeader_SquadMemberAgentAllowed verifies that an
// agent in a personal squad can trigger its leader via an issue comment.
// Unrelated agents cannot use a personal squad merely because they are agents.
func TestComment_SquadPrivateLeader_SquadMemberAgentAllowed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID, leaderOwnerID, _ := personalAgentTestFixture(t)
	otherAgentID := createHandlerTestAgent(t, "squad-personal-leader-agent-actor", nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, scope)
		VALUES ($1, 'Private Leader Agent Actor Test', '', $2, $3, 'personal')
		RETURNING id
	`, testWorkspaceID, agentID, leaderOwnerID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO squad_member (squad_id, member_type, member_id, role)
		VALUES ($1, 'agent', $2, 'collaborator')
	`, squadID, otherAgentID); err != nil {
		t.Fatalf("add agent actor to personal squad: %v", err)
	}

	// Create issue assigned to the squad.
	var issueID string
	issueNumber := nextHandlerTestIssueNumber(t)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, 'personal leader agent actor test', 'squad', $3, $4)
		RETURNING id
	`, testWorkspaceID, testUserID, squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	// Use a completed task to authenticate the agent without classifying this
	// comment as an active worker-stage update. Active worker comments are
	// intentionally suppressed because task completion wakes the leader once.
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, completed_at)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'completed', 0, $2, now())
		RETURNING id
	`, otherAgentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create agent task: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	// Agent posts a comment.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "agent reporting in",
	})
	r.Header.Set("X-Agent-ID", otherAgentID)
	r.Header.Set("X-Task-ID", taskID)
	r.Header.Set("X-Actor-Source", "task_token")
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The personal leader should receive a task from a fellow squad agent.
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count == 0 {
		t.Fatalf("personal leader got 0 queued tasks from squad member agent comment; want at least 1")
	}
}
