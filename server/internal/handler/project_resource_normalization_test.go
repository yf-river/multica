package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestCreateProject_NormalizesBundledResourceType(t *testing.T) {
	title := "normalized resource type " + uuid.NewString()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title": title,
		"resources": []map[string]any{{
			"resource_type": "  github_repo  ",
			"resource_ref": map[string]any{
				"url": "https://github.com/example/normalized-resource",
			},
		}},
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}

	var response CreateProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, response.ID)
	})
	if len(response.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(response.Resources))
	}
	if got := response.Resources[0].ResourceType; got != "github_repo" {
		t.Fatalf("response resource_type = %q, want github_repo", got)
	}

	var stored string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT resource_type FROM project_resource WHERE id = $1`,
		response.Resources[0].ID,
	).Scan(&stored); err != nil {
		t.Fatalf("load stored resource type: %v", err)
	}
	if stored != "github_repo" {
		t.Fatalf("stored resource_type = %q, want github_repo", stored)
	}
}
