package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
	var caseName string
	var caseCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)::int, max(case_name)
		FROM prompt_evaluation_case
		WHERE workspace_id = $1 AND asset_id = $2
	`, testWorkspaceID, created.ID).Scan(&caseCount, &caseName); err != nil {
		t.Fatalf("load structured cases: %v", err)
	}
	if caseCount != 1 {
		t.Fatalf("structured case count = %d, name=%q", caseCount, caseName)
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
	casesW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(casesW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+created.ID, nil))
	if casesW.Code != http.StatusOK {
		t.Fatalf("list cases status = %d, body = %s", casesW.Code, casesW.Body.String())
	}
	var casesResp struct {
		Items []PromptEvaluationCaseResponse `json:"items"`
		Total int                            `json:"total"`
	}
	if err := json.Unmarshal(casesW.Body.Bytes(), &casesResp); err != nil {
		t.Fatalf("decode cases response: %v", err)
	}
	if casesResp.Total != 1 || casesResp.Items[0].AssetID != created.ID || casesResp.Items[0].Status != "启用" {
		t.Fatalf("cases response = %+v", casesResp)
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
	if recent["trace/task id"] == "" || recent["评估结论"] != "通过" {
		t.Fatalf("unexpected trace/conclusion: %#v", recent)
	}
	results := recent["用例结果"].([]any)
	first := results[0].(map[string]any)
	if first["渲染提示词"] != "请澄清 登录失败，仓库是 user-center。" {
		t.Fatalf("rendered prompt = %q", first["渲染提示词"])
	}
	var runID, runStatus, runKind, trialStatus, renderedPrompt string
	if err := testPool.QueryRow(context.Background(), `
		SELECT r.id::text, r.status, r.run_kind, t.status, t.rendered_prompt
		FROM prompt_evaluation_run r
		JOIN prompt_evaluation_trial t ON t.run_id = r.id
		WHERE r.workspace_id = $1 AND r.asset_id = $2
		ORDER BY r.created_at DESC, t.case_index ASC
		LIMIT 1
	`, testWorkspaceID, created.ID).Scan(&runID, &runStatus, &runKind, &trialStatus, &renderedPrompt); err != nil {
		t.Fatalf("load structured prompt evaluation run: %v", err)
	}
	if recent["trace/task id"] != runID || runStatus != "通过" || runKind != "本地渲染" || trialStatus != "通过" {
		t.Fatalf("structured run mismatch: runID=%s status=%s kind=%s trial=%s recent=%#v", runID, runStatus, runKind, trialStatus, recent)
	}
	if renderedPrompt != "请澄清 登录失败，仓库是 user-center。" {
		t.Fatalf("structured trial rendered prompt = %q", renderedPrompt)
	}

	summaryW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationSummary(summaryW, newRequest(http.MethodGet, "/api/prompt-evaluation-summary", nil))
	if summaryW.Code != http.StatusOK {
		t.Fatalf("summary status = %d, body = %s", summaryW.Code, summaryW.Body.String())
	}
	var summary PromptEvaluationSummaryResponse
	if err := json.Unmarshal(summaryW.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if summary.RunStatus["运行总数"] < 1 || summary.RunStatus["通过"] < 1 {
		t.Fatalf("summary run status = %#v", summary.RunStatus)
	}
	if _, ok := summary.RunStatus["需人工复核"]; !ok {
		t.Fatalf("summary missing manual review status: %#v", summary.RunStatus)
	}
	if summary.Assets["测试套件"] < 1 || summary.Assets["结构化用例"] < 1 {
		t.Fatalf("summary assets = %#v", summary.Assets)
	}
	passRate, _ := summary.Metrics["通过率"].(float64)
	if summary.Metrics["通过数"].(float64) < 1 || passRate < 0 || passRate > 1 || summary.Metrics["本地运行数"].(float64) < 1 {
		t.Fatalf("summary metrics = %#v", summary.Metrics)
	}
	if _, ok := summary.Metrics["需人工复核"]; !ok {
		t.Fatalf("summary missing manual review metric: %#v", summary.Metrics)
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

func TestPromptEvaluationCaseCRUD(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "评测用例 CRUD 提示词", "请处理 {{issue_title}}。", `[]`)
	createAssetW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createAssetW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "评测用例 CRUD 数据集",
		"asset_type": "数据集",
		"payload":    map[string]any{"cases": []any{}},
	}))
	if createAssetW.Code != http.StatusCreated {
		t.Fatalf("create asset status = %d, body = %s", createAssetW.Code, createAssetW.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createAssetW.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}

	createCaseW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationCase(createCaseW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases", map[string]any{
		"asset_id":          asset.ID,
		"case_name":         "登录失败需要 trace",
		"variables":         map[string]any{"issue_title": "登录失败"},
		"expected_contains": []string{"trace/task id", "验收条件"},
		"tags":              []string{"手工用例", "user-center"},
		"status":            "启用",
	}))
	if createCaseW.Code != http.StatusCreated {
		t.Fatalf("create case status = %d, body = %s", createCaseW.Code, createCaseW.Body.String())
	}
	var created PromptEvaluationCaseResponse
	if err := json.Unmarshal(createCaseW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created case: %v", err)
	}
	if created.Source != "manual" || created.CaseIndex != 0 || created.CaseName != "登录失败需要 trace" {
		t.Fatalf("created case = %+v", created)
	}
	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/run", nil), "id", asset.ID))
	if runW.Code != http.StatusOK {
		t.Fatalf("run asset with manual case status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var trialCaseName string
	if err := testPool.QueryRow(context.Background(), `
		SELECT t.case_name
		FROM prompt_evaluation_trial t
		JOIN prompt_evaluation_run r ON r.id = t.run_id
		WHERE r.asset_id = $1
		ORDER BY r.created_at DESC, t.case_index ASC
		LIMIT 1
	`, asset.ID).Scan(&trialCaseName); err != nil {
		t.Fatalf("load manual case trial: %v", err)
	}
	if trialCaseName != "登录失败需要 trace" {
		t.Fatalf("manual structured case was not used by run, got %q", trialCaseName)
	}

	updateCaseW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationCase(updateCaseW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-cases/"+created.ID, map[string]any{
		"case_name":         "登录失败需要可观测证据",
		"expected_contains": []string{"可观测证据"},
		"status":            "归档",
	}), "id", created.ID))
	if updateCaseW.Code != http.StatusOK {
		t.Fatalf("update case status = %d, body = %s", updateCaseW.Code, updateCaseW.Body.String())
	}
	var updated PromptEvaluationCaseResponse
	if err := json.Unmarshal(updateCaseW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated case: %v", err)
	}
	if updated.CaseName != "登录失败需要可观测证据" || updated.Status != "归档" || updated.Source != "manual" {
		t.Fatalf("updated case = %+v", updated)
	}

	listW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(listW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID, nil))
	if listW.Code != http.StatusOK {
		t.Fatalf("list case status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Items []PromptEvaluationCaseResponse `json:"items"`
		Total int                            `json:"total"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed cases: %v", err)
	}
	if listed.Total != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("listed cases = %+v", listed)
	}

	deleteCaseW := httptest.NewRecorder()
	testHandler.DeletePromptEvaluationCase(deleteCaseW, withURLParam(newRequest(http.MethodDelete, "/api/prompt-evaluation-cases/"+created.ID, nil), "id", created.ID))
	if deleteCaseW.Code != http.StatusNoContent {
		t.Fatalf("delete case status = %d, body = %s", deleteCaseW.Code, deleteCaseW.Body.String())
	}
	listAfterDeleteW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(listAfterDeleteW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID, nil))
	if listAfterDeleteW.Code != http.StatusOK {
		t.Fatalf("list after delete status = %d, body = %s", listAfterDeleteW.Code, listAfterDeleteW.Body.String())
	}
	if err := json.Unmarshal(listAfterDeleteW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode cases after delete: %v", err)
	}
	if listed.Total != 0 {
		t.Fatalf("cases after delete = %+v", listed)
	}
}

