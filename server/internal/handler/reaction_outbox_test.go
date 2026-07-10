package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAddIssueReactionCommitsDurableEvent(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Issue reaction durable event")
	cleanupCommentOutboxForIssue(t, issue.ID)
	w := addIssueReactionForTest(t, issue.ID, "thumbs_up")
	if w.Code != http.StatusCreated {
		t.Fatalf("AddIssueReaction: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var reaction IssueReactionResponse
	if err := json.NewDecoder(w.Body).Decode(&reaction); err != nil {
		t.Fatalf("decode issue reaction: %v", err)
	}
	assertReactionEvent(t, protocol.EventIssueReactionAdded, issue.ID, reaction.ID)
}

func TestAddIssueReactionRollsBackWhenEventInsertFails(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Issue reaction rollback")
	cleanupCommentOutboxForIssue(t, issue.ID)
	installOutboxStreamFailure(t, "issue:"+issue.ID)
	w := addIssueReactionForTest(t, issue.ID, "rollback_issue_reaction")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("AddIssueReaction: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertNoIssueReactionOrEvent(t, issue.ID, protocol.EventIssueReactionAdded, "rollback_issue_reaction")
}

func TestAddCommentReactionCommitsDurableEvent(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment reaction durable event")
	cleanupCommentOutboxForIssue(t, issue.ID)
	commentResponse, comment := issue.postComment(t, map[string]any{"content": "reaction target"}, nil)
	if commentResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", commentResponse.Code, commentResponse.Body.String())
	}
	w := addCommentReactionForTest(t, comment.ID, "eyes")
	if w.Code != http.StatusCreated {
		t.Fatalf("AddReaction: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var reaction ReactionResponse
	if err := json.NewDecoder(w.Body).Decode(&reaction); err != nil {
		t.Fatalf("decode comment reaction: %v", err)
	}
	assertReactionEvent(t, protocol.EventReactionAdded, issue.ID, reaction.ID)
}

func TestAddCommentReactionRollsBackWhenEventInsertFails(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Comment reaction rollback")
	cleanupCommentOutboxForIssue(t, issue.ID)
	commentResponse, comment := issue.postComment(t, map[string]any{"content": "rollback reaction target"}, nil)
	if commentResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", commentResponse.Code, commentResponse.Body.String())
	}
	installOutboxStreamFailure(t, "issue:"+issue.ID)
	w := addCommentReactionForTest(t, comment.ID, "rollback_comment_reaction")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("AddReaction: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertNoCommentReactionOrEvent(t, comment.ID, protocol.EventReactionAdded, "rollback_comment_reaction")
}

func addIssueReactionForTest(t *testing.T, issueID, emoji string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/reactions", map[string]any{"emoji": emoji})
	req = withURLParam(req, "id", issueID)
	testHandler.AddIssueReaction(w, req)
	return w
}

func addCommentReactionForTest(t *testing.T, commentID, emoji string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/comments/"+commentID+"/reactions", map[string]any{"emoji": emoji})
	req = withURLParam(req, "commentId", commentID)
	testHandler.AddReaction(w, req)
	return w
}

func assertReactionEvent(t *testing.T, eventType, issueID, reactionID string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = $1
		  AND stream_key = 'issue:' || $2
		  AND payload #>> '{reaction,id}' = $3
	`, eventType, issueID, reactionID).Scan(&count); err != nil {
		t.Fatalf("count durable reaction event: %v", err)
	}
	if count != 1 {
		t.Fatalf("durable reaction events = %d, want 1", count)
	}
}

func assertNoIssueReactionOrEvent(t *testing.T, issueID, eventType, emoji string) {
	t.Helper()
	var reactionCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM issue_reaction WHERE issue_id = $1 AND emoji = $2
	`, issueID, emoji).Scan(&reactionCount); err != nil {
		t.Fatalf("count rolled-back reactions: %v", err)
	}
	if reactionCount != 0 {
		t.Fatalf("reaction persisted without durable event: %d rows", reactionCount)
	}
	assertNoReactionEvent(t, eventType, emoji)
}

func assertNoCommentReactionOrEvent(t *testing.T, commentID, eventType, emoji string) {
	t.Helper()
	var reactionCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment_reaction WHERE comment_id = $1 AND emoji = $2
	`, commentID, emoji).Scan(&reactionCount); err != nil {
		t.Fatalf("count rolled-back reactions: %v", err)
	}
	if reactionCount != 0 {
		t.Fatalf("reaction persisted without durable event: %d rows", reactionCount)
	}
	assertNoReactionEvent(t, eventType, emoji)
}

func assertNoReactionEvent(t *testing.T, eventType, emoji string) {
	t.Helper()
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND payload #>> '{reaction,emoji}' = $2
	`, eventType, emoji).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back reaction events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("failed reaction left %d durable events", eventCount)
	}
}
