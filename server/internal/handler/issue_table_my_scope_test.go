package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMyIssueScopePreservesTaskTokenBoundary(t *testing.T) {
	h := &Handler{}
	for _, source := range []string{"", "task_token"} {
		t.Run(source, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/issues/table/rows", nil)
			r.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
			r.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000002")
			r.Header.Set("X-Actor-Source", source)
			compiled, ok := h.compileIssueTableQuery(httptest.NewRecorder(), r, issueTableQuerySpec{
				Scope: issueTableScope{Kind: "my", Relation: "assigned"},
				Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
			})
			if !ok {
				t.Fatal("query compilation failed")
			}
			if source == "task_token" {
				if compiled.crossWorkspace || !strings.Contains(compiled.where, "i.workspace_id = $1") {
					t.Fatal("task token escaped its workspace")
				}
			} else if !compiled.crossWorkspace || !strings.Contains(compiled.where, "FROM member m WHERE m.user_id") {
				t.Fatal("personal aggregation must require workspace membership")
			}
		})
	}
}
