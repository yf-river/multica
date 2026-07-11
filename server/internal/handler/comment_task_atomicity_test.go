package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCreateCommentRollsBackWhenTriggeredTaskFails(t *testing.T) {
	agentID := createHandlerTestAgent(t, "atomic-comment-agent-"+uuid.NewString(), nil)
	issue := createHandlerAssignedCommentIssueFixture(t, "atomic comment task "+uuid.NewString(), agentID)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.issue_id = '%s'::uuid", issue.ID))
	content := "comment and triggered task must commit together " + uuid.NewString()

	w, _ := issue.postComment(t, map[string]any{"content": content}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var commentCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, issue.ID, content).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 0 {
		t.Fatalf("comment committed without triggered task: %d rows", commentCount)
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND stream_key = 'issue:' || $2
		  AND payload #>> '{comment,content}' = $3
	`, protocol.EventCommentCreated, issue.ID, content).Scan(&eventCount); err != nil {
		t.Fatalf("count comment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("comment event committed without triggered task: %d rows", eventCount)
	}
}

func TestUpdateCommentRollsBackCancellationWhenReplacementTaskFails(t *testing.T) {
	agentID := createHandlerTestAgent(t, "atomic-comment-edit-agent-"+uuid.NewString(), nil)
	issue := createHandlerAssignedCommentIssueFixture(t, "atomic comment edit "+uuid.NewString(), agentID)
	oldContent := "original actionable comment " + uuid.NewString()
	w, comment := issue.postComment(t, map[string]any{"content": oldContent}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create comment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var originalTaskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id::text FROM agent_task_queue
		WHERE trigger_comment_id = $1 AND status = 'queued'
	`, comment.ID).Scan(&originalTaskID); err != nil {
		t.Fatalf("load original comment task: %v", err)
	}
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.issue_id = '%s'::uuid", issue.ID))

	w = httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/comments/"+comment.ID, map[string]any{
		"content": "replacement actionable comment " + uuid.NewString(),
	})
	req = withURLParam(req, "commentId", comment.ID)
	testHandler.UpdateComment(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var content, taskStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT content FROM comment WHERE id = $1`, comment.ID).Scan(&content); err != nil {
		t.Fatalf("reload comment: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, originalTaskID).Scan(&taskStatus); err != nil {
		t.Fatalf("reload original task: %v", err)
	}
	if content != oldContent || taskStatus != "queued" {
		t.Fatalf("partial edit committed: content=%q task_status=%q", content, taskStatus)
	}
}

func TestUpdateCommentCommitsDurableEvent(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "durable comment edit "+uuid.NewString())
	w, comment := issue.postComment(t, map[string]any{"content": "before durable edit"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create comment: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	content := "after durable edit " + uuid.NewString()
	w = httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/comments/"+comment.ID, map[string]any{"content": content})
	req = withURLParam(req, "commentId", comment.ID)
	testHandler.UpdateComment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update comment: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND stream_key = 'issue:' || $2
		  AND payload #>> '{comment,id}' = $3
		  AND payload #>> '{comment,content}' = $4
	`, protocol.EventCommentUpdated, issue.ID, comment.ID, content).Scan(&eventCount); err != nil {
		t.Fatalf("count durable comment update events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("durable comment update events = %d, want 1", eventCount)
	}
}
