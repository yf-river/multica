package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func knowledgeImportFixture(t *testing.T, status string) string {
	t.Helper()
	return dbfx.Insert(t, "knowledge_import", testutil.Cols{
		"id": dbid.NewV7().String(), "workspace_id": testWorkspaceID, "created_by": testUserID,
		"source_url": "https://example.com/article", "resource_uri": "viking://resources/workspaces/" + testWorkspaceID + "/article",
		"status": status, "upstream_task_id": "owned-upstream-task",
	})
}

func TestKnowledgeImportIdempotencyAndWorkspaceIsolation(t *testing.T) {
	id := knowledgeImportFixture(t, "indexing")
	for range 2 {
		var result knowledgeImportResponse
		testutil.Call(t, testHandler.ImportKnowledgeURL, newRequest(http.MethodPost, "/", map[string]string{"request_id": id, "url": "https://example.com/article"})).Want(http.StatusOK).JSON(&result)
		if result.ID != id || result.Status != "indexing" {
			t.Fatalf("retry changed job: %+v", result)
		}
	}
	testutil.Call(t, testHandler.ImportKnowledgeURL, newRequest(http.MethodPost, "/", map[string]string{"request_id": id, "url": "https://example.com/other"})).Want(http.StatusConflict)
	for _, source := range []string{"http://169.254.169.254/latest/meta-data", "file:///etc/passwd"} {
		testutil.Call(t, testHandler.ImportKnowledgeURL, newRequest(http.MethodPost, "/", map[string]string{"url": source})).Want(http.StatusBadRequest)
	}
	foreign := dbfx.Workspace(t, "Other knowledge workspace", "knowledge-foreign-"+id)
	req := newRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Workspace-ID", foreign)
	testutil.Call(t, testHandler.GetKnowledgeImport, withURLParam(req, "importId", id)).Want(http.StatusNotFound)
}

func TestKnowledgeImportStatusPersistsUpstreamCompletion(t *testing.T) {
	id := knowledgeImportFixture(t, "indexing")
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/tasks/owned-upstream-task" || r.Header.Get("X-OpenViking-User") != testWorkspaceID {
			t.Errorf("wrong upstream identity: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
	}))
	t.Setenv("MULTICA_OPENVIKING_URL", upstream.URL)
	for range 2 {
		var result knowledgeImportResponse
		req := withURLParam(newRequest(http.MethodGet, "/", nil), "importId", id)
		testutil.Call(t, testHandler.GetKnowledgeImport, req).Want(http.StatusOK).JSON(&result)
		if result.Status != "completed" {
			t.Fatalf("completion lost: %+v", result)
		}
	}
	upstream.Close()
	if calls != 1 {
		t.Fatalf("completed import polled upstream %d times", calls)
	}
}

func TestKnowledgeImportInterruptedFetchHasReadableTerminalStatus(t *testing.T) {
	id := knowledgeImportFixture(t, "fetching")
	dbfx.Exec(t, "UPDATE knowledge_import SET created_at=now()-interval '3 minutes' WHERE id=$1", id)
	var result knowledgeImportResponse
	testutil.Call(t, testHandler.GetKnowledgeImport, withURLParam(newRequest(http.MethodGet, "/", nil), "importId", id)).Want(http.StatusOK).JSON(&result)
	if result.Status != "failed" || !strings.Contains(result.Error, "中断") {
		t.Fatalf("interrupted import remains active: %+v", result)
	}
}

func TestKnowledgeDeletePermissionsAndWorkspaceBoundary(t *testing.T) {
	root := "viking://resources/workspaces/" + testWorkspaceID
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.Header.Get("X-OpenViking-User") != testWorkspaceID || r.URL.Query().Get("uri") != root+"/article" {
			t.Errorf("incorrect delete: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"status":"ok","result":{}}`))
	}))
	defer upstream.Close()
	t.Setenv("MULTICA_OPENVIKING_URL", upstream.URL)
	request := func(uri string) *http.Request {
		return newRequest(http.MethodDelete, "/?uri="+url.QueryEscape(uri), nil)
	}
	testutil.Call(t, testHandler.DeleteKnowledgeResource, request(root+"/article")).Want(http.StatusOK)
	for _, uri := range []string{root + "-other/article", root + "/%2e%2e/other"} {
		testutil.Call(t, testHandler.DeleteKnowledgeResource, request(uri)).Want(http.StatusForbidden)
	}
	testutil.Call(t, testHandler.DeleteKnowledgeResource, request(root)).Want(http.StatusBadRequest)
	member := dbfx.User(t, "Knowledge member", "knowledge-member@example.test")
	dbfx.Member(t, testWorkspaceID, member, "member")
	req := request(root + "/article")
	req.Header.Set("X-User-ID", member)
	testutil.Call(t, testHandler.DeleteKnowledgeResource, req).Want(http.StatusForbidden)
	if calls != 1 {
		t.Fatalf("refused requests reached OpenViking: %d", calls)
	}
}
