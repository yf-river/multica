package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetIssuePreservesEnrichmentFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createTestIssue(t, "issue enrichment failure", "none")
	for _, queryName := range []string{"ListLabelsForIssues", "ListIssueReactions", "ListAttachmentsByIssue"} {
		t.Run(queryName, func(t *testing.T) {
			h := *testHandler
			h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: queryName})
			w := httptest.NewRecorder()
			h.GetIssue(w, withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure: expected 500, got %d: %s", queryName, w.Code, w.Body.String())
			}
		})
	}
}
