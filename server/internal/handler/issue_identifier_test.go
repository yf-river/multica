package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetIssueTreatsNumericSuffixUUIDAsUUID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("requires DB")
	}

	const issueID = "11111111-1111-4111-8111-487848365834"
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID); err != nil {
		t.Fatalf("remove stale issue fixture: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue (id, workspace_id, creator_type, creator_id, title, number)
		VALUES ($1, $2, 'member', $3, 'numeric suffix UUID', $4)
	`, issueID, testWorkspaceID, testUserID, nextHandlerTestIssueNumber(t)); err != nil {
		t.Fatalf("create issue fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	req := newRequest(http.MethodGet, "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.GetIssue(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue numeric-suffix UUID: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
