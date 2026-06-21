package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptEvaluationAssetCRUD(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPrompt(t, testWorkspaceID, "评测提示词")

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":   promptID,
		"name":        "user-center 澄清数据集",
		"description": "用于验证澄清提示词",
		"asset_type":  "数据集",
		"payload":     map[string]any{"cases": []map[string]any{{"输入": "登录失败", "期望": "询问边界和验收"}}},
		"status":      "启用",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.AssetType != "数据集" || created.PromptID == nil || *created.PromptID != promptID {
		t.Fatalf("created = %+v", created)
	}

	listW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationAssets(listW, newRequest(http.MethodGet, "/api/prompt-evaluation-assets?asset_type=数据集&status=启用", nil))
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var listResp struct {
		Items []PromptEvaluationAssetResponse `json:"items"`
		Total int                             `json:"total"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Total != 1 || listResp.Items[0].ID != created.ID {
		t.Fatalf("list response = %+v", listResp)
	}

	updateW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationAsset(updateW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-assets/"+created.ID, map[string]any{
		"asset_type": "实验",
		"payload":    map[string]any{"指标": []string{"完整性", "可执行性"}, "结果": "待运行"},
	}), "id", created.ID))
	if updateW.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateW.Code, updateW.Body.String())
	}
	var updated PromptEvaluationAssetResponse
	if err := json.Unmarshal(updateW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.AssetType != "实验" || updated.PromptID == nil || *updated.PromptID != promptID {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestRunPromptEvaluationAssetWritesChineseResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"澄清渲染提示词",
		"请澄清 {{issue_title}}，仓库是 {{repo}}。",
		`[{"name":"repo","default_value":"user-center"}]`,
	)

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "澄清渲染测试套件",
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{
				{
					"名称":   "登录失败澄清",
					"变量":   map[string]any{"issue_title": "登录失败"},
					"期望包含": []string{"登录失败", "user-center"},
				},
			},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/run", nil), "id", created.ID))
	if runW.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var ran PromptEvaluationAssetResponse
	if err := json.Unmarshal(runW.Body.Bytes(), &ran); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	payload, ok := ran.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", ran.Payload)
	}
	recent, ok := payload["最近运行"].(map[string]any)
	if !ok {
		t.Fatalf("missing 最近运行 in payload: %#v", payload)
	}
	if recent["总用例数"] != float64(1) || recent["通过用例数"] != float64(1) || recent["缺失变量数"] != float64(0) {
		t.Fatalf("unexpected run metrics: %#v", recent)
	}
	if recent["通过率"] != float64(1) || recent["执行Agent"] != "本地提示词渲染器" || recent["模型"] != "本地模板渲染" || recent["runtime"] != "server" {
		t.Fatalf("missing production metrics: %#v", recent)
	}
	if recent["trace/task id"] != "未创建 Agent 任务" || recent["评估结论"] != "通过" {
		t.Fatalf("unexpected trace/conclusion: %#v", recent)
	}
	results := recent["用例结果"].([]any)
	first := results[0].(map[string]any)
	if first["渲染提示词"] != "请澄清 登录失败，仓库是 user-center。" {
		t.Fatalf("rendered prompt = %q", first["渲染提示词"])
	}
}

func TestRunPromptEvaluationAssetReadsDatasetPayload(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"数据集渲染提示词",
		"项目 {{project}} 需要澄清 {{issue_title}}。",
		`[]`,
	)

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "中文数据集可运行",
		"asset_type": "数据集",
		"payload": map[string]any{
			"schema_version": 1,
			"语义版本":           "multica.training_evaluation.v1",
			"数据集": []map[string]any{
				{
					"名称":   "中文键数据集用例",
					"变量":   map[string]any{"project": "user-center", "issue_title": "登录失败"},
					"期望包含": []string{"user-center", "登录失败"},
				},
			},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/run", nil), "id", created.ID))
	if runW.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var ran PromptEvaluationAssetResponse
	if err := json.Unmarshal(runW.Body.Bytes(), &ran); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	payload := ran.Payload.(map[string]any)
	recent := payload["最近运行"].(map[string]any)
	if recent["总用例数"] != float64(1) || recent["通过用例数"] != float64(1) || recent["通过率"] != float64(1) {
		t.Fatalf("dataset metrics = %#v", recent)
	}
}

func TestRunPromptEvaluationAssetAgentQueuesChatTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM task_usage WHERE task_id IN (
				SELECT atq.id FROM agent_task_queue atq JOIN agent a ON a.id = atq.agent_id
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估 Agent'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM task_message WHERE task_id IN (
				SELECT atq.id FROM agent_task_queue atq JOIN agent a ON a.id = atq.agent_id
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估 Agent'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM chat_message WHERE chat_session_id IN (
				SELECT cs.id FROM chat_session cs JOIN agent a ON a.id = cs.agent_id
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估 Agent'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_task_queue WHERE agent_id IN (
				SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估 Agent'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM chat_session WHERE agent_id IN (
				SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估 Agent'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估 Agent'`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codebuddy' AND name LIKE 'prompt-eval-codebuddy-%'`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})

	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 测试运行时', '{}'::jsonb, $4, 'private', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codebuddy-daemon-"+randomID()[:8], "prompt-eval-codebuddy-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}

	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"Agent 真实运行提示词",
		"请评估 {{issue_title}}。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "真实 Agent 运行实验",
		"asset_type": "实验",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "登录失败", "变量": map[string]any{"issue_title": "登录失败"}, "期望包含": []string{"登录失败"}}},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAssetAgent(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/agent-run", nil), "id", created.ID))
	if runW.Code != http.StatusAccepted {
		t.Fatalf("agent run status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var resp PromptEvaluationAgentRunResponse
	if err := json.Unmarshal(runW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode agent run response: %v", err)
	}
	if resp.TaskID == "" || resp.ChatSessionID == "" || resp.AgentID == "" || resp.RuntimeID != runtimeID || resp.Model != "minimax-m2.7-ioa" {
		t.Fatalf("agent run response = %+v, runtimeID=%s", resp, runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["trace/task id"] != resp.TaskID || recent["状态"] != "已入队" || recent["评估结论"] != "等待 Agent 执行完成" {
		t.Fatalf("recent agent run = %#v", recent)
	}
}

func TestPromptEvaluationAssetRejectsForeignPrompt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	foreignWorkspaceID := createPromptLibraryTestWorkspace(t, "prompt-eval-foreign-"+randomID()[:8])
	foreignPromptID := createPromptEvaluationTestPrompt(t, foreignWorkspaceID, "外部提示词")

	w := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(w, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  foreignPromptID,
		"name":       "跨空间数据集",
		"asset_type": "数据集",
		"payload":    map[string]any{},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func createPromptEvaluationTestPrompt(t *testing.T, workspaceID, name string) string {
	t.Helper()
	return createPromptEvaluationTestPromptWithContent(t, workspaceID, name, "请澄清问题。", `[]`)
}

func createPromptEvaluationTestPromptWithContent(t *testing.T, workspaceID, name, content, variables string) string {
	t.Helper()
	var promptID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO prompt_library_item (workspace_id, name, description, prompt_type, content, variables, tags, status, created_by)
		VALUES ($1, $2, '', '需求澄清', $3, $4::jsonb, '[]'::jsonb, '启用', $5)
		RETURNING id
	`, workspaceID, name, content, variables, testUserID).Scan(&promptID); err != nil {
		t.Fatalf("create prompt: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id = $1`, promptID)
	})
	return promptID
}