func TestUpdatePromptEvaluationAssetPreservesManualCases(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "保留人工用例提示词", "请处理 {{issue_title}}。", `[]`)
	createAssetW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createAssetW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "保留人工用例数据集",
		"asset_type": "数据集",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "旧 payload 用例", "变量": map[string]any{"issue_title": "旧问题"}}},
		},
	}))
	if createAssetW.Code != http.StatusCreated {
		t.Fatalf("create asset status = %d, body = %s", createAssetW.Code, createAssetW.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createAssetW.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}

	createCaseW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationCase(createCaseW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases", map[string]any{
		"asset_id":          asset.ID,
		"case_name":         "人工沉淀用例",
		"variables":         map[string]any{"issue_title": "人工问题"},
		"expected_contains": []string{"验收条件"},
		"status":            "启用",
	}))
	if createCaseW.Code != http.StatusCreated {
		t.Fatalf("create manual case status = %d, body = %s", createCaseW.Code, createCaseW.Body.String())
	}
	var manual PromptEvaluationCaseResponse
	if err := json.Unmarshal(createCaseW.Body.Bytes(), &manual); err != nil {
		t.Fatalf("decode manual case: %v", err)
	}

	updateAssetW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationAsset(updateAssetW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-assets/"+asset.ID, map[string]any{
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "新 payload 用例", "变量": map[string]any{"issue_title": "新问题"}}},
		},
	}), "id", asset.ID))
	if updateAssetW.Code != http.StatusOK {
		t.Fatalf("update asset status = %d, body = %s", updateAssetW.Code, updateAssetW.Body.String())
	}

	listW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(listW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID, nil))
	if listW.Code != http.StatusOK {
		t.Fatalf("list cases status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Items []PromptEvaluationCaseResponse `json:"items"`
		Total int                            `json:"total"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed cases: %v", err)
	}
	if listed.Total != 2 {
		t.Fatalf("listed cases = %+v", listed)
	}
	seenManual := false
	seenNewPayload := false
	for _, item := range listed.Items {
		if item.CaseName == "旧 payload 用例" {
			t.Fatalf("old payload case was not replaced: %+v", listed)
		}
		if item.ID == manual.ID {
			seenManual = true
			if item.Source != "manual" || item.CaseName != "人工沉淀用例" {
				t.Fatalf("manual case changed = %+v", item)
			}
		}
		if item.CaseName == "新 payload 用例" {
			seenNewPayload = true
			if item.Source != "payload" || item.CaseIndex <= manual.CaseIndex {
				t.Fatalf("payload case did not avoid manual index = payload %+v manual %+v", item, manual)
			}
		}
	}
	if !seenManual || !seenNewPayload {
		t.Fatalf("expected manual and new payload cases, got %+v", listed)
	}
}

func TestRunPromptEvaluationAssetAgentQueuesChatTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	created, resp, runtimeID := createPromptEvaluationAgentRunFixture(t, "真实 Agent 运行实验", "登录失败")
	if resp.TaskID == "" || resp.ChatSessionID == "" || resp.AgentID == "" || resp.RuntimeID != runtimeID || resp.Model != "minimax-m2.7-ioa" {
		t.Fatalf("agent run response = %+v, runtimeID=%s", resp, runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["trace/task id"] != resp.TaskID || recent["状态"] != "已入队" || recent["评估结论"] != "等待 Agent 执行完成" {
		t.Fatalf("recent agent run = %#v", recent)
	}
	if resp.Run.ID == "" || resp.Run.Status != "已入队" || resp.Run.RunKind != "Agent执行" || resp.Run.TaskID == nil || *resp.Run.TaskID != resp.TaskID {
		t.Fatalf("agent structured run response = %+v", resp.Run)
	}
	var runStatus, runKind, taskID, chatSessionID, trialStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT r.status, r.run_kind, r.task_id::text, r.chat_session_id::text, t.status
		FROM prompt_evaluation_run r
		JOIN prompt_evaluation_trial t ON t.run_id = r.id
		WHERE r.id = $1
		LIMIT 1
	`, resp.Run.ID).Scan(&runStatus, &runKind, &taskID, &chatSessionID, &trialStatus); err != nil {
		t.Fatalf("load queued structured prompt evaluation run: %v", err)
	}
	if runStatus != "已入队" || runKind != "Agent执行" || taskID != resp.TaskID || chatSessionID != resp.ChatSessionID || trialStatus != "待执行" {
		t.Fatalf("queued structured run mismatch: status=%s kind=%s task=%s session=%s trial=%s", runStatus, runKind, taskID, chatSessionID, trialStatus)
	}
	var userMessage string
	if err := testPool.QueryRow(context.Background(), `
		SELECT content FROM chat_message
		WHERE chat_session_id = $1 AND role = 'user'
		ORDER BY created_at ASC
		LIMIT 1
	`, resp.ChatSessionID).Scan(&userMessage); err != nil {
		t.Fatalf("load prompt evaluation agent user message: %v", err)
	}
	if !containsAll(userMessage, []string{"必须返回的 JSON schema", "用例结果", "需人工复核", "Multica 会用它自动回写运行历史"}) {
		t.Fatalf("agent evaluation message missing structured output contract: %s", userMessage)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now() - interval '2 seconds'
		WHERE id = $1
	`, resp.TaskID); err != nil {
		t.Fatalf("start agent task: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
		VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 11, 7, 2, 3, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}
	structuredOutput := `Agent 输出：
` + "```json" + `
{"用例结果":[{"case_index":0,"status":"通过","output":"已覆盖验收条件和 trace/task id","failure_reason":"无","evidence":{"命中":["验收条件","trace/task id"]}}],"评估结论":"Agent 已返回结构化逐用例评估"}
` + "```"
	if _, err := testHandler.Queries.CreateTaskMessage(context.Background(), db.CreateTaskMessageParams{
		TaskID:  parseUUID(resp.TaskID),
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: structuredOutput, Valid: true},
	}); err != nil {
		t.Fatalf("insert task message: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskTraceEvent(context.Background(), db.CreateTaskTraceEventParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		TaskID:        parseUUID(resp.TaskID),
		AgentID:       parseUUID(resp.AgentID),
		RuntimeID:     parseUUID(resp.RuntimeID),
		ChatSessionID: parseUUID(resp.ChatSessionID),
		Source:        "prompt_evaluation",
		EventType:     "llm.usage_reported",
		EventName:     "训练评估用量已上报",
		Status:        "completed",
		Attempt:       1,
		Provider:      "codebuddy",
		Model:         "minimax-m2.7-ioa",
		InputTokens:   16,
		OutputTokens:  7,
		FailureReason: "无",
		ErrorType:     "",
		Metadata:      []byte(`{"阶段":"训练评估"}`),
	}); err != nil {
		t.Fatalf("insert task trace event: %v", err)
	}

	completeW := httptest.NewRecorder()
	completeReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/complete", map[string]any{
		"output":     structuredOutput,
		"session_id": "prompt-eval-session",
		"work_dir":   "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.CompleteTask(completeW, withURLParam(completeReq, "taskId", resp.TaskID))
	if completeW.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completeW.Code, completeW.Body.String())
	}

	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.ID != resp.Run.ID || len(evidence.Trials) != 1 || evidence.Trials[0].CaseName != "登录失败" {
		t.Fatalf("evidence trials = %+v", evidence)
	}
	if evidence.Run.Status != "通过" || evidence.Run.PassedCases != 1 || evidence.Run.FailedCases != 0 || evidence.Run.InputTokens != 16 || evidence.Run.OutputTokens != 7 {
		t.Fatalf("auto-synced run = %+v", evidence.Run)
	}
	if evidence.Run.EstimatedCost <= 0 {
		t.Fatalf("auto-synced run estimated cost = %v, want > 0", evidence.Run.EstimatedCost)
	}
	if evidence.Trials[0].Status != "通过" || evidence.Trials[0].FailureReason != "无" || evidence.Trials[0].InputTokens != 16 || evidence.Trials[0].OutputTokens != 7 {
		t.Fatalf("auto-synced trial = %+v", evidence.Trials[0])
	}
	if len(evidence.TaskUsage) != 1 || evidence.TaskUsage[0].InputTokens != 11 || evidence.TaskUsage[0].OutputTokens != 7 {
		t.Fatalf("evidence usage = %+v", evidence.TaskUsage)
	}
	if !evidence.TaskUsage[0].Priced || evidence.TaskUsage[0].EstimatedCost <= 0 {
		t.Fatalf("evidence usage cost = %+v", evidence.TaskUsage[0])
	}
	if len(evidence.TaskMessages) != 1 || !strings.Contains(evidence.TaskMessages[0].Content, "结构化逐用例评估") {
		t.Fatalf("evidence messages = %+v", evidence.TaskMessages)
	}
	if len(evidence.TraceEvents) != 1 || evidence.TraceEvents[0].EventName != "训练评估用量已上报" || evidence.TraceEvents[0].Metadata["阶段"] != "训练评估" {
		t.Fatalf("evidence traces = %+v", evidence.TraceEvents)
	}

	assetW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationAsset(assetW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+created.ID, nil), "id", created.ID))
	if assetW.Code != http.StatusOK {
		t.Fatalf("asset status = %d, body = %s", assetW.Code, assetW.Body.String())
	}
	var syncedAsset PromptEvaluationAssetResponse
	if err := json.Unmarshal(assetW.Body.Bytes(), &syncedAsset); err != nil {
		t.Fatalf("decode synced asset: %v", err)
	}
	syncedPayload := syncedAsset.Payload.(map[string]any)
	agentRun := syncedPayload["最近Agent运行"].(map[string]any)
	if agentRun["状态"] != "通过" || agentRun["run_id"] != resp.Run.ID || !strings.Contains(stringFromAny(agentRun["评估结论"]), "结构化逐用例评估") {
		t.Fatalf("auto-synced asset agent run = %#v", agentRun)
	}
}

