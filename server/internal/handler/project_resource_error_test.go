package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProjectResourceReadsPreserveDatabaseFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	project := createProjectResourceTestProject(t, "project resource read failure")

	t.Run("project lookup cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := newRequest(http.MethodGet, "/api/projects/"+project.ID+"/resources", nil).WithContext(ctx)
		req = withURLParam(req, "id", project.ID)
		w := httptest.NewRecorder()

		testHandler.ListProjectResources(w, req)

		if w.Code != 499 {
			t.Fatalf("canceled project resource list: expected 499, got %d: %s", w.Code, w.Body.String())
		}
	})

	createW := httptest.NewRecorder()
	createReq := newRequest(http.MethodPost, "/api/projects/"+project.ID+"/resources", map[string]any{
		"resource_type": "github_repo",
		"resource_ref":  map[string]any{"url": "https://github.com/multica-ai/read-failure"},
	})
	createReq = withURLParam(createReq, "id", project.ID)
	testHandler.CreateProjectResource(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create project resource: expected 201, got %d: %s", createW.Code, createW.Body.String())
	}
	var resource ProjectResourceResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &resource); err != nil {
		t.Fatalf("decode project resource: %v", err)
	}

	t.Run("resource query failure", func(t *testing.T) {
		h := *testHandler
		h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetProjectResourceInWorkspace"})
		req := newRequest(http.MethodPatch, "/api/projects/"+project.ID+"/resources/"+resource.ID, map[string]any{})
		req = withURLParams(req, "id", project.ID, "resourceId", resource.ID)
		w := httptest.NewRecorder()

		h.UpdateProjectResource(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("project resource query failure: expected 500, got %d: %s", w.Code, w.Body.String())
		}
	})
}
