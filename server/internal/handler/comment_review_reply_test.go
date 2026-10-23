package handler

// GONG-63: a reply addressed to the managed reviewer must be handed to the issue's
// current owner instead of being dropped. These tests drive the real HTTP handlers
// against the test database and assert both the wire receipt (review_reply) and the
// actual task rows, because "comment saved" must never be mistaken for "an agent ran".

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/review"
)

// reviewReplyFixture stands up the reviewer's managed identity and one issue owned by
// the given assignee. It returns the issue id and the reviewer agent id.
func reviewReplyFixture(t *testing.T, assigneeType, assigneeID string) (issueID, reviewerID string) {
	t.Helper()
	reviewerID = enableReviewSettings(t, "")
	issueID = insertReviewReplyIssue(t, assigneeType, assigneeID)
	return issueID, reviewerID
}

func insertReviewReplyIssue(t *testing.T, assigneeType, assigneeID string) string {
	t.Helper()
	var number int
	if err := testPool.QueryRow(context.Background(), `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter
	`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("next issue number: %v", err)
	}

	var assigneeTypeArg, assigneeIDArg any
	if assigneeType != "" {
		assigneeTypeArg = assigneeType
	}
	if assigneeID != "" {
		assigneeIDArg = assigneeID
	}

	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number, last_activity_at)
		VALUES ($1, 'member', $2, 'review reply routing', $3, $4, $5, now())
		RETURNING id
	`, testWorkspaceID, testUserID, assigneeTypeArg, assigneeIDArg, number).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

// insertReviewConclusionComment writes the reviewer's conclusion the way the review
// run does: an agent-authored comment with the managed identity.
func insertReviewConclusionComment(t *testing.T, issueID, reviewerID string) string {
	t.Helper()
	var commentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, '审查结论：打回')
		RETURNING id
	`, testWorkspaceID, issueID, reviewerID).Scan(&commentID); err != nil {
		t.Fatalf("insert reviewer comment: %v", err)
	}
	return commentID
}

func insertAgentReplyComment(t *testing.T, issueID, agentID, parentID, content string) string {
	t.Helper()
	var parentArg any
	if parentID != "" {
		parentArg = parentID
	}
	var commentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, parent_id)
		VALUES ($1, $2, 'agent', $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, issueID, agentID, content, parentArg).Scan(&commentID); err != nil {
		t.Fatalf("insert agent reply: %v", err)
	}
	return commentID
}

// postMemberComment posts as the workspace owner and returns the decoded response.
func postMemberComment(t *testing.T, issueID, parentID, content string) CommentResponse {
	t.Helper()
	body := map[string]any{"content": content}
	if parentID != "" {
		body["parent_id"] = parentID
	}
	w := httptest.NewRecorder()
	r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	return resp
}

