package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func createCommentTriggerPreviewIssue(t *testing.T, title string, assigneeType, assigneeID string) string {
	t.Helper()
	ctx := context.Background()

	var number int
	if err := testPool.QueryRow(ctx, `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter
	`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("next issue number: %v", err)
	}

	var assigneeTypeArg any
	var assigneeIDArg any
	if assigneeType != "" {
		assigneeTypeArg = assigneeType
	}
	if assigneeID != "" {
		assigneeIDArg = assigneeID
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id
	`, testWorkspaceID, testUserID, title, assigneeTypeArg, assigneeIDArg, number).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	return issueID
}

func previewCommentTriggersForTest(t *testing.T, issueID string, body any) commentTriggerPreviewResponse {
	t.Helper()

	w := httptest.NewRecorder()
	r := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments/trigger-preview", body)
	r = withURLParam(r, "id", issueID)
	testHandler.PreviewCommentTriggers(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PreviewCommentTriggers: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp commentTriggerPreviewResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	return resp
}

func postCommentForTriggerPreviewTest(t *testing.T, issueID string, body map[string]any) string {
	t.Helper()

	w := httptest.NewRecorder()
	r := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body)
	r = withURLParam(r, "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode created comment: %v", err)
	}
	return resp.ID
}

func insertMemberRootCommentForTriggerPreviewTest(t *testing.T, issueID, content string) string {
	t.Helper()

	var commentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, $4)
		RETURNING id
	`, testWorkspaceID, issueID, testUserID, content).Scan(&commentID); err != nil {
		t.Fatalf("insert member root comment: %v", err)
	}
	return commentID
}

