package handler

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestRenderPromptLibraryTrialMessageUsesCurrentVariables(t *testing.T) {
	rendered := renderPromptLibraryTrialMessage("请围绕 {{任务标题}} 澄清。背景：{{ 项目背景 }}", map[string]string{
		"任务标题": "登录失败",
		"项目背景": "账号系统",
	})
	if !strings.Contains(rendered, "请围绕 登录失败 澄清。背景：账号系统") {
		t.Fatalf("rendered message did not replace variables: %s", rendered)
	}
	if strings.Contains(rendered, "<用户输入>") {
		t.Fatalf("rendered message should omit empty user input: %s", rendered)
	}
}

func TestMissingPromptLibraryTrialVariables(t *testing.T) {
	missing := missingPromptLibraryTrialVariables("请围绕 {{任务标题}} 分析 {{ 项目背景 }}，再次确认 {{任务标题}}。", map[string]string{
		"任务标题": "登录失败",
		"项目背景": " ",
	})
	if len(missing) != 1 || missing[0] != "项目背景" {
		t.Fatalf("missing = %#v, want 项目背景", missing)
	}
}

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
		"variables":   []map[string]any{{"name": "issue_title", "label": "任务标题", "required": true}},
		"tags":        []string{"user-center", "小队"},
		"status":      "启用",
	}

	createW := httptest.NewRecorder()
	itemKey := uuid.NewString()
	createReq := newRequest(http.MethodPost, "/api/prompt-library", createBody)
	createReq.Header.Set("Idempotency-Key", itemKey)
	testHandler.CreatePromptLibraryItem(createW, createReq)
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
	replayReq := newRequest(http.MethodPost, "/api/prompt-library", createBody)
	replayReq.Header.Set("Idempotency-Key", itemKey)
	replayW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(replayW, replayReq)
	if replayW.Code != http.StatusCreated || replayW.Body.String() != createW.Body.String() {
		t.Fatalf("item replay = %d %s, want exact %s", replayW.Code, replayW.Body.String(), createW.Body.String())
	}
	changedItemBody := maps.Clone(createBody)
	changedItemBody["content"] = "changed content"
	changedItemReq := newRequest(http.MethodPost, "/api/prompt-library", changedItemBody)
	changedItemReq.Header.Set("Idempotency-Key", itemKey)
	changedItemW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(changedItemW, changedItemReq)
	if changedItemW.Code != http.StatusConflict {
		t.Fatalf("changed item replay = %d %s, want 409", changedItemW.Code, changedItemW.Body.String())
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
	versionBody := map[string]any{
		"content":     "请输出可直接验收的澄清结论。",
		"change_note": "收敛验收输出",
	}
	versionKey := uuid.NewString()
	createVersion := func() *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPost, "/api/prompt-library/"+created.ID+"/versions", versionBody), "id", created.ID)
		req.Header.Set("Idempotency-Key", versionKey)
		w := httptest.NewRecorder()
		testHandler.CreatePromptLibraryVersion(w, req)
		return w
	}
	versionW := createVersion()
	versionReplayW := createVersion()
	if versionW.Code != http.StatusCreated || versionReplayW.Code != http.StatusCreated || versionReplayW.Body.String() != versionW.Body.String() {
		t.Fatalf("version replay differs: first=%d %s replay=%d %s", versionW.Code, versionW.Body.String(), versionReplayW.Code, versionReplayW.Body.String())
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- createVersion()
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusCreated || response.Body.String() != versionW.Body.String() {
			t.Fatalf("concurrent version replay = %d %s, want exact", response.Code, response.Body.String())
		}
	}
	changedVersionReq := withURLParam(newRequest(http.MethodPost, "/api/prompt-library/"+created.ID+"/versions", map[string]any{
		"content": "changed version content", "change_note": "different intent",
	}), "id", created.ID)
	changedVersionReq.Header.Set("Idempotency-Key", versionKey)
	changedVersionW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryVersion(changedVersionW, changedVersionReq)
	if changedVersionW.Code != http.StatusConflict {
		t.Fatalf("changed version replay = %d %s, want 409", changedVersionW.Code, changedVersionW.Body.String())
	}

	versionsW := httptest.NewRecorder()
	testHandler.ListPromptLibraryVersions(versionsW, withURLParam(newRequest(http.MethodGet, "/api/prompt-library/"+created.ID+"/versions", nil), "id", created.ID))
	if versionsW.Code != http.StatusOK {
		t.Fatalf("versions status = %d, body = %s", versionsW.Code, versionsW.Body.String())
	}
	var versionsResp struct {
		Items []PromptLibraryVersionResponse `json:"items"`
		Total int                            `json:"total"`
	}
	if err := json.Unmarshal(versionsW.Body.Bytes(), &versionsResp); err != nil {
		t.Fatalf("decode versions response: %v", err)
	}
	if versionsResp.Total != 3 || len(versionsResp.Items) != 3 {
		t.Fatalf("versions response = %+v, want three versions", versionsResp)
	}
	if versionsResp.Items[0].Version != 3 || versionsResp.Items[0].Content != versionBody["content"] {
		t.Fatalf("latest version = %+v, want explicit v3", versionsResp.Items[0])
	}
	if versionsResp.Items[1].Version != 2 || versionsResp.Items[1].Source != "手动更新" || versionsResp.Items[1].Content != updated.Content {
		t.Fatalf("updated version = %+v, want v2", versionsResp.Items[1])
	}
	if versionsResp.Items[2].Version != 1 || versionsResp.Items[2].Source != "手动创建" || versionsResp.Items[2].Content != created.Content {
		t.Fatalf("initial version = %+v, want created v1", versionsResp.Items[2])
	}

	deleteW := httptest.NewRecorder()
	testHandler.DeletePromptLibraryItem(deleteW, withURLParam(newRequest(http.MethodDelete, "/api/prompt-library/"+created.ID, nil), "id", created.ID))
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteW.Code, deleteW.Body.String())
	}
	deletedItemReplay := httptest.NewRecorder()
	deletedItemReq := newRequest(http.MethodPost, "/api/prompt-library", createBody)
	deletedItemReq.Header.Set("Idempotency-Key", itemKey)
	testHandler.CreatePromptLibraryItem(deletedItemReplay, deletedItemReq)
	if deletedItemReplay.Code != http.StatusCreated || deletedItemReplay.Body.String() != createW.Body.String() {
		t.Fatalf("deleted item replay = %d %s, want durable exact response", deletedItemReplay.Code, deletedItemReplay.Body.String())
	}
	deletedVersionReplay := createVersion()
	if deletedVersionReplay.Code != http.StatusCreated || deletedVersionReplay.Body.String() != versionW.Body.String() {
		t.Fatalf("deleted version replay = %d %s, want durable exact response", deletedVersionReplay.Code, deletedVersionReplay.Body.String())
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