func countAllTasksForAgent(t *testing.T, issueID, agentID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2
	`, issueID, agentID).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return n
}

// TestReviewReply_RoutesToCurrentOwner covers the reported bug: a reply in the
// reviewer's thread (no @mention, newest message from the developer) reaches the
// owner agent, and the reviewer gains no run. Preview and save must name the same
// recipient.
func TestReviewReply_RoutesToCurrentOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Reply Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)
	// The developer already answered in this thread; the composer still keeps the
	// reviewer's conclusion as the reply's parent.
	insertAgentReplyComment(t, issueID, ownerID, rootID, "修订结论：需要你定两件事")

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content":   "全部按你的建议走。",
		"parent_id": rootID,
	})
	requirePreviewAgents(t, preview, ownerID)
	if preview.Agents[0].Source != string(commentTriggerSourceReviewAssignee) {
		t.Fatalf("preview agent source = %q, want %q", preview.Agents[0].Source, commentTriggerSourceReviewAssignee)
	}
	if preview.ReviewReply == nil || preview.ReviewReply.Status != ReviewReplyStatusWillNotify {
		t.Fatalf("preview review_reply = %+v, want will_notify", preview.ReviewReply)
	}
	if preview.ReviewReply.RecipientType != "agent" || preview.ReviewReply.RecipientID != ownerID {
		t.Fatalf("preview recipient = %+v, want agent %s", preview.ReviewReply, ownerID)
	}

	resp := postMemberComment(t, issueID, rootID, "全部按你的建议走。")
	if resp.ReviewReply == nil {
		t.Fatal("create response has no review_reply")
	}
	if resp.ReviewReply.Status != string(DispatchQueued) {
		t.Fatalf("review_reply status = %q, want %q (%+v)", resp.ReviewReply.Status, DispatchQueued, resp.ReviewReply)
	}
	if resp.ReviewReply.RecipientID != ownerID {
		t.Fatalf("review_reply recipient = %+v, want owner %s", resp.ReviewReply, ownerID)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, ownerID); got != 1 {
		t.Errorf("owner queued tasks = %d, want 1", got)
	}
	if got := countAllTasksForAgent(t, issueID, reviewerID); got != 0 {
		t.Errorf("reviewer tasks = %d, want 0 (the reviewer must not be woken)", got)
	}
}

// TestReviewReply_ExplicitReviewerMentionRoutesToOwner: @mentioning the reviewer is
// a request addressed to the reviewer, so it follows the same handover rule.
func TestReviewReply_ExplicitReviewerMentionRoutesToOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Mention Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	content := fmt.Sprintf("[@审查员](mention://agent/%s) 这里的结论我不同意，请继续", reviewerID)
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content":   content,
		"parent_id": rootID,
	})
	requirePreviewAgents(t, preview, ownerID)
	if preview.ReviewReply == nil || preview.ReviewReply.RecipientID != ownerID {
		t.Fatalf("preview review_reply = %+v, want owner %s", preview.ReviewReply, ownerID)
	}
	if len(preview.Blocked) != 0 {
		t.Fatalf("preview blocked = %+v, want none (a reviewer mention is a handover, not a rejection)", preview.Blocked)
	}

	resp := postMemberComment(t, issueID, rootID, content)
	if resp.ReviewReply == nil || resp.ReviewReply.Status != string(DispatchQueued) {
		t.Fatalf("review_reply = %+v, want queued", resp.ReviewReply)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, ownerID); got != 1 {
		t.Errorf("owner queued tasks = %d, want 1", got)
	}
	if got := countAllTasksForAgent(t, issueID, reviewerID); got != 0 {
		t.Errorf("reviewer tasks = %d, want 0", got)
	}
}

// TestReviewReply_ReviewerAndOwnerMentionDispatchOnce: naming the owner explicitly
// on top of the reviewer handover must not run the owner twice.
func TestReviewReply_ReviewerAndOwnerMentionDispatchOnce(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Reply Dedup Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	content := fmt.Sprintf(
		"[@审查员](mention://agent/%s) [@Owner](mention://agent/%s) 继续吧",
		reviewerID, ownerID,
	)
	resp := postMemberComment(t, issueID, rootID, content)
	if got := countQueuedCommentTriggerTasks(t, issueID, ownerID); got != 1 {
		t.Errorf("owner queued tasks = %d, want exactly 1", got)
	}
	if resp.ReviewReply == nil || resp.ReviewReply.Status != string(DispatchQueued) {
		t.Fatalf("review_reply = %+v, want queued", resp.ReviewReply)
	}
	if got := countAllTasksForAgent(t, issueID, reviewerID); got != 0 {
		t.Errorf("reviewer tasks = %d, want 0", got)
	}
}

// TestReviewReply_OtherAgentMentionKeepsExplicitRule: an explicit mention of some
// other agent stays what it is; the owner is not additionally dispatched.
func TestReviewReply_OtherAgentMentionKeepsExplicitRule(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Reply Bystander Owner", nil)
	otherID := createHandlerTestAgent(t, "Review Reply Other Agent", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	content := fmt.Sprintf("[@Other](mention://agent/%s) 帮我看下这个结论", otherID)
	resp := postMemberComment(t, issueID, rootID, content)
	if resp.ReviewReply != nil {
		t.Fatalf("review_reply = %+v, want nil (the owner was not addressed)", resp.ReviewReply)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, otherID); got != 1 {
		t.Errorf("other agent queued tasks = %d, want 1", got)
	}
	if got := countAllTasksForAgent(t, issueID, ownerID); got != 0 {
		t.Errorf("owner tasks = %d, want 0", got)
	}
}

// TestReviewReply_FollowsCurrentOwner: the handover resolves the owner at reply time,
// so a reassigned issue goes to the new owner.
func TestReviewReply_FollowsCurrentOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	firstID := createHandlerTestAgent(t, "Review Reply First Owner", nil)
	secondID := createHandlerTestAgent(t, "Review Reply Second Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", firstID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	if _, err := testPool.Exec(context.Background(), `
		UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1
	`, issueID, secondID); err != nil {
		t.Fatalf("reassign issue: %v", err)
	}

	resp := postMemberComment(t, issueID, rootID, "改完再送审")
	if resp.ReviewReply == nil || resp.ReviewReply.RecipientID != secondID {
		t.Fatalf("review_reply = %+v, want new owner %s", resp.ReviewReply, secondID)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, secondID); got != 1 {
		t.Errorf("new owner queued tasks = %d, want 1", got)
	}
	if got := countAllTasksForAgent(t, issueID, firstID); got != 0 {
		t.Errorf("previous owner tasks = %d, want 0", got)
	}
}

// TestReviewReply_SquadOwnerRunsLeader: a squad assignment hands the reply to the
// squad's leader with the squad context, exactly like the existing squad routes.
func TestReviewReply_SquadOwnerRunsLeader(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	leaderID := createHandlerTestAgent(t, "Review Reply Squad Leader", nil)
	squadID := createCommentTriggerPreviewSquad(t, "Review Reply Squad", leaderID)
	issueID, reviewerID := reviewReplyFixture(t, "squad", squadID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	resp := postMemberComment(t, issueID, rootID, "按结论改")
	if resp.ReviewReply == nil || resp.ReviewReply.RecipientType != "squad" || resp.ReviewReply.RecipientID != squadID {
		t.Fatalf("review_reply = %+v, want squad %s", resp.ReviewReply, squadID)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, leaderID); got != 1 {
		t.Errorf("leader queued tasks = %d, want 1", got)
	}
	var isLeader bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT is_leader_task FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2
	`, issueID, leaderID).Scan(&isLeader); err != nil {
		t.Fatalf("load leader task: %v", err)
	}
	if !isLeader {
		t.Error("task is not a squad leader task")
	}
}