func TestPromptEvaluationAgentModelCanBeConfigured(t *testing.T) {
	t.Setenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL", "")
	if got := promptEvaluationAgentModel(); got != "minimax-m2.7-ioa" {
		t.Fatalf("default prompt evaluation agent model = %q", got)
	}
	t.Setenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL", "hy3-preview-ioa")
	if got := promptEvaluationAgentModel(); got != "hy3-preview-ioa" {
		t.Fatalf("configured prompt evaluation agent model = %q", got)
	}
}

func TestPromptEvaluationRuntimeReadinessRejectsStaleRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codebuddy' AND name LIKE 'prompt-eval-codebuddy-%'`, testWorkspaceID); err != nil {
		t.Fatalf("cleanup codebuddy runtime: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 过期测试运行时', '{}'::jsonb, $4, 'private', now() - interval '5 minutes')
	`, testWorkspaceID, "prompt-eval-codebuddy-stale-"+randomID()[:8], "prompt-eval-codebuddy-stale-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create stale codebuddy runtime: %v", err)
	}

	readinessW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRuntimeReadiness(readinessW, newRequest(http.MethodGet, "/api/prompt-evaluation-runtime-readiness", nil))
	if readinessW.Code != http.StatusOK {
		t.Fatalf("readiness status = %d, body = %s", readinessW.Code, readinessW.Body.String())
	}
	var readiness PromptEvaluationRuntimeReadinessResponse
	if err := json.Unmarshal(readinessW.Body.Bytes(), &readiness); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if readiness.Status != "过期" || readiness.LastSeenAgeSeconds < 120 || readiness.Runtime == nil {
		t.Fatalf("readiness = %+v", readiness)
	}

	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "过期 runtime 提示词", "请评估 {{issue_title}}。", `[]`)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "过期 runtime Agent 实验",
		"asset_type": "实验",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "过期 runtime", "变量": map[string]any{"issue_title": "过期 runtime"}, "期望包含": []string{"过期"}}},
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
	if runW.Code != http.StatusServiceUnavailable || !strings.Contains(runW.Body.String(), "last_seen_at") {
		t.Fatalf("agent run with stale runtime status = %d, body = %s", runW.Code, runW.Body.String())
	}
}

func TestPromptEvaluationRuntimeReadinessReportsUnavailableStates(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codebuddy' AND name LIKE 'prompt-eval-codebuddy-%'`, testWorkspaceID); err != nil {
		t.Fatalf("cleanup codebuddy runtime: %v", err)
	}

	readinessW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRuntimeReadiness(readinessW, newRequest(http.MethodGet, "/api/prompt-evaluation-runtime-readiness", nil))
	if readinessW.Code != http.StatusOK {
		t.Fatalf("missing readiness status = %d, body = %s", readinessW.Code, readinessW.Body.String())
	}
	var missing PromptEvaluationRuntimeReadinessResponse
	if err := json.Unmarshal(readinessW.Body.Bytes(), &missing); err != nil {
		t.Fatalf("decode missing readiness response: %v", err)
	}
	if missing.Status != "缺失" || !strings.Contains(missing.Fix, "启动 multica daemon") || missing.Runtime != nil {
		t.Fatalf("missing readiness = %+v", missing)
	}

	var offlineRuntimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'offline', 'CodeBuddy 离线测试运行时', '{}'::jsonb, $4, 'private', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codebuddy-offline-"+randomID()[:8], "prompt-eval-codebuddy-offline-"+randomID()[:8], testUserID).Scan(&offlineRuntimeID); err != nil {
		t.Fatalf("create offline codebuddy runtime: %v", err)
	}

	offlineW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRuntimeReadiness(offlineW, newRequest(http.MethodGet, "/api/prompt-evaluation-runtime-readiness", nil))
	if offlineW.Code != http.StatusOK {
		t.Fatalf("offline readiness status = %d, body = %s", offlineW.Code, offlineW.Body.String())
	}
	var offline PromptEvaluationRuntimeReadinessResponse
	if err := json.Unmarshal(offlineW.Body.Bytes(), &offline); err != nil {
		t.Fatalf("decode offline readiness response: %v", err)
	}
	if offline.Status != "离线" || offline.Runtime == nil || offline.Runtime.ID != offlineRuntimeID || !strings.Contains(offline.Fix, "启动 multica daemon") {
		t.Fatalf("offline readiness = %+v", offline)
	}

	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "离线 runtime 提示词", "请评估 {{issue_title}}。", `[]`)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "离线 runtime Agent 实验",
		"asset_type": "实验",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "离线 runtime", "变量": map[string]any{"issue_title": "离线 runtime"}, "期望包含": []string{"离线"}}},
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
	if runW.Code != http.StatusServiceUnavailable || !strings.Contains(runW.Body.String(), "启动 multica daemon") {
		t.Fatalf("agent run with offline runtime status = %d, body = %s", runW.Code, runW.Body.String())
	}

	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, offlineRuntimeID); err != nil {
		t.Fatalf("delete offline runtime: %v", err)
	}
	var runtimeOwnerID string
	runtimeOwnerAccount := "prompt-eval-runtime-owner-" + randomID()[:8] + "@multica.test"
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, account)
		VALUES ('Prompt Eval Runtime Owner', $1)
		RETURNING id
	`, runtimeOwnerAccount).Scan(&runtimeOwnerID); err != nil {
		t.Fatalf("create runtime owner: %v", err)
	}
	var plainMemberID string
	plainMemberAccount := "prompt-eval-plain-member-" + randomID()[:8] + "@multica.test"
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, account)
		VALUES ('Prompt Eval Plain Member', $1)
		RETURNING id
	`, plainMemberAccount).Scan(&plainMemberID); err != nil {
		t.Fatalf("create plain member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id IN ($2, $3)`, testWorkspaceID, runtimeOwnerID, plainMemberID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id IN ($1, $2)`, runtimeOwnerID, plainMemberID)
	})
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member'), ($1, $3, 'member')
	`, testWorkspaceID, runtimeOwnerID, plainMemberID); err != nil {
		t.Fatalf("add runtime readiness members: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 私有测试运行时', '{}'::jsonb, $4, 'private', now())
	`, testWorkspaceID, "prompt-eval-codebuddy-private-"+randomID()[:8], "prompt-eval-codebuddy-private-"+randomID()[:8], runtimeOwnerID); err != nil {
		t.Fatalf("create private codebuddy runtime: %v", err)
	}

	noPermissionW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRuntimeReadiness(noPermissionW, newRequestAs(plainMemberID, http.MethodGet, "/api/prompt-evaluation-runtime-readiness", nil))
	if noPermissionW.Code != http.StatusOK {
		t.Fatalf("no permission readiness status = %d, body = %s", noPermissionW.Code, noPermissionW.Body.String())
	}
	var noPermission PromptEvaluationRuntimeReadinessResponse
	if err := json.Unmarshal(noPermissionW.Body.Bytes(), &noPermission); err != nil {
		t.Fatalf("decode no permission readiness response: %v", err)
	}
	if noPermission.Status != "无权限" || !strings.Contains(noPermission.Fix, "runtime 所有者") || noPermission.Runtime != nil {
		t.Fatalf("no permission readiness = %+v", noPermission)
	}
}

