package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateIssueRollsBackFieldsWhenAttachmentCannotBeLinked(t *testing.T) {
	ctx := context.Background()
	const originalTitle = "Issue attachment update rollback"
	issue := createHandlerCommentIssueFixture(t, originalTitle)

	var availableAttachmentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (
			workspace_id, uploader_type, uploader_id, filename, url,
			content_type, size_bytes
		)
		VALUES ($1, 'member', $2, 'update.txt', 'memory://issue-update', 'text/plain', 6)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&availableAttachmentID); err != nil {
		t.Fatalf("insert available attachment: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, availableAttachmentID)
	})
	missingAttachmentID := "019f4b33-1451-7489-851d-f2670810a642"

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{
		"title":          "title that must roll back",
		"attachment_ids": []string{availableAttachmentID, missingAttachmentID},
	})
	req = withURLParam(req, "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateIssue: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "attachments are unavailable") {
		t.Fatalf("UpdateIssue: expected unavailable attachment error, got %s", w.Body.String())
	}

	var storedTitle string
	if err := testPool.QueryRow(ctx, `SELECT title FROM issue WHERE id = $1`, issue.ID).Scan(&storedTitle); err != nil {
		t.Fatalf("load issue after rollback: %v", err)
	}
	if storedTitle != originalTitle {
		t.Fatalf("UpdateIssue: title = %q after attachment failure, want %q", storedTitle, originalTitle)
	}

	var linkedIssueID *string
	if err := testPool.QueryRow(ctx, `SELECT issue_id FROM attachment WHERE id = $1`, availableAttachmentID).Scan(&linkedIssueID); err != nil {
		t.Fatalf("load attachment after rollback: %v", err)
	}
	if linkedIssueID != nil {
		t.Fatalf("UpdateIssue: partial attachment link was not rolled back, issue_id=%s", *linkedIssueID)
	}

	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = 'issue:updated'
		  AND payload #>> '{issue,id}' = $1
	`, issue.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back issue event: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("UpdateIssue: rollback left %d durable issue events", eventCount)
	}
}

func TestUpdateIssueAcceptsAlreadyLinkedAttachment(t *testing.T) {
	issue := createHandlerCommentIssueFixture(t, "Issue attachment idempotence")
	attachmentID := createHandlerIssueAttachment(t, issue.ID, "already-linked.txt")

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{
		"title":          "Issue attachment idempotence updated",
		"attachment_ids": []string{attachmentID, attachmentID},
	})
	req = withURLParam(req, "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200 for already linked attachment, got %d: %s", w.Code, w.Body.String())
	}

	var linkedIssueID string
	if err := testPool.QueryRow(context.Background(), `SELECT issue_id FROM attachment WHERE id = $1`, attachmentID).Scan(&linkedIssueID); err != nil {
		t.Fatalf("load linked attachment: %v", err)
	}
	if linkedIssueID != issue.ID {
		t.Fatalf("attachment issue_id = %s, want %s", linkedIssueID, issue.ID)
	}
}

func TestUpdateIssueRollsBackFieldsWhenDerivedMetadataExceedsLimit(t *testing.T) {
	ctx := context.Background()
	issue := createHandlerCommentIssueFixture(t, "Issue metadata update rollback")

	var metadataSize int
	if err := testPool.QueryRow(ctx, `
		UPDATE issue
		SET metadata = jsonb_build_object('filler', repeat('x', 7900))
		WHERE id = $1
		RETURNING pg_column_size(metadata)
	`, issue.ID).Scan(&metadataSize); err != nil {
		t.Fatalf("seed near-limit metadata: %v", err)
	}
	if metadataSize >= 8192 {
		t.Fatalf("seed metadata size = %d, must start below the database limit", metadataSize)
	}

	tapdURL := "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004223"
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/issues/"+issue.ID, map[string]any{
		"description": "TAPD wiki URL: " + tapdURL,
	})
	req = withURLParam(req, "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateIssue: expected 400 for oversized derived metadata, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "8KB") {
		t.Fatalf("UpdateIssue: expected metadata size error, got %s", w.Body.String())
	}

	var description *string
	var metadata []byte
	if err := testPool.QueryRow(ctx, `SELECT description, metadata FROM issue WHERE id = $1`, issue.ID).Scan(&description, &metadata); err != nil {
		t.Fatalf("load issue after metadata rollback: %v", err)
	}
	if description != nil {
		t.Fatalf("UpdateIssue: description persisted despite metadata failure: %q", *description)
	}
	if strings.Contains(string(metadata), "source_provider") {
		t.Fatalf("UpdateIssue: derived metadata partially persisted after rollback: %s", metadata)
	}

	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = 'issue:updated'
		  AND payload #>> '{issue,id}' = $1
	`, issue.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count rolled-back issue event: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("UpdateIssue: metadata rollback left %d durable issue events", eventCount)
	}
}