// TestReviewReply_HumanOwnerSavesWithoutRun: a human owner means no agent run, and
// the receipt says the reply went to that person.
func TestReviewReply_HumanOwnerSavesWithoutRun(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID, reviewerID := reviewReplyFixture(t, "member", testUserID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	resp := postMemberComment(t, issueID, rootID, "我自己来改")
	if resp.ID == "" {
		t.Fatal("reply was not saved")
	}
	if resp.ReviewReply == nil || resp.ReviewReply.Status != ReviewReplyStatusHumanAssignee {
		t.Fatalf("review_reply = %+v, want human_assignee", resp.ReviewReply)
	}
	if resp.ReviewReply.RecipientType != "member" || resp.ReviewReply.RecipientID != testUserID {
		t.Fatalf("review_reply recipient = %+v, want member %s", resp.ReviewReply, testUserID)
	}
	if got := countAllTasksForAgent(t, issueID, reviewerID); got != 0 {
		t.Errorf("reviewer tasks = %d, want 0", got)
	}
}

// TestReviewReply_NoOwnerReportsNoRecipient: with no assignee the reply is saved and
// the receipt states that nobody was notified, instead of staying silent.
func TestReviewReply_NoOwnerReportsNoRecipient(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID, reviewerID := reviewReplyFixture(t, "", "")
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	resp := postMemberComment(t, issueID, rootID, "有人看下吗")
	if resp.ID == "" {
		t.Fatal("reply was not saved")
	}
	if resp.ReviewReply == nil || resp.ReviewReply.Status != ReviewReplyStatusNoAssignee {
		t.Fatalf("review_reply = %+v, want no_assignee", resp.ReviewReply)
	}
	if got := countAllTasksForAgent(t, issueID, reviewerID); got != 0 {
		t.Errorf("reviewer tasks = %d, want 0", got)
	}
}

// TestReviewReply_UninvocableOwnerReportsReason: an owner the caller may not invoke
// is reported with the real reason — the handover must not bypass the gate.
func TestReviewReply_UninvocableOwnerReportsReason(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	privateOwnerID, _, _ := privateAgentTestFixture(t)
	issueID, reviewerID := reviewReplyFixture(t, "agent", privateOwnerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	preview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content":   "继续",
		"parent_id": rootID,
	})
	if len(preview.Agents) != 0 {
		t.Fatalf("preview agents = %+v, want none", preview.Agents)
	}
	if preview.ReviewReply == nil || preview.ReviewReply.Status != string(DispatchBlocked) ||
		preview.ReviewReply.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("preview review_reply = %+v, want blocked/invocation_not_allowed", preview.ReviewReply)
	}

	resp := postMemberComment(t, issueID, rootID, "继续")
	if resp.ReviewReply == nil || resp.ReviewReply.Status != string(DispatchBlocked) ||
		resp.ReviewReply.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("review_reply = %+v, want blocked/invocation_not_allowed", resp.ReviewReply)
	}
	if got := countAllTasksForAgent(t, issueID, privateOwnerID); got != 0 {
		t.Errorf("private owner tasks = %d, want 0", got)
	}
}

