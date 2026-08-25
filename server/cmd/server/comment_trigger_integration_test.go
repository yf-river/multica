package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
)

// authRequestWithAgent models auth middleware resolving a task token for the
// agent. The task is best-effort cleaned up via test teardown elsewhere.
func authRequestWithAgent(t *testing.T, method, path string, body any, agentID string) *http.Response {
	t.Helper()
	taskID := ensureAgentTask(t, agentID)
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatalf("generate task token: %v", err)
	}
	tokenHash := auth.HashToken(token)
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '1 hour')
	`, tokenHash, taskID, agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("persist task token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE token_hash = $1`, tokenHash)
	})
	return authRequestWithHeaders(t, method, path, body, map[string]string{
		"Authorization": "Bearer " + token,
	})
}

func ensureAgentTask(t *testing.T, agentID string) string {
	t.Helper()
	ctx := context.Background()
	var taskID string
	if err := testPool.QueryRow(ctx,
		`SELECT id::text FROM agent_task_queue WHERE agent_id = $1 LIMIT 1`,
		agentID,
	).Scan(&taskID); err == nil && taskID != "" {
		return taskID
	}
	var runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id::text FROM agent WHERE id = $1`,
		agentID,
	).Scan(&runtimeID); err != nil {
		t.Fatalf("ensureAgentTask: load runtime_id for agent %s: %v", agentID, err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 0)
		RETURNING id::text
	`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("ensureAgentTask: insert task for agent %s: %v", agentID, err)
	}
	return taskID
}

