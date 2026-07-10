package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createHandlerIssueAttachment(t *testing.T, issueID, filename string) string {
	t.Helper()

	var attachmentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (
			workspace_id, issue_id, uploader_type, uploader_id, filename,
			url, content_type, size_bytes
		)
		VALUES ($1, $2, 'member', $3, $4, 'memory://comment-atomicity', 'text/plain', 12)
		RETURNING id
	`, testWorkspaceID, issueID, testUserID, filename).Scan(&attachmentID); err != nil {
		t.Fatalf("insert issue attachment: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, attachmentID)
	})
	return attachmentID
}

func TestCreateCommentRollsBackWhenAttachmentCannotBeLinked(t *testing.T) {
	ctx := context.Background()
	issue := createHandlerCommentIssueFixture(t, "Comment attachment create rollback")
	availableID := createHandlerIssueAttachment(t, issue.ID, "available.txt")
	missingID := "019f4b1b-9c26-7d90-89ac-18fc51b3f676"
	const content = "must not survive a partial attachment claim"

	w, _ := issue.postComment(t, map[string]any{
		"content":        content,
		"attachment_ids": []string{availableID, missingID},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateComment: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "attachments are unavailable") {
		t.Fatalf("CreateComment: expected unavailable attachment error, got %s", w.Body.String())
	}

	var commentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, issue.ID, content).Scan(&commentCount); err != nil {
		t.Fatalf("count comments after rollback: %v", err)
	}
	if commentCount != 0 {
		t.Fatalf("CreateComment: failed attachment claim left %d comment rows", commentCount)
	}

	var commentID *string
	if err := testPool.QueryRow(ctx, `SELECT comment_id FROM attachment WHERE id = $1`, availableID).Scan(&commentID); err != nil {
		t.Fatalf("load available attachment after rollback: %v", err)
	}
	if commentID != nil {
		t.Fatalf("CreateComment: partial attachment claim was not rolled back, comment_id=%s", *commentID)
	}
	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'comment:created'
		  AND payload #>> '{comment,content}' = $1
	`, content).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back comment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("attachment rollback left %d durable comment events", eventCount)
	}
}

func TestUpdateCommentRollsBackContentAndAttachmentsTogether(t *testing.T) {
	ctx := context.Background()
	issue := createHandlerCommentIssueFixture(t, "Comment attachment update rollback")
	keptID := createHandlerIssueAttachment(t, issue.ID, "kept.txt")
	newID := createHandlerIssueAttachment(t, issue.ID, "new.txt")
	missingID := "019f4b1d-fc50-79c8-a420-079164bd73ca"
	const originalContent = "original comment body"

	createResponse, comment := issue.postComment(t, map[string]any{
		"content":        originalContent,
		"attachment_ids": []string{keptID},
	}, nil)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("CreateComment setup: expected 201, got %d: %s", createResponse.Code, createResponse.Body.String())
	}

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/comments/"+comment.ID, map[string]any{
		"content":        "content that must roll back",
		"attachment_ids": []string{keptID, newID, missingID},
	})
	req = withURLParam(req, "commentId", comment.ID)
	testHandler.UpdateComment(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateComment: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "attachments are unavailable") {
		t.Fatalf("UpdateComment: expected unavailable attachment error, got %s", w.Body.String())
	}

	var storedContent string
	if err := testPool.QueryRow(ctx, `SELECT content FROM comment WHERE id = $1`, comment.ID).Scan(&storedContent); err != nil {
		t.Fatalf("load comment after rollback: %v", err)
	}
	if storedContent != originalContent {
		t.Fatalf("UpdateComment: content = %q after attachment failure, want %q", storedContent, originalContent)
	}

	var keptCommentID string
	if err := testPool.QueryRow(ctx, `SELECT comment_id FROM attachment WHERE id = $1`, keptID).Scan(&keptCommentID); err != nil {
		t.Fatalf("load kept attachment: %v", err)
	}
	if keptCommentID != comment.ID {
		t.Fatalf("UpdateComment: kept attachment comment_id = %s, want %s", keptCommentID, comment.ID)
	}
	var newCommentID *string
	if err := testPool.QueryRow(ctx, `SELECT comment_id FROM attachment WHERE id = $1`, newID).Scan(&newCommentID); err != nil {
		t.Fatalf("load newly requested attachment: %v", err)
	}
	if newCommentID != nil {
		t.Fatalf("UpdateComment: newly requested attachment was not rolled back, comment_id=%s", *newCommentID)
	}
}