// TestReviewReply_BusyOwnerAbsorbsReply: when the owner already has a run for this
// issue, the second reply joins it rather than creating a duplicate run.
func TestReviewReply_BusyOwnerAbsorbsReply(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Reply Busy Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)

	first := postMemberComment(t, issueID, rootID, "第一处按建议改")
	if first.ReviewReply == nil || first.ReviewReply.Status != string(DispatchQueued) {
		t.Fatalf("first review_reply = %+v, want queued", first.ReviewReply)
	}
	second := postMemberComment(t, issueID, rootID, "第二处也按建议改")
	if second.ReviewReply == nil {
		t.Fatal("second reply has no review_reply")
	}
	switch second.ReviewReply.Status {
	case string(DispatchCoalesced), string(DispatchDeferred):
	default:
		t.Fatalf("second review_reply status = %q, want coalesced or deferred", second.ReviewReply.Status)
	}
	if got := countAllTasksForAgent(t, issueID, ownerID); got != 1 {
		t.Errorf("owner tasks = %d, want 1 (no duplicate run)", got)
	}
}

// TestReviewReply_OrdinaryAgentThreadUnaffected: replying to a normal agent's comment
// keeps the existing parent-author rule and reports no reviewer handover.
func TestReviewReply_OrdinaryAgentThreadUnaffected(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Ordinary Thread Owner", nil)
	otherID := createHandlerTestAgent(t, "Ordinary Thread Agent", nil)
	issueID := insertReviewReplyIssue(t, "agent", ownerID)
	rootID := insertAgentReplyComment(t, issueID, otherID, "", "普通讨论的根评论")

	// The preview resolves exactly the parent author: no reviewer handover, so the
	// issue owner is not pulled in as an extra recipient.
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{
		"content":   "回复普通 agent",
		"parent_id": rootID,
	})
	requirePreviewAgents(t, preview, otherID)
	if preview.ReviewReply != nil {
		t.Fatalf("preview review_reply = %+v, want nil", preview.ReviewReply)
	}

	resp := postMemberComment(t, issueID, rootID, "回复普通 agent")
	if resp.ReviewReply != nil {
		t.Fatalf("review_reply = %+v, want nil for a non-reviewer thread", resp.ReviewReply)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, otherID); got != 1 {
		t.Errorf("parent author queued tasks = %d, want 1", got)
	}
}

// TestReviewReply_AgentAuthorsDoNotHandOver pins the anti-loop boundary: the handover
// only serves member replies, so an agent's comment in the reviewer thread cannot
// wake the owner by mentioning the reviewer.
func TestReviewReply_AgentAuthorsDoNotHandOver(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Reply Agent Boundary Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)
	ctx := context.Background()
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	parent, err := testHandler.Queries.GetComment(ctx, parseUUID(rootID))
	if err != nil {
		t.Fatalf("load parent comment: %v", err)
	}

	content := fmt.Sprintf("[@审查员](mention://agent/%s) 继续", reviewerID)
	triggers, _, reviewReply := testHandler.computeCommentAgentTriggers(
		ctx, issue, content, &parent, "agent", ownerID, commentTriggerComputeOptions{},
	)
	if len(triggers) != 0 {
		t.Fatalf("agent-authored triggers = %+v, want none", triggers)
	}
	if reviewReply != nil {
		t.Fatalf("agent-authored review_reply = %+v, want nil", reviewReply)
	}
}

// TestReviewReply_ReviewerCommentDoesNotSelfTrigger pins the existing review flow's
// boundary: the reviewer's own conclusion comment routes nowhere on its own, so the
// reject flow's explicit wake is the only dispatch (no duplicate run).
func TestReviewReply_ReviewerCommentDoesNotSelfTrigger(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ownerID := createHandlerTestAgent(t, "Review Conclusion Owner", nil)
	issueID, reviewerID := reviewReplyFixture(t, "agent", ownerID)
	rootID := insertReviewConclusionComment(t, issueID, reviewerID)
	ctx := context.Background()
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	root, err := testHandler.Queries.GetComment(ctx, parseUUID(rootID))
	if err != nil {
		t.Fatalf("load reviewer comment: %v", err)
	}

	triggers, _, reviewReply := testHandler.computeCommentAgentTriggers(
		ctx, issue, root.Content, nil, "agent", reviewerID, commentTriggerComputeOptions{},
	)
	if len(triggers) != 0 || reviewReply != nil {
		t.Fatalf("reviewer conclusion resolution = %+v / %+v, want no triggers and no handover", triggers, reviewReply)
	}
	var systemKey string
	if err := testPool.QueryRow(ctx, `SELECT coalesce(system_key,'') FROM agent WHERE id = $1`, reviewerID).Scan(&systemKey); err != nil {
		t.Fatalf("load reviewer: %v", err)
	}
	if systemKey != review.SystemKey {
		t.Fatalf("system_key = %q, want %q", systemKey, review.SystemKey)
	}
}
