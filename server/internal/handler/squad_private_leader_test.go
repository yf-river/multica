package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func createPrivateLeaderIssue(t *testing.T, title, squadID string) string {
	t.Helper()
	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, 'squad', $4, $5)
		RETURNING id
	`, testWorkspaceID, testUserID, title, squadID, nextHandlerTestIssueNumber(t)).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

func queuedLeaderTaskCount(t *testing.T, issueID, leaderID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, leaderID,
	).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return count
}

func submitPrivateLeaderComment(t *testing.T, issueID string, request *http.Request) {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.CreateComment(response, withURLParam(request, "id", issueID))
	if response.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", response.Code, response.Body.String())
	}
}

func TestCreateIssue_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "Private Leader Create Test")

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

func TestUpdateIssue_SquadPrivateLeader_PlainMemberBlocked(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	_, memberID, squadID := newPrivateLeaderSquadFixture(t, "Private Leader Update Test")

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

func TestCreateIssue_SquadPrivateLeader_OwnerAllowed(t *testing.T) {
	_, _, squadID := newPrivateLeaderSquadFixture(t, "Private Leader Owner Test")
	created := createIssueThroughHandler(t, map[string]any{
		"title":         "Owner assigns personal-leader squad",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})
}

func TestComment_SquadPrivateLeader_PlainMemberNoEnqueue(t *testing.T) {
	agentID, memberID, squadID := newPrivateLeaderSquadFixture(t, "Private Leader Comment Test")
	issueID := createPrivateLeaderIssue(t, "personal leader comment test", squadID)

	submitPrivateLeaderComment(t, issueID, newRequestAs(memberID, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "any update on this?",
	}))

	count := queuedLeaderTaskCount(t, issueID, agentID)
	if count != 0 {
		t.Fatalf("personal leader got %d queued tasks from plain member comment; want 0", count)
	}
}

func TestChildDone_SquadPrivateLeader_PlainMemberNoEnqueue(t *testing.T) {
	ctx := context.Background()
	agentID, memberID, squadID := newPrivateLeaderSquadFixture(t, "Private Leader ChildDone Test")

	parent := createIssueThroughHandler(t, map[string]any{
		"title":         "parent with personal-leader squad",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE parent_issue_id = $1`, parent.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, parent.ID)
	})

	// Clear any tasks enqueued by the create.
	mustExec(t, ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)

	child := createIssueThroughHandler(t, map[string]any{
		"title":           "child task",
		"parent_issue_id": parent.ID,
		"assignee_type":   "member",
		"assignee_id":     memberID,
		"status":          "in_progress",
	})
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, child.ID)
	})

	// Plain member moves child to done.
	w := httptest.NewRecorder()
	r := newRequestAs(memberID, "PATCH", "/api/issues/"+child.ID, map[string]any{
		"status": "done",
	})
	r = withURLParam(r, "id", child.ID)
	testHandler.UpdateIssue(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue (child done): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	count := queuedLeaderTaskCount(t, parent.ID, agentID)
	if count != 0 {
		t.Fatalf("personal leader got %d queued tasks from plain member child-done; want 0", count)
	}
}

// TestComment_SquadPrivateLeader_SquadMemberAgentAllowed verifies that an
// agent in a personal squad can trigger its leader via an issue comment.
// Unrelated agents cannot use a personal squad merely because they are agents.
func TestComment_SquadPrivateLeader_SquadMemberAgentAllowed(t *testing.T) {
	requireHandlerDatabase(t)
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

	issueID := createPrivateLeaderIssue(t, "personal leader agent actor test", squadID)

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

	r := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "agent reporting in",
	})
	setTaskTokenActor(r, otherAgentID, taskID)
	submitPrivateLeaderComment(t, issueID, r)

	count := queuedLeaderTaskCount(t, issueID, agentID)
	if count == 0 {
		t.Fatalf("personal leader got 0 queued tasks from squad member agent comment; want at least 1")
	}
}
