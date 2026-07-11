package main

import (
	"context"
	"io"
	"testing"
)

// createTestAttachment inserts a test attachment row directly into the DB,
// linked to the given issue with no comment_id. Returns the attachment UUID.
func createTestAttachment(t *testing.T, issueID string) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, issue_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1::uuid, $2::uuid, 'member', $3::uuid, 'test.txt', 'https://example.com/test.txt', 'text/plain', 42)
		RETURNING id::text
	`, testWorkspaceID, issueID, testUserID).Scan(&id)
	if err != nil {
		t.Fatalf("createTestAttachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1::uuid`, id)
	})
	return id
}

// listCommentAttachmentIDs returns the attachment IDs linked to a comment.
func listCommentAttachmentIDs(t *testing.T, commentID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(),
		`SELECT id::text FROM attachment WHERE comment_id = $1::uuid ORDER BY created_at`, commentID)
	if err != nil {
		t.Fatalf("listCommentAttachmentIDs: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("listCommentAttachmentIDs scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func createCommentWithAttachments(t *testing.T, issueID, content string, attachmentIDs []string) string {
	t.Helper()

	resp := authRequest(t, "POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content":        content,
		"type":           "comment",
		"attachment_ids": attachmentIDs,
	})
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("CreateComment: expected 201, got %d: %s", resp.StatusCode, body)
	}
	var comment map[string]any
	readJSON(t, resp, &comment)
	return comment["id"].(string)
}

func updateComment(t *testing.T, commentID string, payload map[string]any) {
	t.Helper()

	resp := authRequest(t, "PUT", "/api/comments/"+commentID, payload)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("UpdateComment: expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestUpdateCommentAttachments(t *testing.T) {
	issueID := createIssue(t, "Attachment edit integration test")
	t.Cleanup(func() {
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("edit to remove some attachments keeps the rest", func(t *testing.T) {
		att1 := createTestAttachment(t, issueID)
		att2 := createTestAttachment(t, issueID)
		att3 := createTestAttachment(t, issueID)

		// Create comment with all three attachments.
		commentID := createCommentWithAttachments(t, issueID, "comment with three attachments", []string{att1, att2, att3})

		// Verify all three are linked.
		ids := listCommentAttachmentIDs(t, commentID)
		if len(ids) != 3 {
			t.Fatalf("expected 3 attachments, got %d", len(ids))
		}

		// Edit: keep only att1 and att3, remove att2.
		updateComment(t, commentID, map[string]any{
			"content":        "updated — removed att2",
			"attachment_ids": []string{att1, att3},
		})

		ids = listCommentAttachmentIDs(t, commentID)
		if len(ids) != 2 {
			t.Fatalf("expected 2 attachments after edit, got %d", len(ids))
		}
		idSet := map[string]bool{ids[0]: true, ids[1]: true}
		if !idSet[att1] || !idSet[att3] {
			t.Errorf("expected att1 and att3 to remain, got %v", ids)
		}
	})

	t.Run("edit to remove all attachments", func(t *testing.T) {
		att1 := createTestAttachment(t, issueID)

		commentID := createCommentWithAttachments(t, issueID, "comment with one attachment", []string{att1})

		if ids := listCommentAttachmentIDs(t, commentID); len(ids) != 1 {
			t.Fatalf("expected 1 attachment, got %d", len(ids))
		}

		// Edit with empty attachment_ids to remove all.
		updateComment(t, commentID, map[string]any{
			"content":        "no more attachments",
			"attachment_ids": []string{},
		})

		if ids := listCommentAttachmentIDs(t, commentID); len(ids) != 0 {
			t.Fatalf("expected 0 attachments after removing all, got %d", len(ids))
		}
	})

	t.Run("old client omitting attachment_ids preserves existing attachments", func(t *testing.T) {
		att1 := createTestAttachment(t, issueID)

		commentID := createCommentWithAttachments(t, issueID, "comment with attachment", []string{att1})

		if ids := listCommentAttachmentIDs(t, commentID); len(ids) != 1 {
			t.Fatalf("expected 1 attachment, got %d", len(ids))
		}

		// Old client: only sends content, no attachment_ids field at all.
		updateComment(t, commentID, map[string]any{
			"content": "edited content without attachment_ids",
		})

		ids := listCommentAttachmentIDs(t, commentID)
		if len(ids) != 1 {
			t.Fatalf("expected 1 attachment preserved (old client), got %d", len(ids))
		}
		if ids[0] != att1 {
			t.Errorf("expected att1 %q preserved, got %q", att1, ids[0])
		}
	})
}