func TestRunPromptEvaluationAssetAgentCompletedWithoutStructuredVerdictNeedsReview(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 人工复核实验", "缺少结构化评估")

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now() - interval '1 second'
		WHERE id = $1
	`, resp.TaskID); err != nil {
		t.Fatalf("start agent task: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskMessage(context.Background(), db.CreateTaskMessageParams{
		TaskID:  parseUUID(resp.TaskID),
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: "Agent 输出：我已经完成训练评估。", Valid: true},
	}); err != nil {
		t.Fatalf("insert task message: %v", err)
	}

	completeW := httptest.NewRecorder()
	completeReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/complete", map[string]any{
		"output":     "Agent 输出：我已经完成训练评估。",
		"session_id": "prompt-eval-review-session",
		"work_dir":   "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.CompleteTask(completeW, withURLParam(completeReq, "taskId", resp.TaskID))
	if completeW.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completeW.Code, completeW.Body.String())
	}

	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.Status != "需人工复核" || evidence.Run.PassedCases != 0 || evidence.Run.FailedCases != 1 || evidence.Run.FailureReason != "缺少结构化逐用例评估结果" {
		t.Fatalf("completed run without structured verdict = %+v", evidence.Run)
	}
	if len(evidence.Trials) != 1 || evidence.Trials[0].Status != "需人工复核" || evidence.Trials[0].FailureReason != "缺少结构化逐用例评估结果" {
		t.Fatalf("completed trial without structured verdict = %+v", evidence.Trials)
	}
}

func TestRunPromptEvaluationAssetAgentAutoSyncsFailedTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 失败实验", "部署失败")

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now() - interval '1 second'
		WHERE id = $1
	`, resp.TaskID); err != nil {
		t.Fatalf("start agent task: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
		VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 5, 1, 0, 0, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "Agent 执行超时",
		"failure_reason": "命令超时",
		"session_id":     "prompt-eval-failed-session",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}

	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.Status != "失败" || evidence.Run.PassedCases != 0 || evidence.Run.FailedCases != 1 || evidence.Run.FailureReason != "Agent 执行超时" {
		t.Fatalf("auto-synced failed run = %+v", evidence.Run)
	}
	if len(evidence.Trials) != 1 || evidence.Trials[0].Status != "失败" || evidence.Trials[0].FailureReason != "Agent 执行超时" {
		t.Fatalf("auto-synced failed trial = %+v", evidence.Trials)
	}
}

