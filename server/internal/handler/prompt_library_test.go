package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptLibraryCRUD(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})

	projectID := createPromptLibraryTestProject(t, "提示词库项目")
	createBody := map[string]any{
		"project_id":  projectID,
		"name":        "user-center 需求澄清提示词",
		"description": "给 user-center 小队队长使用的澄清模板",
		"prompt_type": "需求澄清",
		"content":     "请先澄清目标、边界、验收条件和风险。",
		"variables":   []map[string]any{{"name": "issue_title", "label": "issue 标题", "required": true}},
		"tags":        []string{"user-center", "小队"},
		"status":      "启用",
	}

	createW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(createW, newRequest(http.MethodPost, "/api/prompt-library", createBody))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptLibraryItemResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ProjectID == nil || *created.ProjectID != projectID {
		t.Fatalf("project_id = %v, want %s", created.ProjectID, projectID)
	}
	if created.PromptType != "需求澄清" || created.Status != "启用" || created.Version != 1 {
		t.Fatalf("unexpected created prompt: %+v", created)
	}

	listW := httptest.NewRecorder()
	testHandler.ListPromptLibraryItems(listW, newRequest(http.MethodGet, "/api/prompt-library?prompt_type=需求澄清&status=启用", nil))
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var listResp struct {
		Items []PromptLibraryItemResponse `json:"items"`
		Total int                         `json:"total"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Total != 1 || len(listResp.Items) != 1 || listResp.Items[0].ID != created.ID {
		t.Fatalf("list response = %+v, want created item only", listResp)
	}

	updateBody := map[string]any{
		"content": "请先澄清目标、边界、验收条件、风险和可观测指标。",
		"tags":    []string{"user-center", "小队", "观测"},
	}
	updateW := httptest.NewRecorder()
	testHandler.UpdatePromptLibraryItem(updateW, withURLParam(newRequest(http.MethodPut, "/api/prompt-library/"+created.ID, updateBody), "id", created.ID))
	if updateW.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateW.Code, updateW.Body.String())
	}
	var updated PromptLibraryItemResponse
	if err := json.Unmarshal(updateW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("version = %d, want 2", updated.Version)
	}
	if updated.ProjectID == nil || *updated.ProjectID != projectID {
		t.Fatalf("project_id after update = %v, want preserved %s", updated.ProjectID, projectID)
	}

	deleteW := httptest.NewRecorder()
	testHandler.DeletePromptLibraryItem(deleteW, withURLParam(newRequest(http.MethodDelete, "/api/prompt-library/"+created.ID, nil), "id", created.ID))
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteW.Code, deleteW.Body.String())
	}
}

func TestPromptLibraryRejectsForeignProject(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	foreignWorkspaceID := createPromptLibraryTestWorkspace(t, "prompt-library-foreign")
	foreignProjectID := createPromptLibraryTestProjectInWorkspace(t, foreignWorkspaceID, "外部项目")

	w := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(w, newRequest(http.MethodPost, "/api/prompt-library", map[string]any{
		"project_id":  foreignProjectID,
		"name":        "跨空间项目",
		"prompt_type": "需求澄清",
		"content":     "不能绑定其他工作区的项目。",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func createPromptLibraryTestProject(t *testing.T, title string) string {
	t.Helper()
	return createPromptLibraryTestProjectInWorkspace(t, testWorkspaceID, title)
}

func createPromptLibraryTestProjectInWorkspace(t *testing.T, workspaceID, title string) string {
	t.Helper()
	var projectID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO project (workspace_id, title, description, icon, status, priority)
		VALUES ($1, $2, '', '', 'planned', 'medium')
		RETURNING id
	`, workspaceID, title).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return projectID
}

func createPromptLibraryTestWorkspace(t *testing.T, slug string) string {
	t.Helper()
	var workspaceID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'PLF')
		RETURNING id
	`, "Prompt Library Foreign", slug).Scan(&workspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	return workspaceID
}
