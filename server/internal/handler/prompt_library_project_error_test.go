package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreatePromptLibraryItemPreservesProjectReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetProjectInWorkspace"})
	w := httptest.NewRecorder()

	h.CreatePromptLibraryItem(w, newRequest(http.MethodPost, "/api/prompt-library", map[string]any{
		"project_id": "11111111-1111-4111-8111-111111111111",
		"name":       "prompt project read failure",
		"content":    "preserve database failures",
	}))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("project lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