func TestPromptEvaluationOptimizationCandidateUsesAgentEvidence(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 证据优化实验", "缺少验收条件")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
		VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 13, 8, 1, 2, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskMessage(context.Background(), db.CreateTaskMessageParams{
		TaskID:  parseUUID(resp.TaskID),
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: "Agent 输出：缺少验收条件和 trace/task id，需要补充可观测字段。", Valid: true},
	}); err != nil {
		t.Fatalf("insert task message: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskTraceEvent(context.Background(), db.CreateTaskTraceEventParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		TaskID:        parseUUID(resp.TaskID),
		AgentID:       parseUUID(resp.AgentID),
		RuntimeID:     parseUUID(resp.RuntimeID),
		ChatSessionID: parseUUID(resp.ChatSessionID),
		Source:        "prompt_evaluation",
		EventType:     "evaluation.failed",
		EventName:     "训练评估失败证据",
		Status:        "failed",
		Attempt:       1,
		Provider:      "codebuddy",
		Model:         "minimax-m2.7-ioa",
		InputTokens:   16,
		OutputTokens:  8,
		FailureReason: "缺少验收条件",
		ErrorType:     "assertion_mismatch",
		Metadata:      []byte(`{"缺失字段":"trace/task id"}`),
	}); err != nil {
		t.Fatalf("insert task trace event: %v", err)
	}

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "缺少验收条件",
		"failure_reason": "assertion_mismatch",
		"session_id":     "prompt-eval-evidence-session",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}

	candidateW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationOptimizationCandidate(candidateW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/optimization-candidates", nil), "id", resp.Run.ID))
	if candidateW.Code != http.StatusCreated {
		t.Fatalf("candidate status = %d, body = %s", candidateW.Code, candidateW.Body.String())
	}
	var candidate PromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(candidateW.Body.Bytes(), &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if !containsAll(candidate.CandidateContent, []string{"真实Agent输出摘要", "Agent 输出：缺少验收条件", "训练评估失败证据", "预估成本"}) {
		t.Fatalf("candidate content missing agent evidence: %s", candidate.CandidateContent)
	}
	if !strings.Contains(candidate.Rationale, "真实 Agent task 证据") {
		t.Fatalf("candidate rationale missing agent evidence: %s", candidate.Rationale)
	}
	source := candidate.SourceFailureSummary.(map[string]any)
	runtimeEvidence, ok := source["真实Agent运行证据"].(map[string]any)
	if !ok {
		t.Fatalf("source summary missing runtime evidence: %#v", source)
	}
	if len(runtimeEvidence["task消息"].([]any)) != 1 || len(runtimeEvidence["trace事件"].([]any)) != 1 || len(runtimeEvidence["task用量"].([]any)) != 1 {
		t.Fatalf("runtime evidence incomplete: %#v", runtimeEvidence)
	}
}

func TestRunPromptEvaluationOptimizationAgentQueuesRealTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, sourceResp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 优化任务来源实验", "输出缺少验收条件")
	markPromptEvaluationTaskRunning(t, sourceResp.TaskID)

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+sourceResp.TaskID+"/fail", map[string]any{
		"error":          "输出缺少验收条件",
		"failure_reason": "assertion_mismatch",
		"session_id":     "prompt-eval-source-failed",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", sourceResp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail source status = %d, body = %s", failW.Code, failW.Body.String())
	}

	optW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationOptimizationAgent(optW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+sourceResp.Run.ID+"/optimization-agent-run", nil), "id", sourceResp.Run.ID))
	if optW.Code != http.StatusAccepted {
		t.Fatalf("optimization agent status = %d, body = %s", optW.Code, optW.Body.String())
	}
	var optResp PromptEvaluationAgentRunResponse
	if err := json.Unmarshal(optW.Body.Bytes(), &optResp); err != nil {
		t.Fatalf("decode optimization agent response: %v", err)
	}
	if optResp.TaskID == "" || optResp.TaskID == sourceResp.TaskID || optResp.Run.RunKind != "Agent执行" || optResp.Run.Status != "已入队" {
		t.Fatalf("optimization agent response = %+v", optResp)
	}
	if optResp.Asset.AssetType != "优化运行" || optResp.Asset.PromptID == nil || *optResp.Asset.PromptID != *sourceResp.Run.PromptID {
		t.Fatalf("optimization asset = %+v source=%+v", optResp.Asset, sourceResp.Run)
	}
	payload := optResp.Asset.Payload.(map[string]any)
	if payload["任务类型"] != "Agent 优化运行" || payload["来源运行"] != sourceResp.Run.ID {
		t.Fatalf("optimization payload = %#v", payload)
	}
	if !containsAll(stringFromAny(payload["语义版本"]), []string{"optimization_agent"}) {
		t.Fatalf("optimization payload version = %#v", payload["语义版本"])
	}
	var caseCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)::int FROM prompt_evaluation_case
		WHERE workspace_id = $1 AND asset_id = $2
	`, testWorkspaceID, optResp.Asset.ID).Scan(&caseCount); err != nil {
		t.Fatalf("load optimization cases: %v", err)
	}
	if caseCount == 0 {
		t.Fatalf("expected optimization asset to sync structured cases")
	}

	markPromptEvaluationTaskRunning(t, optResp.TaskID)
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
		VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 21, 13, 1, 2, now())
	`, optResp.TaskID); err != nil {
		t.Fatalf("insert optimization task usage: %v", err)
	}
	optimizationOutput := `Agent 优化输出：
` + "```json" + `
{
  "用例结果":[{"case_index":0,"status":"通过","output":"已生成候选提示词正文","failure_reason":"无","evidence":{"命中":["优化候选","验收条件","trace/task id"]}}],
  "评估结论":"Agent 已生成可人工确认的优化候选",
  "优化候选名称":"Agent 自动优化候选",
  "候选提示词正文":"请澄清 {{issue_title}}，输出必须使用中文，并明确验收条件、trace/task id、失败原因和下一步人工确认点。",
  "逐条修改依据":"补充验收条件、trace/task id 和失败原因，保证领导演示可复盘。",
  "可能影响的通过用例":"需要回归原有中文澄清用例。",
  "人工验收清单":["确认中文输出","确认包含 trace/task id","确认原提示词未被自动替换"]
}
` + "```"
	if _, err := testHandler.Queries.CreateTaskMessage(context.Background(), db.CreateTaskMessageParams{
		TaskID:  parseUUID(optResp.TaskID),
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: optimizationOutput, Valid: true},
	}); err != nil {
		t.Fatalf("insert optimization task message: %v", err)
	}
	if _, err := testHandler.Queries.CreateTaskTraceEvent(context.Background(), db.CreateTaskTraceEventParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		TaskID:        parseUUID(optResp.TaskID),
		AgentID:       parseUUID(optResp.AgentID),
		RuntimeID:     parseUUID(optResp.RuntimeID),
		ChatSessionID: parseUUID(optResp.ChatSessionID),
		Source:        "prompt_evaluation",
		EventType:     "llm.usage_reported",
		EventName:     "Agent 优化候选已生成",
		Status:        "completed",
		Attempt:       1,
		Provider:      "codebuddy",
		Model:         "minimax-m2.7-ioa",
		InputTokens:   21,
		OutputTokens:  13,
		FailureReason: "无",
		ErrorType:     "",
		Metadata:      []byte(`{"阶段":"Agent 优化运行"}`),
	}); err != nil {
		t.Fatalf("insert optimization task trace event: %v", err)
	}

	completeW := httptest.NewRecorder()
	completeReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+optResp.TaskID+"/complete", map[string]any{
		"output":     optimizationOutput,
		"session_id": "prompt-eval-optimization-session",
		"work_dir":   "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.CompleteTask(completeW, withURLParam(completeReq, "taskId", optResp.TaskID))
	if completeW.Code != http.StatusOK {
		t.Fatalf("complete optimization status = %d, body = %s", completeW.Code, completeW.Body.String())
	}

	syncW := httptest.NewRecorder()
	testHandler.SyncPromptEvaluationRunFromTask(syncW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+optResp.Run.ID+"/sync", nil), "id", optResp.Run.ID))
	if syncW.Code != http.StatusOK {
		t.Fatalf("sync optimization status = %d, body = %s", syncW.Code, syncW.Body.String())
	}
	candidates, err := testHandler.Queries.ListPromptEvaluationOptimizationCandidates(context.Background(), db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RunID:       parseUUID(sourceResp.Run.ID),
		PromptID:    parseUUID(*sourceResp.Run.PromptID),
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list optimization candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one auto candidate, got %d", len(candidates))
	}
	candidate := promptEvaluationOptimizationCandidateToResponse(candidates[0])
	if candidate.Status != "待确认" || candidate.FailedCaseCount != 1 || !strings.Contains(candidate.CandidateContent, "请澄清 {{issue_title}}") {
		t.Fatalf("auto optimization candidate = %+v", candidate)
	}
	sourceSummary := candidate.SourceFailureSummary.(map[string]any)
	if sourceSummary["来源Agent优化运行"] != optResp.Run.ID || sourceSummary["来源Agent优化资产"] != optResp.Asset.ID {
		t.Fatalf("auto optimization source summary = %#v", sourceSummary)
	}
	syncAgainW := httptest.NewRecorder()
	testHandler.SyncPromptEvaluationRunFromTask(syncAgainW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+optResp.Run.ID+"/sync", nil), "id", optResp.Run.ID))
	if syncAgainW.Code != http.StatusOK {
		t.Fatalf("sync optimization again status = %d, body = %s", syncAgainW.Code, syncAgainW.Body.String())
	}
	candidates, err = testHandler.Queries.ListPromptEvaluationOptimizationCandidates(context.Background(), db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RunID:       parseUUID(sourceResp.Run.ID),
		PromptID:    parseUUID(*sourceResp.Run.PromptID),
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list optimization candidates after duplicate sync: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected duplicate sync to keep one candidate, got %d", len(candidates))
	}
}

