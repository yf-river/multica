package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRerunIssueRequiresExplicitTarget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createTestIssue(t, "rerun requires explicit target", "todo", "medium")
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", nil), "id", issueID)

	testHandler.RerunIssue(w, req)

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "rerun target is required") {
		t.Fatalf("empty rerun body: expected explicit-target 400, got %d: %s", w.Code, w.Body.String())
	}
}