func updateCommentForTriggerPreviewTest(t *testing.T, commentID string, body map[string]any) {
	t.Helper()

	w := httptest.NewRecorder()
	r := newRequest(http.MethodPut, "/api/comments/"+commentID, body)
	r = withURLParam(r, "commentId", commentID)
	testHandler.UpdateComment(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateComment: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func countQueuedCommentTriggerTasks(t *testing.T, issueID, agentID string) int {
	t.Helper()

	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'
	`, issueID, agentID).Scan(&n); err != nil {
		t.Fatalf("count queued tasks: %v", err)
	}
	return n
}

func createCommentTriggerPreviewSquad(t *testing.T, name, leaderID string) string {
	t.Helper()

	var squadID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, name, leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})
	return squadID
}

func requirePreviewAgents(t *testing.T, preview commentTriggerPreviewResponse, wantIDs ...string) {
	t.Helper()
	if len(preview.Agents) != len(wantIDs) {
		t.Fatalf("preview agents = %+v, want ids %v", preview.Agents, wantIDs)
	}
	got := make(map[string]struct{}, len(preview.Agents))
	for _, agent := range preview.Agents {
		got[agent.ID] = struct{}{}
	}
	for _, want := range wantIDs {
		if _, ok := got[want]; !ok {
			t.Fatalf("preview agents = %+v, missing id %s", preview.Agents, want)
		}
	}
}

func requirePreviewAgent(t *testing.T, preview commentTriggerPreviewResponse, wantID string, wantSource commentAgentTriggerSource) {
	t.Helper()
	requirePreviewAgents(t, preview, wantID)
	if preview.Agents[0].Source != string(wantSource) {
		t.Fatalf("preview source = %q, want %q", preview.Agents[0].Source, wantSource)
	}
}

func TestPreviewCommentTriggers_MatchesCreateForInheritedParentMention(t *testing.T) {
	requireHandlerDatabase(t)

	waltID := createHandlerTestAgent(t, "Preview Inherit Walt", nil)
	kimID := createHandlerTestAgent(t, "Preview Inherit Kim", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger preview inherits parent mention", "agent", waltID)

	topLevelPreview := previewCommentTriggersForTest(t, issueID, commentTriggerPreviewRequest{
		Content: "hello from the root composer",
	})
	requirePreviewAgents(t, topLevelPreview, waltID)

	rootContent := fmt.Sprintf("[@Kim](mention://agent/%s) can you inspect this?", kimID)
	rootID := insertMemberRootCommentForTriggerPreviewTest(t, issueID, rootContent)
	if got := countQueuedCommentTriggerTasks(t, issueID, kimID); got != 0 {
		t.Fatalf("fixture queued Kim tasks = %d, want 0", got)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, waltID); got != 0 {
		t.Fatalf("fixture queued Walt tasks = %d, want 0", got)
	}

	replyContent := "plain reply with no mention"
	replyParentID := rootID
	replyBody := map[string]any{
		"content":   replyContent,
		"parent_id": rootID,
	}
	replyPreview := previewCommentTriggersForTest(t, issueID, commentTriggerPreviewRequest{
		Content:  replyContent,
		ParentID: &replyParentID,
	})
	requirePreviewAgent(t, replyPreview, kimID, commentTriggerSourceMentionAgent)

	postCommentForTriggerPreviewTest(t, issueID, replyBody)
	if got := countQueuedCommentTriggerTasks(t, issueID, kimID); got != 1 {
		t.Fatalf("plain reply queued Kim tasks = %d, want 1", got)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, waltID); got != 0 {
		t.Fatalf("plain reply queued Walt tasks = %d, want 0", got)
	}
}

func TestPreviewCommentTriggers_ReturnsMentionedAgentsAndSuppressFiltersCreate(t *testing.T) {
	requireHandlerDatabase(t)

	agentA := createHandlerTestAgent(t, "Preview Mention A", nil)
	agentB := createHandlerTestAgent(t, "Preview Mention B", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger preview mentions", "", "")
	content := fmt.Sprintf("[@A](mention://agent/%s) [@B](mention://agent/%s) please inspect", agentA, agentB)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if got := len(preview.Agents); got != 2 {
		t.Fatalf("expected 2 preview agents, got %d: %+v", got, preview.Agents)
	}
	for _, agent := range preview.Agents {
		if agent.Source != string(commentTriggerSourceMentionAgent) {
			t.Fatalf("preview source = %q, want %q", agent.Source, commentTriggerSourceMentionAgent)
		}
	}

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content":            content,
		"suppress_agent_ids": []string{agentB},
	})

	if got := countQueuedCommentTriggerTasks(t, issueID, agentA); got != 1 {
		t.Fatalf("unsuppressed mentioned agent queued tasks = %d, want 1", got)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentB); got != 0 {
		t.Fatalf("suppressed mentioned agent queued tasks = %d, want 0", got)
	}
}

func TestPreviewCommentTriggers_EditExcludesSameCommentPendingTask(t *testing.T) {
	requireHandlerDatabase(t)
	agentID := createHandlerTestAgent(t, "Edit Preview Exclude Agent", nil)
	tests := []struct {
		name         string
		assigneeType string
		useSquad     bool
		mention      string
		wantSource   commentAgentTriggerSource
	}{
		{name: "agent assignee", assigneeType: "agent", wantSource: commentTriggerSourceIssueAssignee},
		{name: "squad assignee", assigneeType: "squad", useSquad: true, wantSource: commentTriggerSourceIssueAssignee},
		{name: "direct agent mention", mention: "agent", wantSource: commentTriggerSourceMentionAgent},
		{name: "squad mention leader", useSquad: true, mention: "squad", wantSource: commentTriggerSourceMentionSquadLeader},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assigneeID := agentID
			mentionID := agentID
			if test.useSquad {
				mentionID = createCommentTriggerPreviewSquad(t, fmt.Sprintf("Edit Preview Squad %d", index), agentID)
				assigneeID = mentionID
			}
			if test.assigneeType == "" {
				assigneeID = ""
			}
			content := "please continue"
			if test.mention != "" {
				content = fmt.Sprintf("[@Target](mention://%s/%s) inspect this", test.mention, mentionID)
			}
			issueID := createCommentTriggerPreviewIssue(t, "edit preview "+test.name, test.assigneeType, assigneeID)
			commentID := postCommentForTriggerPreviewTest(t, issueID, map[string]any{"content": content})
			if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
				t.Fatalf("queued tasks before edit preview = %d, want 1", got)
			}
			preview := previewCommentTriggersForTest(t, issueID, map[string]any{
				"content": content + " again", "editing_comment_id": commentID,
			})
			requirePreviewAgent(t, preview, agentID, test.wantSource)
		})
	}
}

func TestPreviewCommentTriggers_EditExclusionDoesNotIgnoreOtherCommentPendingTask(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "Edit Preview Other Pending Agent", nil)
	issueID := createCommentTriggerPreviewIssue(t, "edit preview other pending", "", "")
	content := fmt.Sprintf("[@Agent](mention://agent/%s) inspect this", agentID)
	_ = postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content": content,
	})
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks before edit preview = %d, want 1", got)
	}
	editingCommentID := postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content": "plain follow-up with no mention",
	})

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content":            content,
		"editing_comment_id": editingCommentID,
	})
	requirePreviewAgents(t, preview)
}

func TestUpdateComment_SuppressAgentIDsFiltersEditRetrigger(t *testing.T) {
	requireHandlerDatabase(t)

	agentA := createHandlerTestAgent(t, "Edit Suppress A", nil)
	agentB := createHandlerTestAgent(t, "Edit Suppress B", nil)
	issueID := createCommentTriggerPreviewIssue(t, "edit suppress agent ids", "", "")
	commentID := postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content": "plain comment",
	})
	content := fmt.Sprintf("[@A](mention://agent/%s) [@B](mention://agent/%s) inspect this", agentA, agentB)

	updateCommentForTriggerPreviewTest(t, commentID, map[string]any{
		"content":            content,
		"attachment_ids":     []string{},
		"suppress_agent_ids": []string{agentB},
	})

	if got := countQueuedCommentTriggerTasks(t, issueID, agentA); got != 1 {
		t.Fatalf("unsuppressed agent queued tasks = %d, want 1", got)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentB); got != 0 {
		t.Fatalf("suppressed agent queued tasks = %d, want 0", got)
	}
}

func TestCreateComment_SuppressUnknownAgentIDIsNoop(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "Suppress Noop Agent", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger suppress noop", "", "")
	content := fmt.Sprintf("[@Agent](mention://agent/%s) please inspect", agentID)

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content": content,
		"suppress_agent_ids": []string{
			"00000000-0000-0000-0000-000000000001",
		},
	})

	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("mentioned agent queued tasks = %d, want 1", got)
	}
}

func TestPreviewCommentTriggers_NoteReturnsNoAgents(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "Preview Note Agent", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger note", "agent", agentID)
	content := fmt.Sprintf("/note [@Agent](mention://agent/%s) human-only context", agentID)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if got := len(preview.Agents); got != 0 {
		t.Fatalf("note preview agents = %d, want 0: %+v", got, preview.Agents)
	}
}

func TestCreateComment_NoteMentionDoesNotQueueAgent(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "Create Note Agent", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger create note", "agent", agentID)
	content := fmt.Sprintf("/note [@Agent](mention://agent/%s) human-only context", agentID)

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{"content": content})

	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 0 {
		t.Fatalf("note create queued tasks = %d, want 0", got)
	}
}

func TestPreviewCommentTriggers_AssigneeAndSuppress(t *testing.T) {
	requireHandlerDatabase(t)

	agentID := createHandlerTestAgent(t, "Preview Assignee", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger assignee", "agent", agentID)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": "can you continue here?"})
	requirePreviewAgent(t, preview, agentID, commentTriggerSourceIssueAssignee)

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content":            "can you continue here?",
		"suppress_agent_ids": []string{agentID},
	})
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 0 {
		t.Fatalf("suppressed assignee queued tasks = %d, want 0", got)
	}
}

func TestPreviewCommentTriggers_AllSuppressesAssigneeAndPendingDedupes(t *testing.T) {
	requireHandlerDatabase(t)

	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Preview Dedup Assignee", nil)
	issueID := createCommentTriggerPreviewIssue(t, "comment trigger all pending", "agent", agentID)

	allPreview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content": "FYI [@all](mention://all/all)",
	})
	if got := len(allPreview.Agents); got != 0 {
		t.Fatalf("@all preview agents = %d, want 0: %+v", got, allPreview.Agents)
	}

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status)
		VALUES ($1, $2, $3, 'queued')
	`, agentID, handlerTestRuntimeID(t), issueID); err != nil {
		t.Fatalf("seed queued task: %v", err)
	}

	pendingPreview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content": "can you continue here?",
	})
	if got := len(pendingPreview.Agents); got != 0 {
		t.Fatalf("pending preview agents = %d, want 0: %+v", got, pendingPreview.Agents)
	}
}