func TestRunPromptEvaluationAssetAgentAutoSyncsCancelledTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 取消实验", "取消任务")

	if _, err := testHandler.TaskService.CancelTask(context.Background(), parseUUID(resp.TaskID)); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.Status != "已取消" || evidence.Run.Conclusion != "Agent 执行已取消" || evidence.Run.FailureReason != "任务被取消" {
		t.Fatalf("auto-synced cancelled run = %+v", evidence.Run)
	}
	if len(evidence.Trials) != 1 || evidence.Trials[0].Status != "已跳过" || evidence.Trials[0].FailureReason != "任务被取消" {
		t.Fatalf("auto-synced cancelled trial = %+v", evidence.Trials)
	}
}

func TestRunPromptEvaluationAssetAgentBatchFailureAutoSyncsTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 批处理失败实验", "批处理失败")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	failed, err := testHandler.Queries.FailAgentTask(context.Background(), db.FailAgentTaskParams{
		ID:            parseUUID(resp.TaskID),
		Error:         pgtype.Text{String: "后台扫描判定任务失败", Valid: true},
		FailureReason: pgtype.Text{String: "agent_error", Valid: true},
	})
	if err != nil {
		t.Fatalf("fail task row: %v", err)
	}
	testHandler.TaskService.HandleFailedTasks(context.Background(), []db.AgentTaskQueue{failed})

	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.Status != "失败" || evidence.Run.FailureReason != "后台扫描判定任务失败" || evidence.Run.TaskID == nil || *evidence.Run.TaskID != resp.TaskID {
		t.Fatalf("auto-synced batch failed run = %+v", evidence.Run)
	}
	if len(evidence.TraceEvents) == 0 {
		t.Fatalf("expected failure trace events, got none")
	}
}

