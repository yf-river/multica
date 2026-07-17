package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func enqueueMentionedAgentTasksForTest(t *testing.T, ctx context.Context, issue db.Issue, comment db.Comment, authorType, authorID string) {
	t.Helper()
	triggers, err := testHandler.computeMentionedAgentCommentTriggers(ctx, issue, comment.Content, nil, authorType, authorID, commentTriggerComputeOptions{})
	if err != nil {
		t.Fatalf("compute mention triggers: %v", err)
	}
	tx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mention task transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	projection, err := testHandler.createCommentAgentTriggersInTx(ctx, testHandler.Queries.WithTx(tx), issue, comment.ID, triggers)
	if err != nil {
		t.Fatalf("create mention task projection: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit mention task projection: %v", err)
	}
	testHandler.publishCommentTaskProjection(ctx, projection)
}

type selfMentionFixture struct {
	JID        string
	RuntimeID  string
	IssueAID   string
	IssueA     db.Issue
	IssueBID   string
	IssueB     db.Issue
	CommentAID string
	CommentA   db.Comment
	CommentBID string
	CommentB   db.Comment
}

func TestMentionTriggerPreservesAgentLookupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	issue := db.Issue{WorkspaceID: util.MustParseUUID(testWorkspaceID)}

	triggers, err := testHandler.computeMentionedAgentCommentTriggers(
		ctx,
		issue,
		"[@Agent](mention://agent/11111111-1111-1111-1111-111111111111)",
		nil,
		"member",
		testUserID,
		commentTriggerComputeOptions{},
	)
	if len(triggers) != 0 || err == nil {
		t.Fatalf("computeMentionedAgentCommentTriggers() triggers=%v err=%v, want no triggers with lookup error", triggers, err)
	}
}

func newSelfMentionFixture(t *testing.T) selfMentionFixture {
	t.Helper()
	ctx := context.Background()

	jID := oldestHandlerTestAgentID(t)
	runtimeID := squadAgentRuntimeID(t, jID)

	insertIssue := func(title string) string {
		t.Helper()
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, creator_type, creator_id, title, assignee_type, assignee_id, number)
			VALUES ($1, 'member', $2, $3, 'agent', $4, $5)
			RETURNING id
		`, testWorkspaceID, testUserID, title, jID, nextHandlerTestIssueNumber(t)).Scan(&id); err != nil {
			t.Fatalf("create issue %q: %v", title, err)
		}
		t.Cleanup(func() {
			mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
			mustExec(t, context.Background(), `DELETE FROM comment WHERE issue_id = $1`, id)
			mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, id)
		})
		return id
	}

	insertJComment := func(issueID, content string) string {
		t.Helper()
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
			VALUES ($1, $2, 'agent', $3, $4)
			RETURNING id
		`, testWorkspaceID, issueID, jID, content).Scan(&id); err != nil {
			t.Fatalf("create comment on %s: %v", issueID, err)
		}
		return id
	}

	issueAID := insertIssue("self-mention test A (same-issue scenarios)")
	issueBID := insertIssue("self-mention test B (parent issue, cross-issue handoff)")

	commentAID := insertJComment(issueAID, "[@J](mention://agent/"+jID+") follow-up coming")
	commentBID := insertJComment(issueBID, "Child issue done — [@J](mention://agent/"+jID+") please wrap up here")

	issueA, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueAID))
	if err != nil {
		t.Fatalf("load issueA: %v", err)
	}
	issueB, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueBID))
	if err != nil {
		t.Fatalf("load issueB: %v", err)
	}
	commentA, err := testHandler.Queries.GetComment(ctx, util.MustParseUUID(commentAID))
	if err != nil {
		t.Fatalf("load commentA: %v", err)
	}
	commentB, err := testHandler.Queries.GetComment(ctx, util.MustParseUUID(commentBID))
	if err != nil {
		t.Fatalf("load commentB: %v", err)
	}

	return selfMentionFixture{
		JID:        jID,
		RuntimeID:  runtimeID,
		IssueAID:   issueAID,
		IssueA:     issueA,
		IssueBID:   issueBID,
		IssueB:     issueB,
		CommentAID: commentAID,
		CommentA:   commentA,
		CommentBID: commentBID,
		CommentB:   commentB,
	}
}

func countQueuedOrDispatched(t *testing.T, agentID, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued', 'dispatched')
	`, issueID, agentID).Scan(&n); err != nil {
		t.Fatalf("count queued/dispatched tasks: %v", err)
	}
	return n
}

func TestEnqueueMentionedAgentTasks_SelfMentionCrossIssueEnqueues(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSelfMentionFixture(t)

	if got := countQueuedOrDispatched(t, fx.JID, fx.IssueBID); got != 0 {
		t.Fatalf("before: expected 0 pending tasks on parent issue, got %d", got)
	}

	enqueueMentionedAgentTasksForTest(t, ctx, fx.IssueB, fx.CommentB, "agent", fx.JID)

	if got := countQueuedOrDispatched(t, fx.JID, fx.IssueBID); got != 1 {
		t.Fatalf("after self-mention from another issue: expected 1 queued task on parent issue, got %d", got)
	}
}

func TestEnqueueMentionedAgentTasks_SelfMentionWhileRunningQueuesFollowup(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSelfMentionFixture(t)

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status)
		VALUES ($1, $2, $3, 'running')
	`, fx.JID, fx.RuntimeID, fx.IssueAID); err != nil {
		t.Fatalf("seed running task: %v", err)
	}

	if got := countQueuedOrDispatched(t, fx.JID, fx.IssueAID); got != 0 {
		t.Fatalf("before: expected 0 queued/dispatched tasks (only the running task), got %d", got)
	}

	enqueueMentionedAgentTasksForTest(t, ctx, fx.IssueA, fx.CommentA, "agent", fx.JID)

	if got := countQueuedOrDispatched(t, fx.JID, fx.IssueAID); got != 1 {
		t.Fatalf("after self-mention while running: expected 1 new queued follow-up, got %d", got)
	}
}

func TestEnqueueMentionedAgentTasks_SelfMentionDedupesAgainstPendingTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fx := newSelfMentionFixture(t)

	cases := []struct {
		name   string
		status string
	}{
		{name: "queued task blocks duplicate", status: "queued"},
		{name: "dispatched task blocks duplicate", status: "dispatched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, fx.IssueAID); err != nil {
				t.Fatalf("reset tasks: %v", err)
			}
			if _, err := testPool.Exec(ctx, `
				INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status)
				VALUES ($1, $2, $3, $4)
			`, fx.JID, fx.RuntimeID, fx.IssueAID, tc.status); err != nil {
				t.Fatalf("seed %s task: %v", tc.status, err)
			}

			before := countQueuedOrDispatched(t, fx.JID, fx.IssueAID)
			if before != 1 {
				t.Fatalf("before: expected 1 pre-existing %s task, got %d", tc.status, before)
			}

			enqueueMentionedAgentTasksForTest(t, ctx, fx.IssueA, fx.CommentA, "agent", fx.JID)

			after := countQueuedOrDispatched(t, fx.JID, fx.IssueAID)
			if after != 1 {
				t.Fatalf("after self-mention with pre-existing %s task: expected dedupe (still 1), got %d", tc.status, after)
			}
		})
	}
}