func TestPreviewCommentTriggers_AssignedSquadLeaderAndSuppress(t *testing.T) {
	requireHandlerDatabase(t)

	leaderID := createHandlerTestAgent(t, "Preview Squad Leader", nil)
	squadID := createCommentTriggerPreviewSquad(t, "Preview Trigger Squad", leaderID)

	issueID := createCommentTriggerPreviewIssue(t, "comment trigger squad assignee", "squad", squadID)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": "please coordinate this"})
	requirePreviewAgent(t, preview, leaderID, commentTriggerSourceIssueAssignee)

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content":            "please coordinate this",
		"suppress_agent_ids": []string{leaderID},
	})
	if got := countQueuedCommentTriggerTasks(t, issueID, leaderID); got != 0 {
		t.Fatalf("suppressed squad leader queued tasks = %d, want 0", got)
	}
}

func TestPreviewCommentTriggers_MentionedSquadLeaderAndSuppress(t *testing.T) {
	requireHandlerDatabase(t)

	leaderID := createHandlerTestAgent(t, "Preview Mentioned Squad Leader", nil)
	squadID := createCommentTriggerPreviewSquad(t, "Preview Mentioned Trigger Squad", leaderID)

	issueID := createCommentTriggerPreviewIssue(t, "comment trigger mentioned squad", "", "")
	content := fmt.Sprintf("[@Squad](mention://squad/%s) please take this", squadID)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	requirePreviewAgent(t, preview, leaderID, commentTriggerSourceMentionSquadLeader)

	postCommentForTriggerPreviewTest(t, issueID, map[string]any{
		"content":            content,
		"suppress_agent_ids": []string{leaderID},
	})
	if got := countQueuedCommentTriggerTasks(t, issueID, leaderID); got != 0 {
		t.Fatalf("suppressed mentioned squad leader queued tasks = %d, want 0", got)
	}
}