func TestRunPromptEvaluationAssetAgentRetryReassignsRunTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实 Agent 重试实验", "运行时离线")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	failed, err := testHandler.Queries.FailAgentTask(context.Background(), db.FailAgentTaskParams{
		ID:            parseUUID(resp.TaskID),
		Error:         pgtype.Text{String: "运行时离线，自动重试", Valid: true},
		FailureReason: pgtype.Text{String: "runtime_offline", Valid: true},
	})
	if err != nil {
		t.Fatalf("fail task row: %v", err)
	}
	retried := testHandler.TaskService.HandleFailedTasks(context.Background(), []db.AgentTaskQueue{failed})
	if retried != 1 {
		t.Fatalf("expected one retry, got %d", retried)
	}

	var childTaskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id::text FROM agent_task_queue
		WHERE parent_task_id = $1
	`, resp.TaskID).Scan(&childTaskID); err != nil {
		t.Fatalf("load retry child: %v", err)
	}
	evidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(evidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if evidenceW.Code != http.StatusOK {
		t.Fatalf("evidence status = %d, body = %s", evidenceW.Code, evidenceW.Body.String())
	}
	var evidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(evidenceW.Body.Bytes(), &evidence); err != nil {
		t.Fatalf("decode evidence response: %v", err)
	}
	if evidence.Run.Status != "已入队" || evidence.Run.TaskID == nil || *evidence.Run.TaskID != childTaskID {
		t.Fatalf("retry did not reassign prompt evaluation run: child=%s run=%+v", childTaskID, evidence.Run)
	}
}

func cleanupPromptEvaluationAgentRunTest(t *testing.T) {
	t.Helper()
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
}

func markPromptEvaluationTaskRunning(t *testing.T, taskID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now() - interval '1 second'
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("start agent task: %v", err)
	}
}

func createPromptEvaluationAgentRunFixture(t *testing.T, assetName string, caseName string) (PromptEvaluationAssetResponse, PromptEvaluationAgentRunResponse, string) {
	t.Helper()
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
		assetName+"提示词",
		"请评估 {{issue_title}}。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       assetName,
		"asset_type": "实验",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": caseName, "变量": map[string]any{"issue_title": caseName}, "期望包含": []string{caseName}}},
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
	return created, resp, runtimeID
}

func TestPromptEvaluationOptimizationCandidatePublishKeepsSourcePrompt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	sourceContent := "请澄清 {{issue_title}}，输出必须使用中文。"
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"失败用例优化提示词",
		sourceContent,
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "失败用例优化运行",
		"asset_type": "优化运行",
		"payload": map[string]any{
			"cases": []map[string]any{
				{
					"名称":   "缺少验收口径",
					"变量":   map[string]any{"issue_title": "登录失败"},
					"期望包含": []string{"验收条件", "trace/task id"},
				},
			},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/run", nil), "id", asset.ID))
	if runW.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var runID, runStatus string
	var failedCases int
	if err := testPool.QueryRow(context.Background(), `
		SELECT id::text, status, failed_cases
		FROM prompt_evaluation_run
		WHERE workspace_id = $1 AND asset_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, testWorkspaceID, asset.ID).Scan(&runID, &runStatus, &failedCases); err != nil {
		t.Fatalf("load failed run: %v", err)
	}
	if runStatus != "未通过" || failedCases != 1 {
		t.Fatalf("run status=%s failed=%d", runStatus, failedCases)
	}

	candidateW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationOptimizationCandidate(candidateW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+runID+"/optimization-candidates", nil), "id", runID))
	if candidateW.Code != http.StatusCreated {
		t.Fatalf("candidate status = %d, body = %s", candidateW.Code, candidateW.Body.String())
	}
	var candidate PromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(candidateW.Body.Bytes(), &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if candidate.Status != "待确认" || candidate.FailedCaseCount != 1 || candidate.PromptID != promptID {
		t.Fatalf("candidate = %+v", candidate)
	}
	if candidate.CandidateContent == sourceContent || !containsAll(candidate.CandidateContent, []string{"优化候选", "失败用例", "人工发布要求"}) {
		t.Fatalf("candidate content did not include optimization guardrails: %s", candidate.CandidateContent)
	}

	publishW := httptest.NewRecorder()
	testHandler.PublishPromptEvaluationOptimizationCandidate(publishW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidate.ID+"/publish", nil), "id", candidate.ID))
	if publishW.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", publishW.Code, publishW.Body.String())
	}
	var published PublishPromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(publishW.Body.Bytes(), &published); err != nil {
		t.Fatalf("decode publish: %v", err)
	}
	if published.Candidate.Status != "已发布" || published.Candidate.PublishedPromptID == nil || *published.Candidate.PublishedPromptID != published.Prompt.ID {
		t.Fatalf("published candidate = %+v prompt=%+v", published.Candidate, published.Prompt)
	}
	if published.Prompt.ID == promptID || published.Prompt.Version != 2 || published.Prompt.Content != candidate.CandidateContent {
		t.Fatalf("published prompt = %+v", published.Prompt)
	}
	var originalContent string
	var originalVersion int
	if err := testPool.QueryRow(context.Background(), `
		SELECT content, version
		FROM prompt_library_item
		WHERE id = $1
	`, promptID).Scan(&originalContent, &originalVersion); err != nil {
		t.Fatalf("load original prompt: %v", err)
	}
	if originalContent != sourceContent || originalVersion != 1 {
		t.Fatalf("original prompt was changed: version=%d content=%s", originalVersion, originalContent)
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

func containsAll(value string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