func countPendingTasks(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status IN ('queued', 'dispatched')`,
		issueID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count pending tasks: %v", err)
	}
	return count
}

func assertPendingTasks(t *testing.T, issueID string, want int) {
	t.Helper()
	if got := countPendingTasks(t, issueID); got != want {
		t.Fatalf("pending tasks = %d, want %d", got, want)
	}
}

func clearTasks(t *testing.T, issueID string) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	if err != nil {
		t.Fatalf("failed to clear tasks: %v", err)
	}
}

func latestTriggerCommentID(t *testing.T, issueID string) string {
	t.Helper()
	var triggerID *string
	err := testPool.QueryRow(context.Background(),
		`SELECT trigger_comment_id::text
		   FROM agent_task_queue
		  WHERE issue_id = $1 AND status IN ('queued', 'dispatched')
		  ORDER BY created_at DESC
		  LIMIT 1`,
		issueID).Scan(&triggerID)
	if err != nil {
		t.Fatalf("failed to fetch trigger_comment_id: %v", err)
	}
	if triggerID == nil {
		return ""
	}
	return *triggerID
}

func getAgentID(t *testing.T) string {
	t.Helper()
	resp := authRequest(t, "GET", "/api/agents?workspace_id="+testWorkspaceID, nil)
	var agents []map[string]any
	readJSON(t, resp, &agents)
	if len(agents) == 0 {
		t.Fatal("no agents in test workspace")
	}
	return agents[0]["id"].(string)
}

func createSecondAgent(t *testing.T) string {
	t.Helper()
	resp := authRequest(t, "GET", "/api/agents?workspace_id="+testWorkspaceID, nil)
	var agents []map[string]any
	readJSON(t, resp, &agents)
	if len(agents) == 0 {
		t.Fatal("no agents in test workspace")
	}
	runtimeID := agents[0]["runtime_id"].(string)

	resp = authRequest(t, "POST", "/api/agents?workspace_id="+testWorkspaceID, map[string]any{
		"name":       "Second Test Agent",
		"runtime_id": runtimeID,
		"scope":      "workspace",
	})
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("CreateAgent: expected 201, got %d: %s", resp.StatusCode, body)
	}
	var agent map[string]any
	readJSON(t, resp, &agent)
	id := agent["id"].(string)
	t.Cleanup(func() {
		authRequest(t, "POST", "/api/agents/"+id+"/archive?workspace_id="+testWorkspaceID, nil)
	})
	return id
}

func createIssueAssignedToAgent(t *testing.T, title, agentID string) string {
	t.Helper()
	resp := authRequest(t, "PUT", fmt.Sprintf("/api/issues/%s", createIssue(t, title)), map[string]any{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	var issue map[string]any
	readJSON(t, resp, &issue)
	return issue["id"].(string)
}

func createIssue(t *testing.T, title string) string {
	t.Helper()
	resp := authRequest(t, "POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  title,
		"status": "todo",
	})
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("CreateIssue: expected 201, got %d: %s", resp.StatusCode, body)
	}
	var issue map[string]any
	readJSON(t, resp, &issue)
	return issue["id"].(string)
}

func postComment(t *testing.T, issueID, content string, parentID *string) string {
	t.Helper()
	body := map[string]any{
		"content": content,
		"type":    "comment",
	}
	if parentID != nil {
		body["parent_id"] = *parentID
	}
	resp := authRequest(t, "POST", "/api/issues/"+issueID+"/comments", body)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("postComment: expected 201, got %d: %s", resp.StatusCode, b)
	}
	var comment map[string]any
	readJSON(t, resp, &comment)
	return comment["id"].(string)
}

func postCommentAsAgent(t *testing.T, issueID, content, agentID string, parentID *string) string {
	t.Helper()
	body := map[string]any{
		"content": content,
		"type":    "comment",
	}
	if parentID != nil {
		body["parent_id"] = *parentID
	}
	resp := authRequestWithAgent(t, "POST", "/api/issues/"+issueID+"/comments", body, agentID)
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("postCommentAsAgent: expected 201, got %d: %s", resp.StatusCode, b)
	}
	var comment map[string]any
	readJSON(t, resp, &comment)
	return comment["id"].(string)
}

func strPtr(s string) *string { return &s }

func TestCommentTriggerOnComment(t *testing.T) {
	agentID := getAgentID(t)
	issueID := createIssueAssignedToAgent(t, "Comment trigger integration test", agentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("top-level comment without mentions triggers agent", func(t *testing.T) {
		clearTasks(t, issueID)
		postComment(t, issueID, "Please fix this bug", nil)
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("top-level comment mentioning only others suppresses trigger", func(t *testing.T) {
		clearTasks(t, issueID)
		// Mention a fake agent UUID that is not the assignee.
		content := "[@SomeoneElse](mention://agent/00000000-0000-0000-0000-000000000001) what do you think?"
		postComment(t, issueID, content, nil)
		assertPendingTasks(t, issueID, 0)
	})

	t.Run("top-level comment mentioning assignee triggers agent", func(t *testing.T) {
		clearTasks(t, issueID)
		content := fmt.Sprintf("[@Agent](mention://agent/%s) fix this", agentID)
		postComment(t, issueID, content, nil)
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply to agent thread without mentions triggers agent", func(t *testing.T) {
		clearTasks(t, issueID)
		// Agent starts a thread.
		threadID := postCommentAsAgent(t, issueID, "I analyzed the issue.", agentID, nil)
		// Member replies in the agent's thread.
		postComment(t, issueID, "Looks good, please proceed", strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply records new comment id (not thread root) as trigger_comment_id", func(t *testing.T) {
		clearTasks(t, issueID)
		threadID := postCommentAsAgent(t, issueID, "First pass analysis.", agentID, nil)
		replyID := postComment(t, issueID, "Please also check the edge case", strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
		if got := latestTriggerCommentID(t, issueID); got != replyID {
			t.Errorf("trigger_comment_id = %q, want reply id %q (thread root was %q)",
				got, replyID, threadID)
		}
	})

	t.Run("reply to member thread without mentions suppresses trigger", func(t *testing.T) {
		clearTasks(t, issueID)
		// Member starts a thread.
		threadID := postComment(t, issueID, "Hey team, what do you think?", nil)
		// Clear the task that was created by the top-level comment.
		clearTasks(t, issueID)
		// Another member reply (same user in this test, but the key is parent is by member).
		postComment(t, issueID, "I agree with you", strPtr(threadID))
		assertPendingTasks(t, issueID, 0)
	})

	t.Run("reply to member thread after agent replied triggers agent", func(t *testing.T) {
		clearTasks(t, issueID)
		// Member starts a thread (top-level comment).
		threadID := postComment(t, issueID, "Please fix this bug", nil)
		clearTasks(t, issueID)
		// Agent replies in the thread.
		postCommentAsAgent(t, issueID, "Working on it, found the root cause.", agentID, strPtr(threadID))
		// Member follows up in the same thread without @mentioning the agent.
		postComment(t, issueID, "Great, please also check the edge case", strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply to member thread mentioning assignee triggers agent", func(t *testing.T) {
		clearTasks(t, issueID)
		// Member starts a thread.
		threadID := postComment(t, issueID, "Question about this", nil)
		clearTasks(t, issueID)
		// Reply mentioning the assignee agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) can you help with this?", agentID)
		postComment(t, issueID, content, strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply to member thread that @mentioned assignee triggers without re-mention", func(t *testing.T) {
		clearTasks(t, issueID)
		// Member starts a thread that @mentions the assignee agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) can you review this?", agentID)
		threadID := postComment(t, issueID, content, nil)
		// Clear the task created by the top-level mention.
		clearTasks(t, issueID)
		// Reply in the thread WITHOUT re-mentioning the assignee.
		postComment(t, issueID, "Here is more context for you", strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})
}

func TestCommentTriggerAtAllSuppression(t *testing.T) {
	agentID := getAgentID(t)
	issueID := createIssueAssignedToAgent(t, "@all suppression test", agentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("top-level @all comment suppresses on_comment", func(t *testing.T) {
		clearTasks(t, issueID)
		postComment(t, issueID, "[@All](mention://all/all) heads up everyone", nil)
		assertPendingTasks(t, issueID, 0)
	})

	t.Run("@all in agent thread suppresses on_comment", func(t *testing.T) {
		clearTasks(t, issueID)
		threadID := postCommentAsAgent(t, issueID, "Here is my analysis.", agentID, nil)
		postComment(t, issueID, "[@All](mention://all/all) FYI for the team", strPtr(threadID))
		assertPendingTasks(t, issueID, 0)
	})
}

func TestCommentTriggerOnAssignNoStatusGate(t *testing.T) {
	agentID := getAgentID(t)

	issueID := createIssue(t, "On-assign status gate test")
	resp := authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"status": "in_progress",
	})
	_ = resp.Body.Close()

	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	resp = authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("assign agent: expected 200, got %d: %s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	assertPendingTasks(t, issueID, 1)
}

func TestCommentTriggerOnMentionNoStatusGate(t *testing.T) {
	agentID := getAgentID(t)

	issueID := createIssue(t, "On-mention done issue test")
	resp := authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"status": "done",
	})
	_ = resp.Body.Close()

	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	content := fmt.Sprintf("[@Agent](mention://agent/%s) found a problem here", agentID)
	postComment(t, issueID, content, nil)

	assertPendingTasks(t, issueID, 1)
}

func TestCommentTriggerThreadInheritedMention(t *testing.T) {
	agentID := getAgentID(t)

	issueID := createIssue(t, "Thread-inherited mention test")
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("reply in thread inherits parent mention", func(t *testing.T) {
		clearTasks(t, issueID)
		// Top-level comment @mentions the agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) can you review this?", agentID)
		threadID := postComment(t, issueID, content, nil)
		assertPendingTasks(t, issueID, 1)
		// Clear the task so we can test the reply independently.
		clearTasks(t, issueID)
		// Reply in the thread WITHOUT mentioning the agent.
		postComment(t, issueID, "Here is more context for you", strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply does not double-trigger when re-mentioning same agent", func(t *testing.T) {
		clearTasks(t, issueID)
		// Top-level comment @mentions the agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) help", agentID)
		threadID := postComment(t, issueID, content, nil)
		clearTasks(t, issueID)
		// Reply also @mentions the same agent — should still be just 1 task.
		reply := fmt.Sprintf("[@Agent](mention://agent/%s) any update?", agentID)
		postComment(t, issueID, reply, strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply mentioning only a member does not inherit agent mention", func(t *testing.T) {
		clearTasks(t, issueID)
		// Top-level comment @mentions the agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) can you help?", agentID)
		threadID := postComment(t, issueID, content, nil)
		clearTasks(t, issueID)
		// Reply mentions only a member — should NOT inherit parent's agent mention.
		reply := fmt.Sprintf("cc [@Someone](mention://member/%s)", testUserID)
		postComment(t, issueID, reply, strPtr(threadID))
		assertPendingTasks(t, issueID, 0)
	})

	t.Run("reply mentioning a different agent does not inherit parent agent", func(t *testing.T) {
		clearTasks(t, issueID)
		agentB := createSecondAgent(t)
		// Top-level comment @mentions agent A.
		content := fmt.Sprintf("[@AgentA](mention://agent/%s) please review", agentID)
		threadID := postComment(t, issueID, content, nil)
		clearTasks(t, issueID)
		// Reply @mentions agent B — should trigger ONLY agent B, not agent A.
		reply := fmt.Sprintf("[@AgentB](mention://agent/%s) can you also look?", agentB)
		postComment(t, issueID, reply, strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})

	t.Run("reply mentioning same agent and member triggers via explicit mention", func(t *testing.T) {
		clearTasks(t, issueID)
		// Top-level comment @mentions the agent.
		content := fmt.Sprintf("[@Agent](mention://agent/%s) review this", agentID)
		threadID := postComment(t, issueID, content, nil)
		clearTasks(t, issueID)
		// Reply re-mentions the same agent along with a member — triggers via the reply's own mention.
		reply := fmt.Sprintf("[@Agent](mention://agent/%s) and cc [@Someone](mention://member/%s)", agentID, testUserID)
		postComment(t, issueID, reply, strPtr(threadID))
		assertPendingTasks(t, issueID, 1)
	})
}

func TestDeleteCommentCancelsTriggeredTasks(t *testing.T) {
	agentID := getAgentID(t)
	issueID := createIssueAssignedToAgent(t, "Delete-comment cancels task test", agentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("deleting trigger comment cancels its queued task", func(t *testing.T) {
		clearTasks(t, issueID)
		commentID := postComment(t, issueID, "Please fix this bug", nil)
		assertPendingTasks(t, issueID, 1)

		resp := authRequest(t, "DELETE", "/api/comments/"+commentID, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DeleteComment: expected 204, got %d", resp.StatusCode)
		}

		assertPendingTasks(t, issueID, 0)
	})
}

func TestCommentTriggerCoalescing(t *testing.T) {
	agentID := getAgentID(t)
	issueID := createIssueAssignedToAgent(t, "Coalescing test", agentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	postComment(t, issueID, "First comment", nil)
	postComment(t, issueID, "Second comment", nil)

	assertPendingTasks(t, issueID, 1)
}

func TestCommentTriggerMentionAssigneeDoneIssue(t *testing.T) {
	agentID := getAgentID(t)

	issueID := createIssueAssignedToAgent(t, "Mention-assignee-done test", agentID)
	clearTasks(t, issueID) // clear any tasks from assignment
	resp := authRequest(t, "PUT", "/api/issues/"+issueID, map[string]any{
		"status": "done",
	})
	_ = resp.Body.Close()

	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	content := fmt.Sprintf("[@Agent](mention://agent/%s) reopen this please", agentID)
	postComment(t, issueID, content, nil)

	assertPendingTasks(t, issueID, 1)
}
