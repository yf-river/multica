package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCreateCommentCommitsDurableEvent(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment durable event")
	cleanupCommentOutboxForIssue(t, issue.ID)
	w, comment := issue.postComment(t, map[string]any{"content": "durable comment"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = $1
		  AND stream_key = 'issue:' || $2
		  AND payload #>> '{comment,id}' = $3
		  AND payload #>> '{comment,content}' = 'durable comment'
	`, protocol.EventCommentCreated, issue.ID, comment.ID).Scan(&count); err != nil {
		t.Fatalf("count durable comment event: %v", err)
	}
	if count != 1 {
		t.Fatalf("durable comment events = %d, want 1", count)
	}
}

func TestCreateCommentRollsBackWhenDurableEventCannotBeInserted(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment outbox rollback")
	cleanupCommentOutboxForIssue(t, issue.ID)
	installOutboxStreamFailure(t, "issue:"+issue.ID)
	const content = "comment must roll back with event"

	w, _ := issue.postComment(t, map[string]any{"content": content}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("CreateComment: expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var commentCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, issue.ID, content).Scan(&commentCount); err != nil {
		t.Fatalf("count rolled-back comments: %v", err)
	}
	if commentCount != 0 {
		t.Fatalf("comment persisted without durable event: %d rows", commentCount)
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND payload #>> '{comment,content}' = $2
	`, protocol.EventCommentCreated, content).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back comment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("failed comment left %d durable events", eventCount)
	}
}

func TestCreateCommentRollsBackWhenResolvedThreadCannotBeReopened(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment unresolve rollback")
	cleanupCommentOutboxForIssue(t, issue.ID)
	rootResponse, root := issue.postComment(t, map[string]any{"content": "resolved root"}, nil)
	if rootResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment root: expected 201, got %d: %s", rootResponse.Code, rootResponse.Body.String())
	}
	resolveCommentHTTP(t, root.ID)
	installCommentUnresolveFailure(t, root.ID)

	const replyContent = "reply and unresolve must commit together"
	replyResponse, _ := issue.postComment(t, map[string]any{
		"content":   replyContent,
		"parent_id": root.ID,
	}, nil)
	if replyResponse.Code != http.StatusInternalServerError {
		t.Fatalf("CreateComment reply: expected 500, got %d: %s", replyResponse.Code, replyResponse.Body.String())
	}
	if !commentResolved(t, root.ID) {
		t.Fatal("failed reply unexpectedly reopened the resolved thread")
	}

	var replyCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, issue.ID, replyContent).Scan(&replyCount); err != nil {
		t.Fatalf("count rolled-back replies: %v", err)
	}
	if replyCount != 0 {
		t.Fatalf("reply persisted while its thread stayed resolved: %d rows", replyCount)
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND payload #>> '{comment,content}' = $2
	`, protocol.EventCommentCreated, replyContent).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back reply events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("failed reply left %d durable events", eventCount)
	}
}

func TestCreateCommentReopensResolvedThreadInSameCommit(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment unresolve success")
	cleanupCommentOutboxForIssue(t, issue.ID)
	rootResponse, root := issue.postComment(t, map[string]any{"content": "resolved root for reply"}, nil)
	if rootResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment root: expected 201, got %d: %s", rootResponse.Code, rootResponse.Body.String())
	}
	resolveCommentHTTP(t, root.ID)

	const replyContent = "reply reopens resolved thread"
	replyResponse, reply := issue.postComment(t, map[string]any{
		"content":   replyContent,
		"parent_id": root.ID,
	}, nil)
	if replyResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment reply: expected 201, got %d: %s", replyResponse.Code, replyResponse.Body.String())
	}
	if commentResolved(t, root.ID) {
		t.Fatal("committed reply left its thread resolved")
	}

	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1
		  AND stream_key = 'issue:' || $2
		  AND payload #>> '{comment,id}' = $3
	`, protocol.EventCommentCreated, issue.ID, reply.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count committed reply events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("committed reply durable events = %d, want 1", eventCount)
	}
}

func cleanupCommentOutboxForIssue(t *testing.T, issueID string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, issueID)
	})
}

func installCommentUnresolveFailure(t *testing.T, commentID string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "comment_unresolve_fail_fn_" + suffix
	triggerName := "comment_unresolve_fail_" + suffix
	ctx := context.Background()
	t.Cleanup(func() {
		testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON comment`, triggerName))
		testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced comment unresolve failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE UPDATE ON comment
		FOR EACH ROW WHEN (OLD.id = '%s' AND OLD.resolved_at IS NOT NULL AND NEW.resolved_at IS NULL)
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, commentID, functionName)); err != nil {
		t.Fatalf("install comment unresolve failure trigger: %v", err)
	}
}
