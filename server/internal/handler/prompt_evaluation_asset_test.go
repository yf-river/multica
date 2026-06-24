package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestBuildPromptEvaluationExecutionEvidencePairsToolCalls(t *testing.T) {
	run := PromptEvaluationRunResponse{
		ID:              "run-1",
		RunKind:         "Agent执行",
		Status:          "通过",
		TriggerSource:   "智能体调试场",
		RuntimeProvider: "codex",
		Model:           "gpt-5.3-codex-spark",
		TotalCases:      1,
		PassedCases:     1,
		FailedCases:     0,
		InputTokens:     10,
		OutputTokens:    5,
		CreatedAt:       "2026-06-24T00:00:00Z",
	}
	taskID := "task-1"
	run.TaskID = &taskID
	messages := []protocol.TaskMessagePayload{
		{
			TaskID:    taskID,
			Seq:       1,
			Type:      "tool_use",
			Tool:      "shell",
			Input:     map[string]any{"tool_call_id": "call-1", "cmd": "echo ok"},
			CreatedAt: "2026-06-24T00:00:01Z",
		},
		{
			TaskID:    taskID,
			Seq:       2,
			Type:      "tool_result",
			Tool:      "shell",
			Output:    "ok",
			CreatedAt: "2026-06-24T00:00:02Z",
		},
		{
			TaskID: taskID,
			Seq:    3,
			Type:   "tool_result",
			Tool:   "browser",
			Output: "orphan",
		},
		{
			TaskID:    taskID,
			Seq:       4,
			Type:      "tool_use",
			Tool:      "curl",
			Input:     map[string]any{"tool_call_id": "call-2", "url": "https://example.test"},
			CreatedAt: "2026-06-24T00:00:03Z",
		},
		{
			TaskID:    taskID,
			Seq:       5,
			Type:      "tool_result",
			Tool:      "curl",
			Output:    "Error: HTTP 500 from upstream",
			CreatedAt: "2026-06-24T00:00:05Z",
		},
	}

	spans, chains, toolSummary, summary := buildPromptEvaluationExecutionEvidence(run, nil, messages, nil)
	if len(chains) != 3 {
		t.Fatalf("tool call chains = %+v, want 3", chains)
	}
	if chains[0].ID != "tool:call-1" || chains[0].Status != "已配对" || chains[0].UseSeq != 1 || chains[0].ResultSeq != 2 || chains[0].Output != "ok" {
		t.Fatalf("paired chain = %+v", chains[0])
	}
	if chains[0].DurationMs != 1000 || chains[0].ResultCategory != "已返回" {
		t.Fatalf("paired chain timing/category = %+v", chains[0])
	}
	if chains[1].Status != "孤立结果" || chains[1].ResultSeq != 3 {
		t.Fatalf("orphan chain = %+v", chains[1])
	}
	if chains[1].ResultCategory != "孤立返回" {
		t.Fatalf("orphan chain category = %+v", chains[1])
	}
	if chains[2].Status != "已配对" || !chains[2].FailureSignal || chains[2].ResultCategory != "异常线索" || chains[2].FailureReason != "工具结果包含 HTTP 状态码 500" {
		t.Fatalf("failure signal chain = %+v", chains[2])
	}
	if len(toolSummary) != 3 {
		t.Fatalf("tool summary = %+v, want 3 rows", toolSummary)
	}
	if toolSummary[0].Tool != "curl" || !toolSummary[0].NeedsAttention || toolSummary[0].FailureSignalCalls != 1 || toolSummary[0].MaxDurationMs != 2000 {
		t.Fatalf("attention summary row = %+v", toolSummary[0])
	}
	if toolSummary[1].Tool != "browser" || !toolSummary[1].NeedsAttention || toolSummary[1].OrphanResultCalls != 1 {
		t.Fatalf("orphan summary row = %+v", toolSummary[1])
	}
	if toolSummary[2].Tool != "shell" || toolSummary[2].AverageDurationMs != 1000 || toolSummary[2].MaxDurationMs != 1000 || toolSummary[2].SlowestToolCallChainID != "tool:call-1" {
		t.Fatalf("shell summary row = %+v", toolSummary[2])
	}
	if summary["工具调用链数"] != 3 || summary["已配对工具调用数"] != 2 || summary["孤立工具结果数"] != 1 {
		t.Fatalf("tool summary = %+v", summary)
	}
	var useSpan, resultSpan *PromptEvaluationExecutionSpanResponse
	for i := range spans {
		if spans[i].ID == "message:1" {
			useSpan = &spans[i]
		}
		if spans[i].ID == "message:2" {
			resultSpan = &spans[i]
		}
	}
	if useSpan == nil || resultSpan == nil {
		t.Fatalf("message spans missing: %+v", spans)
	}
	if useSpan.Details["工具调用链ID"] != "tool:call-1" || resultSpan.Details["工具调用链ID"] != "tool:call-1" {
		t.Fatalf("span chain details: use=%+v result=%+v", useSpan.Details, resultSpan.Details)
	}
}

func TestPromptEvaluationToolFailureSignalExtractsStructuredStatus(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantSignal bool
		wantReason string
	}{
		{
			name:       "http status",
			output:     "Error: HTTP 503 from upstream",
			wantSignal: true,
			wantReason: "工具结果包含 HTTP 状态码 503",
		},
		{
			name:       "exit status",
			output:     "command failed with exit status 2",
			wantSignal: true,
			wantReason: "工具结果包含非零退出码 2",
		},
		{
			name:       "zero exit",
			output:     "command exited with status 0",
			wantSignal: false,
			wantReason: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSignal, gotReason := promptEvaluationToolFailureSignal(tt.output)
			if gotSignal != tt.wantSignal || gotReason != tt.wantReason {
				t.Fatalf("promptEvaluationToolFailureSignal(%q) = (%v, %q), want (%v, %q)", tt.output, gotSignal, gotReason, tt.wantSignal, tt.wantReason)
			}
		})
	}
}

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
	if created.StructureSchema != promptEvaluationAssetProfileV1 ||
		created.StructuredCaseCount != 1 ||
		created.StructuredVariableCount != 0 ||
		created.StructuredAssertionCount != 1 ||
		created.LinkedPromptCount != 1 ||
		created.DatasetRowCount != 1 {
		t.Fatalf("created asset profile = %+v", created)
	}
	assertPromptEvaluationDatasetRows(t, created.ID, []string{"用例 1"})
	createdPayload, ok := created.Payload.(map[string]any)
	if !ok {
		t.Fatalf("created payload is not object: %#v", created.Payload)
	}
	if createdPayload["schema_version"] != float64(1) ||
		createdPayload["schema"] != "multica.training_evaluation.payload.v1" ||
		createdPayload["语义版本"] != "multica.training_evaluation.v1" {
		t.Fatalf("created payload missing contract fields: %#v", createdPayload)
	}
	canonicalCases, ok := createdPayload["cases"].([]any)
	if !ok || len(canonicalCases) != 1 {
		t.Fatalf("created payload cases not canonical: %#v", createdPayload["cases"])
	}
	firstCase, ok := canonicalCases[0].(map[string]any)
	if !ok || firstCase["case_name"] == "" {
		t.Fatalf("created payload case missing stable fields: %#v", canonicalCases[0])
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
		"payload": map[string]any{
			"指标":   []string{"完整性", "可执行性"},
			"实验对象": "评测提示词",
			"对比维度": []string{"命中率", "缺失变量", "中文一致性"},
			"基线输出": "待运行",
		},
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
	if updated.StructuredCaseCount != 1 ||
		updated.EvaluationDimensionCount != 2 ||
		updated.ExperimentDimensionCount != 3 ||
		updated.LinkedPromptCount != 1 ||
		updated.DatasetRowCount != 0 {
		t.Fatalf("updated asset profile = %+v", updated)
	}
	assertPromptEvaluationDatasetRows(t, created.ID, nil)
	assertPromptEvaluationExperimentDimensions(t, created.ID, []string{"命中率", "缺失变量", "中文一致性"})
	updatedPayload, ok := updated.Payload.(map[string]any)
	if !ok || updatedPayload["schema_version"] != float64(1) || updatedPayload["payload_contract"] == nil {
		t.Fatalf("updated payload missing contract: %#v", updated.Payload)
	}
	if _, ok := updatedPayload["cases"].([]any); !ok {
		t.Fatalf("updated payload missing canonical cases: %#v", updatedPayload)
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
	dimensionsW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationExperimentDimensions(dimensionsW, newRequest(http.MethodGet, "/api/prompt-evaluation-experiment-dimensions?asset_id="+created.ID, nil))
	if dimensionsW.Code != http.StatusOK {
		t.Fatalf("list experiment dimensions status = %d, body = %s", dimensionsW.Code, dimensionsW.Body.String())
	}
	var dimensionsResp struct {
		Items []PromptEvaluationExperimentDimensionResponse `json:"items"`
		Total int                                           `json:"total"`
	}
	if err := json.Unmarshal(dimensionsW.Body.Bytes(), &dimensionsResp); err != nil {
		t.Fatalf("decode experiment dimensions response: %v", err)
	}
	if dimensionsResp.Total != 3 || dimensionsResp.Items[0].ExperimentAssetID != created.ID || dimensionsResp.Items[0].ExperimentTarget != "评测提示词" {
		t.Fatalf("experiment dimensions response = %+v", dimensionsResp)
	}
}

func TestPromptEvaluationDatasetFromTraces(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "trace 数据集提示词", "请复盘 {{event_name}}。", `["event_name"]`)
	assetW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(assetW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":   promptID,
		"name":        "trace 导入数据集",
		"description": "从真实任务 trace 沉淀评估样本",
		"asset_type":  "数据集",
		"payload":     map[string]any{"cases": []map[string]any{}},
		"status":      "启用",
	}))
	if assetW.Code != http.StatusCreated {
		t.Fatalf("create asset status = %d, body = %s", assetW.Code, assetW.Body.String())
	}
	var asset PromptEvaluationAssetResponse
	if err := json.Unmarshal(assetW.Body.Bytes(), &asset); err != nil {
		t.Fatalf("decode asset: %v", err)
	}
	agentID := createHandlerTestAgent(t, "trace dataset agent", nil)
	taskID := createHandlerTestTaskForAgent(t, agentID)
	trace, err := testHandler.Queries.CreateTaskTraceEvent(context.Background(), db.CreateTaskTraceEventParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		TaskID:        parseUUID(taskID),
		AgentID:       parseUUID(agentID),
		RuntimeID:     parseUUID(handlerTestRuntimeID(t)),
		Source:        "daemon",
		EventType:     "tool.result",
		EventName:     "接口验收完成",
		Status:        "completed",
		Attempt:       1,
		Provider:      "codex",
		Model:         "gpt-5.3-codex-spark",
		InputTokens:   21,
		OutputTokens:  13,
		FailureReason: "",
		ErrorType:     "",
		DurationMs:    pgtype.Int8{Int64: 1234, Valid: true},
		TotalMs:       pgtype.Int8{Int64: 2234, Valid: true},
		Metadata:      []byte(`{"接口":"/api/usercenter/profile","项目":"usercenter"}`),
	})
	if err != nil {
		t.Fatalf("create trace event: %v", err)
	}

	importW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationDatasetFromTraces(importW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/dataset-from-traces", map[string]any{
		"task_ids":          []string{taskID},
		"event_type":        "tool.result",
		"expected_contains": []string{"接口验收完成", "completed"},
		"tags":              []string{"usercenter", "trace样本"},
	}), "id", asset.ID))
	if importW.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", importW.Code, importW.Body.String())
	}
	var imported PromptEvaluationDatasetFromTracesResponse
	if err := json.Unmarshal(importW.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if imported.Asset.ID != asset.ID || imported.Asset.DatasetRowCount < int32(imported.CreatedCount) || imported.CreatedCount != 1 || len(imported.Cases) != 1 || len(imported.TraceEvents) != 1 {
		t.Fatalf("imported response = %+v", imported)
	}
	if imported.Cases[0].Source != "trace" || imported.Cases[0].CaseName == "" {
		t.Fatalf("imported case = %+v", imported.Cases[0])
	}
	if imported.TraceEvents[0].ID != uuidToString(trace.ID) || imported.TraceEvents[0].TaskID != taskID {
		t.Fatalf("imported trace event = %+v, trace=%s task=%s", imported.TraceEvents[0], uuidToString(trace.ID), taskID)
	}
	assertPromptEvaluationDatasetRowsContain(t, asset.ID, []string{imported.Cases[0].CaseName})
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
	if recent["通过率"] != float64(1) || recent["执行Agent"] != "本地提示词渲染器" || recent["模型"] != "本地模板渲染检查" || recent["runtime"] != "server" {
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

	snapshotW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationEvidenceSnapshot(snapshotW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+runID+"/evidence-snapshots?snapshot_type=验收归档", nil), "id", runID))
	if snapshotW.Code != http.StatusCreated {
		t.Fatalf("create summary snapshot status = %d, body = %s", snapshotW.Code, snapshotW.Body.String())
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
	if summary.Metrics["通过数"].(float64) < 1 || passRate < 0 || passRate > 1 || summary.Metrics["模板渲染检查数"].(float64) < 1 {
		t.Fatalf("summary metrics = %#v", summary.Metrics)
	}
	if _, ok := summary.Metrics["需人工复核"]; !ok {
		t.Fatalf("summary missing manual review metric: %#v", summary.Metrics)
	}
	if summary.Metrics["服务端证据快照"].(float64) < 1 || summary.Metrics["验收归档快照"].(float64) < 1 {
		t.Fatalf("summary missing evidence snapshots: %#v", summary.Metrics)
	}
	if summary.Assets["服务端证据快照"] < 1 || summary.Assets["验收归档快照"] < 1 {
		t.Fatalf("summary assets missing evidence snapshots: %#v", summary.Assets)
	}
	if _, ok := summary.Assets["实验维度事实"]; !ok {
		t.Fatalf("summary missing experiment dimension fact metric: %#v", summary.Assets)
	}

	futureSince := url.QueryEscape(time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339))
	windowW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationSummary(windowW, newRequest(http.MethodGet, "/api/prompt-evaluation-summary?since="+futureSince, nil))
	if windowW.Code != http.StatusOK {
		t.Fatalf("window summary status = %d, body = %s", windowW.Code, windowW.Body.String())
	}
	var windowSummary PromptEvaluationSummaryResponse
	if err := json.Unmarshal(windowW.Body.Bytes(), &windowSummary); err != nil {
		t.Fatalf("decode window summary response: %v", err)
	}
	if windowSummary.RunStatus["运行总数"] != 0 || windowSummary.Metrics["通过数"].(float64) != 0 || windowSummary.Metrics["输入token"].(float64) != 0 {
		t.Fatalf("window summary should filter run metrics, got status=%#v metrics=%#v", windowSummary.RunStatus, windowSummary.Metrics)
	}
	if windowSummary.Metrics["服务端证据快照"].(float64) != 0 || windowSummary.Assets["服务端证据快照"] != 0 {
		t.Fatalf("window summary should filter evidence snapshots, got metrics=%#v assets=%#v", windowSummary.Metrics, windowSummary.Assets)
	}
	if windowSummary.Assets["测试套件"] < 1 || windowSummary.Assets["结构化用例"] < 1 {
		t.Fatalf("window summary should keep asset inventory, got assets=%#v", windowSummary.Assets)
	}
}

func TestGetPromptEvaluationSummaryCanExcludeAcceptanceFixtures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
	})

	var businessAssetID, acceptanceAssetID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, description, asset_type, payload, status)
		VALUES ($1, '业务需求评估套件', '日常 usercenter 需求拆解评估', '测试套件', '{"cases":[{"名称":"业务用例"}]}'::jsonb, '启用')
		RETURNING id::text
	`, testWorkspaceID).Scan(&businessAssetID); err != nil {
		t.Fatalf("create business asset: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, description, asset_type, payload, status)
		VALUES ($1, 'goal-test curl 端到端验收套件', '只用于页面验收和 e2e 证据', '测试套件', '{"cases":[{"名称":"验收用例"}]}'::jsonb, '启用')
		RETURNING id::text
	`, testWorkspaceID).Scan(&acceptanceAssetID); err != nil {
		t.Fatalf("create acceptance asset: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO prompt_evaluation_run (workspace_id, asset_id, run_kind, status, total_cases, passed_cases, input_tokens, output_tokens, estimated_cost)
		VALUES
			($1, $2, '本地渲染', '通过', 1, 1, 11, 7, 0.01),
			($1, $3, '本地渲染', '通过', 1, 1, 100, 70, 0.10)
	`, testWorkspaceID, businessAssetID, acceptanceAssetID); err != nil {
		t.Fatalf("create evaluation runs: %v", err)
	}

	allW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationSummary(allW, newRequest(http.MethodGet, "/api/prompt-evaluation-summary", nil))
	if allW.Code != http.StatusOK {
		t.Fatalf("all summary status = %d, body = %s", allW.Code, allW.Body.String())
	}
	var allSummary PromptEvaluationSummaryResponse
	if err := json.Unmarshal(allW.Body.Bytes(), &allSummary); err != nil {
		t.Fatalf("decode all summary: %v", err)
	}
	if allSummary.Assets["资产总数"] != 2 || allSummary.RunStatus["运行总数"] != 2 || allSummary.Metrics["输入token"].(float64) != 111 {
		t.Fatalf("all summary should include acceptance fixtures, assets=%#v status=%#v metrics=%#v", allSummary.Assets, allSummary.RunStatus, allSummary.Metrics)
	}

	businessW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationSummary(businessW, newRequest(http.MethodGet, "/api/prompt-evaluation-summary?include_acceptance_fixtures=false", nil))
	if businessW.Code != http.StatusOK {
		t.Fatalf("business summary status = %d, body = %s", businessW.Code, businessW.Body.String())
	}
	var businessSummary PromptEvaluationSummaryResponse
	if err := json.Unmarshal(businessW.Body.Bytes(), &businessSummary); err != nil {
		t.Fatalf("decode business summary: %v", err)
	}
	if businessSummary.Assets["资产总数"] != 1 || businessSummary.RunStatus["运行总数"] != 1 || businessSummary.Metrics["输入token"].(float64) != 11 {
		t.Fatalf("business summary should exclude acceptance fixtures, assets=%#v status=%#v metrics=%#v", businessSummary.Assets, businessSummary.RunStatus, businessSummary.Metrics)
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
	if created.Source != "manual" || created.CaseIndex != 1 || created.CaseName != "登录失败需要 trace" {
		t.Fatalf("created case = %+v", created)
	}
	if len(created.Assertions) != 2 || created.Assertions[0].ExpectedText != "trace/task id" || created.Assertions[1].ExpectedText != "验收条件" {
		t.Fatalf("created assertions = %+v", created.Assertions)
	}
	assertPromptEvaluationCaseAssertions(t, created.ID, []string{"trace/task id", "验收条件"})
	assertPromptEvaluationDatasetRowsContain(t, asset.ID, []string{"登录失败需要 trace"})
	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAsset(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/run", nil), "id", asset.ID))
	if runW.Code != http.StatusOK {
		t.Fatalf("run asset with manual case status = %d, body = %s", runW.Code, runW.Body.String())
	}
	trialRows, err := testPool.Query(context.Background(), `
		WITH latest_run AS (
			SELECT id
			FROM prompt_evaluation_run
			WHERE asset_id = $1
			ORDER BY created_at DESC
			LIMIT 1
		)
		SELECT t.case_name
		FROM prompt_evaluation_trial t
		JOIN latest_run r ON r.id = t.run_id
		ORDER BY t.case_index ASC
	`, asset.ID)
	if err != nil {
		t.Fatalf("load manual case trials: %v", err)
	}
	defer trialRows.Close()
	trialCaseNames := []string{}
	for trialRows.Next() {
		var trialCaseName string
		if err := trialRows.Scan(&trialCaseName); err != nil {
			t.Fatalf("scan manual case trial: %v", err)
		}
		trialCaseNames = append(trialCaseNames, trialCaseName)
	}
	if err := trialRows.Err(); err != nil {
		t.Fatalf("iterate manual case trials: %v", err)
	}
	if !containsAll(strings.Join(trialCaseNames, "\n"), []string{"登录失败需要 trace"}) {
		t.Fatalf("manual structured case was not used by run, got %#v", trialCaseNames)
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
	if len(updated.Assertions) != 1 || updated.Assertions[0].ExpectedText != "可观测证据" || updated.Assertions[0].Status != "归档" {
		t.Fatalf("updated assertions = %+v", updated.Assertions)
	}
	assertPromptEvaluationCaseAssertions(t, created.ID, []string{"可观测证据"})
	assertPromptEvaluationDatasetRowsContain(t, asset.ID, []string{"登录失败需要可观测证据"})

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
	var listedManual *PromptEvaluationCaseResponse
	for idx := range listed.Items {
		if listed.Items[idx].ID == created.ID {
			listedManual = &listed.Items[idx]
			break
		}
	}
	if listed.Total != 2 || listedManual == nil {
		t.Fatalf("listed cases = %+v", listed)
	}
	if len(listedManual.Assertions) != 1 || listedManual.Assertions[0].ExpectedText != "可观测证据" {
		t.Fatalf("listed assertions = %+v", listedManual.Assertions)
	}
	var listedPayload *PromptEvaluationCaseResponse
	for idx := range listed.Items {
		if listed.Items[idx].Source == "payload" {
			listedPayload = &listed.Items[idx]
			break
		}
	}
	if listedPayload == nil {
		t.Fatalf("payload case not found in listed cases = %+v", listed)
	}
	updatePayloadTagsW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationCase(updatePayloadTagsW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-cases/"+listedPayload.ID, map[string]any{
		"tags": []string{"资产载荷", "治理标签"},
	}), "id", listedPayload.ID))
	if updatePayloadTagsW.Code != http.StatusOK {
		t.Fatalf("update payload case tags status = %d, body = %s", updatePayloadTagsW.Code, updatePayloadTagsW.Body.String())
	}
	var updatedPayload PromptEvaluationCaseResponse
	if err := json.Unmarshal(updatePayloadTagsW.Body.Bytes(), &updatedPayload); err != nil {
		t.Fatalf("decode updated payload case: %v", err)
	}
	updatedPayloadTags, err := json.Marshal(updatedPayload.Tags)
	if err != nil {
		t.Fatalf("encode updated payload tags: %v", err)
	}
	if updatedPayload.Source != "payload" || !containsAll(string(updatedPayloadTags), []string{"资产载荷", "治理标签"}) {
		t.Fatalf("updated payload case should preserve source and tags, got %+v", updatedPayload)
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
	if listed.Total != 1 || listed.Items[0].Source != "payload" {
		t.Fatalf("cases after delete = %+v", listed)
	}
	assertPromptEvaluationCaseAssertions(t, created.ID, nil)
	assertPromptEvaluationDatasetRows(t, asset.ID, []string{"默认用例"})

	createSuiteW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createSuiteW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "评测用例 CRUD 测试套件",
		"asset_type": "测试套件",
		"payload":    map[string]any{"cases": []any{}},
	}))
	if createSuiteW.Code != http.StatusCreated {
		t.Fatalf("create test suite status = %d, body = %s", createSuiteW.Code, createSuiteW.Body.String())
	}
	var testSuite PromptEvaluationAssetResponse
	if err := json.Unmarshal(createSuiteW.Body.Bytes(), &testSuite); err != nil {
		t.Fatalf("decode test suite: %v", err)
	}
	createSuiteCaseW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationCase(createSuiteCaseW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases", map[string]any{
		"asset_id":          testSuite.ID,
		"case_name":         "测试套件必须输出通过率",
		"variables":         map[string]any{"issue_title": "登录失败"},
		"expected_contains": []string{"通过率", "失败原因"},
		"tags":              []string{"测试套件", "手工用例"},
	}))
	if createSuiteCaseW.Code != http.StatusCreated {
		t.Fatalf("create test suite case status = %d, body = %s", createSuiteCaseW.Code, createSuiteCaseW.Body.String())
	}
	var suiteCase PromptEvaluationCaseResponse
	if err := json.Unmarshal(createSuiteCaseW.Body.Bytes(), &suiteCase); err != nil {
		t.Fatalf("decode test suite case: %v", err)
	}
	assertPromptEvaluationTestSuiteCasesContain(t, testSuite.ID, []string{"测试套件必须输出通过率"})
	updateSuiteCaseW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationCase(updateSuiteCaseW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-cases/"+suiteCase.ID, map[string]any{
		"case_name": "测试套件必须输出领导证据",
		"status":    "归档",
	}), "id", suiteCase.ID))
	if updateSuiteCaseW.Code != http.StatusOK {
		t.Fatalf("update test suite case status = %d, body = %s", updateSuiteCaseW.Code, updateSuiteCaseW.Body.String())
	}
	assertPromptEvaluationTestSuiteCasesContain(t, testSuite.ID, []string{"测试套件必须输出领导证据"})
	deleteSuiteCaseW := httptest.NewRecorder()
	testHandler.DeletePromptEvaluationCase(deleteSuiteCaseW, withURLParam(newRequest(http.MethodDelete, "/api/prompt-evaluation-cases/"+suiteCase.ID, nil), "id", suiteCase.ID))
	if deleteSuiteCaseW.Code != http.StatusNoContent {
		t.Fatalf("delete test suite case status = %d, body = %s", deleteSuiteCaseW.Code, deleteSuiteCaseW.Body.String())
	}
	assertPromptEvaluationTestSuiteCases(t, testSuite.ID, []string{"默认用例"})
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
			"cases": []map[string]any{{"名称": "旧 payload 用例", "变量": map[string]any{"issue_title": "旧问题"}, "期望包含": []string{"旧 payload 断言"}}},
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
			"cases": []map[string]any{{"名称": "新 payload 用例", "变量": map[string]any{"issue_title": "新问题"}, "期望包含": []string{"新 payload 断言"}}},
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
			if len(item.Assertions) != 1 || item.Assertions[0].ExpectedText != "验收条件" {
				t.Fatalf("manual assertions changed = %+v", item.Assertions)
			}
		}
		if item.CaseName == "新 payload 用例" {
			seenNewPayload = true
			if item.Source != "payload" || item.CaseIndex <= manual.CaseIndex {
				t.Fatalf("payload case did not avoid manual index = payload %+v manual %+v", item, manual)
			}
			if len(item.Assertions) != 1 || item.Assertions[0].ExpectedText != "新 payload 断言" {
				t.Fatalf("payload assertions not refreshed = %+v", item.Assertions)
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
	created, resp, runtimeID := createPromptEvaluationAgentRunFixture(t, "真实智能体运行实验", "登录失败")
	if resp.TaskID == "" || resp.ChatSessionID == "" || resp.AgentID == "" || resp.RuntimeID != runtimeID || resp.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("agent run response = %+v, runtimeID=%s", resp, runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["trace/task id"] != resp.TaskID || recent["状态"] != "已入队" || recent["评估结论"] != "等待智能体执行完成" {
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
	if !containsAll(userMessage, []string{"必须返回的 JSON schema", "multica.training_evaluation.agent_verdict.v1", "case_results", "需人工复核", "Multica 会用它自动回写运行历史"}) {
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
		VALUES ($1, 'codex', 'gpt-5.3-codex-spark', 11, 7, 2, 3, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}
	structuredOutput := `Agent 输出：
` + "```json" + `
{"schema_version":1,"schema":"multica.training_evaluation.agent_verdict.v1","case_results":[{"case_index":0,"status":"通过","output":"已覆盖验收条件和 trace/任务标识","failure_reason":"无","conclusion":"通过","evidence":{"命中":["验收条件","trace/任务标识"]}}],"summary":{"total_cases":1,"passed_cases":1,"failed_cases":0,"failure_reason":"无","conclusion":"Agent 已返回结构化逐用例评估"}}
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
		Provider:      "codex",
		Model:         "gpt-5.3-codex-spark",
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
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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
	hasUsageTrace := false
	for _, trace := range evidence.TraceEvents {
		if trace.EventName == "训练评估用量已上报" && trace.Metadata["阶段"] == "训练评估" {
			hasUsageTrace = true
			break
		}
	}
	if !hasUsageTrace {
		t.Fatalf("evidence traces = %+v", evidence.TraceEvents)
	}
	if len(evidence.ExecutionSpans) == 0 {
		t.Fatalf("evidence execution spans empty")
	}
	hasRootSpan := false
	hasUsageSpan := false
	for _, span := range evidence.ExecutionSpans {
		if span.SpanKind == "任务根节点" && span.SpanName == "评估任务执行" {
			hasRootSpan = true
		}
		if span.SpanKind == "模型用量" && strings.Contains(span.SpanName, "训练评估用量已上报") {
			hasUsageSpan = true
		}
	}
	if !hasRootSpan || !hasUsageSpan {
		t.Fatalf("evidence execution spans = %+v", evidence.ExecutionSpans)
	}
	if evidence.ExecutionSummary["span总数"] == nil || evidence.ExecutionSummary["用量span数"] == nil {
		t.Fatalf("evidence execution summary = %+v", evidence.ExecutionSummary)
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
	if got := promptEvaluationAgentModel(); got != "gpt-5.3-codex-spark" {
		t.Fatalf("default prompt evaluation agent model = %q", got)
	}
	t.Setenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL", "gpt-5.4-mini")
	if got := promptEvaluationAgentModel(); got != "gpt-5.4-mini" {
		t.Fatalf("configured prompt evaluation agent model = %q", got)
	}
}

func TestPromptEvaluationRuntimeReadinessRejectsStaleRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codex' AND name LIKE 'prompt-eval-codex-%'`, testWorkspaceID); err != nil {
		t.Fatalf("cleanup codex runtime: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 过期测试运行时', '{}'::jsonb, $4, 'private', now() - interval '5 minutes')
	`, testWorkspaceID, "prompt-eval-codex-stale-"+randomID()[:8], "prompt-eval-codex-stale-"+randomID()[:8], testUserID); err != nil {
		t.Fatalf("create stale codex runtime: %v", err)
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

func TestPromptEvaluationRuntimeReadinessReportsRecentCapacityFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "容量受限 readiness 实验", "额度不足")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "429 当前无可用Token额度",
		"failure_reason": "agent_error.provider_capacity_or_rate_limit",
		"session_id":     "prompt-eval-capacity-session",
		"work_dir":       "/tmp/prompt-eval-capacity",
	}, testWorkspaceID, "prompt-eval-codex-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
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
	if readiness.Status != "容量受限" || readiness.Runtime == nil || !strings.Contains(readiness.Detail, "429 当前无可用Token额度") {
		t.Fatalf("capacity readiness = %+v", readiness)
	}

	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "容量受限提示词", "请评估 {{issue_title}}。", `[]`)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "容量受限 Agent 实验",
		"asset_type": "实验",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "容量受限", "变量": map[string]any{"issue_title": "容量受限"}, "期望包含": []string{"容量"}}},
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
	if runW.Code != http.StatusServiceUnavailable || !strings.Contains(runW.Body.String(), "429") {
		t.Fatalf("agent run with capacity-limited runtime status = %d, body = %s", runW.Code, runW.Body.String())
	}
}

func TestPromptEvaluationRuntimeReadinessReportsUnavailableStates(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codex' AND name LIKE 'prompt-eval-codex-%'`, testWorkspaceID); err != nil {
		t.Fatalf("cleanup codex runtime: %v", err)
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
	if missing.Status != "缺失" || !strings.Contains(missing.Fix, "启动 multica 守护进程") || missing.Runtime != nil {
		t.Fatalf("missing readiness = %+v", missing)
	}

	var offlineRuntimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'offline', 'Codex 离线测试运行时', '{}'::jsonb, $4, 'private', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codex-offline-"+randomID()[:8], "prompt-eval-codex-offline-"+randomID()[:8], testUserID).Scan(&offlineRuntimeID); err != nil {
		t.Fatalf("create offline codex runtime: %v", err)
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
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 私有测试运行时', '{}'::jsonb, $4, 'private', now())
	`, testWorkspaceID, "prompt-eval-codex-private-"+randomID()[:8], "prompt-eval-codex-private-"+randomID()[:8], runtimeOwnerID); err != nil {
		t.Fatalf("create private codex runtime: %v", err)
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
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体人工复核实验", "缺少结构化评估")

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
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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

	reviewW := httptest.NewRecorder()
	testHandler.ReviewPromptEvaluationRun(reviewW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/review", map[string]any{
		"decision": "通过",
		"note":     "人工确认可作为通过样例",
	}), "id", resp.Run.ID))
	if reviewW.Code != http.StatusOK {
		t.Fatalf("review status = %d, body = %s", reviewW.Code, reviewW.Body.String())
	}
	var reviewed PromptEvaluationRunResponse
	if err := json.Unmarshal(reviewW.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode reviewed run: %v", err)
	}
	if reviewed.Status != "通过" || reviewed.ReviewDecision != "通过" || reviewed.ReviewNote != "人工确认可作为通过样例" || reviewed.ReviewedBy == nil || *reviewed.ReviewedBy != testUserID || reviewed.ReviewedAt == "" {
		t.Fatalf("reviewed run = %+v", reviewed)
	}
	reviewedEvidenceW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationRunEvidence(reviewedEvidenceW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence", nil), "id", resp.Run.ID))
	if reviewedEvidenceW.Code != http.StatusOK {
		t.Fatalf("reviewed evidence status = %d, body = %s", reviewedEvidenceW.Code, reviewedEvidenceW.Body.String())
	}
	var reviewedEvidence PromptEvaluationRunEvidenceResponse
	if err := json.Unmarshal(reviewedEvidenceW.Body.Bytes(), &reviewedEvidence); err != nil {
		t.Fatalf("decode reviewed evidence response: %v", err)
	}
	if len(reviewedEvidence.Trials) != 1 || reviewedEvidence.Trials[0].Status != "通过" || reviewedEvidence.Trials[0].FailureReason != "无" {
		t.Fatalf("reviewed trial = %+v", reviewedEvidence.Trials)
	}
	manualReview, ok := reviewedEvidence.Run.Metrics.(map[string]any)["人工复核"].(map[string]any)
	if !ok || manualReview["处理结果"] != "通过" || manualReview["处理说明"] != "人工确认可作为通过样例" {
		t.Fatalf("manual review metrics = %#v", reviewedEvidence.Run.Metrics)
	}
}

func TestRunPromptEvaluationAssetAgentAutoSyncsFailedTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体失败实验", "部署失败")

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
		VALUES ($1, 'codex', 'gpt-5.3-codex-spark', 5, 1, 0, 0, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "智能体执行超时",
		"failure_reason": "命令超时",
		"session_id":     "prompt-eval-failed-session",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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
	if evidence.Run.Status != "失败" || evidence.Run.PassedCases != 0 || evidence.Run.FailedCases != 1 || evidence.Run.FailureReason != "智能体执行超时" {
		t.Fatalf("auto-synced failed run = %+v", evidence.Run)
	}
	if len(evidence.Trials) != 1 || evidence.Trials[0].Status != "失败" || evidence.Trials[0].FailureReason != "智能体执行超时" {
		t.Fatalf("auto-synced failed trial = %+v", evidence.Trials)
	}
}

func TestPromptEvaluationEvidenceSnapshotArchivesRunEvidence(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "服务端证据快照实验", "需要归档")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "429 当前无可用Token额度",
		"failure_reason": "agent_error.provider_capacity_or_rate_limit",
		"session_id":     "prompt-eval-snapshot-session",
		"work_dir":       "/tmp/prompt-eval-snapshot",
	}, testWorkspaceID, "prompt-eval-codex-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}

	createSnapshotW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationEvidenceSnapshot(createSnapshotW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence-snapshots?snapshot_type=验收归档", nil), "id", resp.Run.ID))
	if createSnapshotW.Code != http.StatusCreated {
		t.Fatalf("create snapshot status = %d, body = %s", createSnapshotW.Code, createSnapshotW.Body.String())
	}
	var snapshot PromptEvaluationEvidenceSnapshotResponse
	if err := json.Unmarshal(createSnapshotW.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.SnapshotType != "验收归档" || snapshot.SchemaVersion != "multica.prompt_evaluation.evidence_snapshot.v1" || snapshot.Evidence == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	summary, ok := snapshot.Summary.(map[string]any)
	if !ok || summary["运行状态"] != "失败" || summary["失败原因"] != "429 当前无可用Token额度" || summary["trace/task id"] != resp.TaskID {
		t.Fatalf("snapshot summary = %#v", snapshot.Summary)
	}
	payload, ok := snapshot.Evidence.(map[string]any)
	if !ok || payload["语义版本"] != "multica.prompt_evaluation.evidence_snapshot.v1" || payload["运行证据"] == nil {
		t.Fatalf("snapshot evidence payload = %#v", snapshot.Evidence)
	}

	listW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationEvidenceSnapshots(listW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence-snapshots", nil), "id", resp.Run.ID))
	if listW.Code != http.StatusOK {
		t.Fatalf("list snapshot status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var listResp struct {
		Items []PromptEvaluationEvidenceSnapshotResponse `json:"items"`
		Total int                                        `json:"total"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode snapshot list: %v", err)
	}
	if listResp.Total != 1 || len(listResp.Items) != 1 || listResp.Items[0].Evidence != nil {
		t.Fatalf("snapshot list = %+v", listResp)
	}

	getW := httptest.NewRecorder()
	req := withURLParams(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/evidence-snapshots/"+snapshot.ID, nil), "id", resp.Run.ID, "snapshotId", snapshot.ID)
	testHandler.GetPromptEvaluationEvidenceSnapshot(getW, req)
	if getW.Code != http.StatusOK {
		t.Fatalf("get snapshot status = %d, body = %s", getW.Code, getW.Body.String())
	}

	mismatchW := httptest.NewRecorder()
	mismatchReq := withURLParams(newRequest(http.MethodGet, "/api/prompt-evaluation-runs/00000000-0000-0000-0000-000000000001/evidence-snapshots/"+snapshot.ID, nil), "id", "00000000-0000-0000-0000-000000000001", "snapshotId", snapshot.ID)
	testHandler.GetPromptEvaluationEvidenceSnapshot(mismatchW, mismatchReq)
	if mismatchW.Code != http.StatusNotFound {
		t.Fatalf("mismatched run snapshot status = %d, body = %s", mismatchW.Code, mismatchW.Body.String())
	}
}

func TestPromptEvaluationOptimizationCandidateUsesAgentEvidence(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体证据优化实验", "缺少验收条件")
	markPromptEvaluationTaskRunning(t, resp.TaskID)

	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
		VALUES ($1, 'codex', 'gpt-5.3-codex-spark', 13, 8, 1, 2, now())
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
		Provider:      "codex",
		Model:         "gpt-5.3-codex-spark",
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
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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
	if !containsAll(candidate.CandidateContent, []string{"真实智能体输出摘要", "Agent 输出：缺少验收条件", "训练评估失败证据", "预估成本"}) {
		t.Fatalf("candidate content missing agent evidence: %s", candidate.CandidateContent)
	}
	if !strings.Contains(candidate.Rationale, "真实智能体 task 证据") {
		t.Fatalf("candidate rationale missing agent evidence: %s", candidate.Rationale)
	}
	source := candidate.SourceFailureSummary.(map[string]any)
	runtimeEvidence, ok := source["真实Agent运行证据"].(map[string]any)
	if !ok {
		t.Fatalf("source summary missing runtime evidence: %#v", source)
	}
	if len(runtimeEvidence["task消息"].([]any)) != 1 || len(runtimeEvidence["trace事件"].([]any)) < 1 || len(runtimeEvidence["task用量"].([]any)) != 1 {
		t.Fatalf("runtime evidence incomplete: %#v", runtimeEvidence)
	}
}

func TestRunPromptEvaluationOptimizationAgentQueuesRealTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, sourceResp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体优化任务来源实验", "输出缺少验收条件")
	markPromptEvaluationTaskRunning(t, sourceResp.TaskID)

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+sourceResp.TaskID+"/fail", map[string]any{
		"error":          "输出缺少验收条件",
		"failure_reason": "assertion_mismatch",
		"session_id":     "prompt-eval-source-failed",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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
	if payload["任务类型"] != "智能体优化运行" || payload["来源运行"] != sourceResp.Run.ID {
		t.Fatalf("optimization payload = %#v", payload)
	}
	if payload["schema"] != "multica.training_evaluation.optimization_run.v2" ||
		payload["语义版本"] != "multica.training_evaluation.optimization_run.v2" {
		t.Fatalf("optimization payload contract version = %#v", payload)
	}
	contract, ok := payload["优化运行契约"].(map[string]any)
	if !ok || !strings.Contains(stringFromAny(contract["重试入口"]), "/agent-run") || stringFromAny(contract["人工确认要求"]) == "" {
		t.Fatalf("optimization contract = %#v", payload["优化运行契约"])
	}
	rounds, ok := payload["优化轮次"].([]any)
	if !ok || len(rounds) != 1 {
		t.Fatalf("optimization rounds = %#v", payload["优化轮次"])
	}
	firstRound, ok := rounds[0].(map[string]any)
	if !ok || intFromAny(firstRound["轮次"]) != 1 || intFromAny(firstRound["重试序号"]) != 0 || firstRound["运行ID"] != optResp.Run.ID || firstRound["trace/task id"] != optResp.TaskID {
		t.Fatalf("first optimization round = %#v", firstRound)
	}
	logs, ok := payload["日志流"].([]any)
	if !ok || len(logs) != 1 {
		t.Fatalf("optimization log stream = %#v", payload["日志流"])
	}
	firstLog, ok := logs[0].(map[string]any)
	if !ok || firstLog["事件"] != "创建优化运行" || intFromAny(firstLog["轮次"]) != 1 {
		t.Fatalf("first optimization log = %#v", firstLog)
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
		VALUES ($1, 'codex', 'gpt-5.3-codex-spark', 21, 13, 1, 2, now())
	`, optResp.TaskID); err != nil {
		t.Fatalf("insert optimization task usage: %v", err)
	}
	optimizationOutput := `智能体优化输出：
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
		EventName:     "智能体优化候选已生成",
		Status:        "completed",
		Attempt:       1,
		Provider:      "codex",
		Model:         "gpt-5.3-codex-spark",
		InputTokens:   21,
		OutputTokens:  13,
		FailureReason: "无",
		ErrorType:     "",
		Metadata:      []byte(`{"阶段":"智能体优化运行"}`),
	}); err != nil {
		t.Fatalf("insert optimization task trace event: %v", err)
	}

	completeW := httptest.NewRecorder()
	completeReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+optResp.TaskID+"/complete", map[string]any{
		"output":     optimizationOutput,
		"session_id": "prompt-eval-optimization-session",
		"work_dir":   "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codex-daemon")
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

func TestPromptEvaluationRequestedAgentIDIgnoresAutoModeLabel(t *testing.T) {
	payload := map[string]any{
		"调试包": map[string]any{
			"执行智能体": nil,
		},
		"运行环境": map[string]any{
			"目标智能体":   "自动选择训练评估智能体",
			"目标智能体标识": nil,
		},
	}
	if got := promptEvaluationRequestedAgentID(payload); got != "" {
		t.Fatalf("requested agent id = %q, want empty auto mode", got)
	}

	explicit := "11111111-1111-4111-8111-111111111111"
	payload["运行环境"] = map[string]any{
		"目标智能体":   "指定执行智能体",
		"目标智能体标识": explicit,
	}
	if got := promptEvaluationRequestedAgentID(payload); got != explicit {
		t.Fatalf("requested agent id = %q, want %q", got, explicit)
	}
}

func TestRunPromptEvaluationAssetAgentAutoSyncsCancelledTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体取消实验", "取消任务")

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
	if evidence.Run.Status != "已取消" || evidence.Run.Conclusion != "智能体执行已取消" || evidence.Run.FailureReason != "任务被取消" {
		t.Fatalf("auto-synced cancelled run = %+v", evidence.Run)
	}
	if len(evidence.Trials) != 1 || evidence.Trials[0].Status != "已跳过" || evidence.Trials[0].FailureReason != "任务被取消" {
		t.Fatalf("auto-synced cancelled trial = %+v", evidence.Trials)
	}
}

func TestCancelPromptEvaluationRunCancelsTaskAndRun(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "公开取消运行实验", "取消公开运行")

	cancelW := httptest.NewRecorder()
	testHandler.CancelPromptEvaluationRun(cancelW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-runs/"+resp.Run.ID+"/cancel", nil), "id", resp.Run.ID))
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel run status = %d, body = %s", cancelW.Code, cancelW.Body.String())
	}
	var cancelled PromptEvaluationRunResponse
	if err := json.Unmarshal(cancelW.Body.Bytes(), &cancelled); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if cancelled.Status != "已取消" || cancelled.TaskID == nil || *cancelled.TaskID != resp.TaskID {
		t.Fatalf("cancelled run response = %+v", cancelled)
	}
	var taskStatus, runStatus, trialStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT q.status, r.status, t.status
		FROM prompt_evaluation_run r
		JOIN agent_task_queue q ON q.id = r.task_id
		JOIN prompt_evaluation_trial t ON t.run_id = r.id
		WHERE r.id = $1
		LIMIT 1
	`, resp.Run.ID).Scan(&taskStatus, &runStatus, &trialStatus); err != nil {
		t.Fatalf("load cancelled run state: %v", err)
	}
	if taskStatus != "cancelled" || runStatus != "已取消" || trialStatus != "已跳过" {
		t.Fatalf("cancel state mismatch: task=%s run=%s trial=%s", taskStatus, runStatus, trialStatus)
	}
}

func TestRunPromptEvaluationAssetAgentBatchFailureAutoSyncsTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体批处理失败实验", "批处理失败")
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
	_, resp, _ := createPromptEvaluationAgentRunFixture(t, "真实智能体重试实验", "运行时离线")
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
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估智能体'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM task_message WHERE task_id IN (
				SELECT atq.id FROM agent_task_queue atq JOIN agent a ON a.id = atq.agent_id
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估智能体'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM chat_message WHERE chat_session_id IN (
				SELECT cs.id FROM chat_session cs JOIN agent a ON a.id = cs.agent_id
				WHERE a.workspace_id = $1 AND a.name = 'Multica 训练评估智能体'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_task_queue WHERE agent_id IN (
				SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估智能体'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM chat_session WHERE agent_id IN (
				SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估智能体'
			)
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE workspace_id = $1 AND name = 'Multica 训练评估智能体'`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'codex' AND name LIKE 'prompt-eval-codex-%'`, testWorkspaceID)
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

func TestRunPromptEvaluationAssetAgentUsesRequestedAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 指定运行时', '{}'::jsonb, $4, 'private', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-selected-daemon-"+randomID()[:8], "prompt-eval-selected-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
	}
	agent, err := testHandler.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Name:               "训练评估指定执行智能体",
		Description:        "用于验证智能体调试场显式选择执行者。",
		RuntimeMode:        "local",
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          parseUUID(runtimeID),
		Visibility:         "workspace",
		MaxConcurrentTasks: 1,
		OwnerID:            parseUUID(testUserID),
		Instructions:       "只输出结构化评估结论。",
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: "gpt-5.4-mini", Valid: true},
	})
	if err != nil {
		t.Fatalf("create selected agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_usage WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id = $1)`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id = $1)`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1)`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE agent_id = $1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agent.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, parseUUID(runtimeID))
	})
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"指定执行智能体提示词",
		"请评估 {{issue_title}}。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "指定执行智能体实验",
		"asset_type": "实验",
		"payload": map[string]any{
			"执行智能体": map[string]any{"agent_id": uuidToString(agent.ID)},
			"cases": []map[string]any{{
				"名称":   "指定执行智能体用例",
				"变量":   map[string]any{"issue_title": "登录失败"},
				"期望包含": []string{"登录失败"},
			}},
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
	if resp.AgentID != uuidToString(agent.ID) || resp.RuntimeID != runtimeID || resp.Model != "gpt-5.4-mini" {
		t.Fatalf("agent run did not use requested agent: resp=%+v agent=%s runtime=%s", resp, uuidToString(agent.ID), runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["agent_id"] != uuidToString(agent.ID) || recent["执行Agent"] != "训练评估指定执行智能体" || recent["模型"] != "gpt-5.4-mini" {
		t.Fatalf("recent agent run did not record requested agent: %#v", recent)
	}
}

func createPromptEvaluationAgentRunFixture(t *testing.T, assetName string, caseName string) (PromptEvaluationAssetResponse, PromptEvaluationAgentRunResponse, string) {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, visibility, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'Codex 测试运行时', '{}'::jsonb, $4, 'private', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codex-daemon-"+randomID()[:8], "prompt-eval-codex-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codex runtime: %v", err)
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

	editedContent := candidate.CandidateContent + "\n\n【人工复核补充】发布前确认保留 trace/task id 和验收条件。"
	updateW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationOptimizationCandidate(updateW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-optimization-candidates/"+candidate.ID, map[string]any{
		"candidate_name":    candidate.CandidateName + " 人工复核版",
		"candidate_content": editedContent,
		"rationale":         candidate.Rationale + " 已由验收者补充生产发布要求。",
		"edit_note":         "补充 trace 和验收条件发布门禁。",
	}), "id", candidate.ID))
	if updateW.Code != http.StatusOK {
		t.Fatalf("update candidate status = %d, body = %s", updateW.Code, updateW.Body.String())
	}
	if err := json.Unmarshal(updateW.Body.Bytes(), &candidate); err != nil {
		t.Fatalf("decode updated candidate: %v", err)
	}
	if candidate.CandidateContent != editedContent || !strings.Contains(candidate.CandidateName, "人工复核版") {
		t.Fatalf("updated candidate = %+v", candidate)
	}
	manualEdit, ok := candidate.Metrics.(map[string]any)["人工编辑"].(map[string]any)
	if !ok || manualEdit["编辑说明"] != "补充 trace 和验收条件发布门禁。" {
		t.Fatalf("manual edit metrics = %#v", candidate.Metrics)
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
	if !strings.Contains(published.Prompt.Content, "人工复核补充") {
		t.Fatalf("published prompt did not use edited candidate content: %s", published.Prompt.Content)
	}
	var publishedVersionSource string
	var publishedVersionCandidateID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT source, source_candidate_id::text
		FROM prompt_library_version
		WHERE prompt_id = $1 AND version = $2
	`, published.Prompt.ID, published.Prompt.Version).Scan(&publishedVersionSource, &publishedVersionCandidateID); err != nil {
		t.Fatalf("load published prompt version: %v", err)
	}
	if publishedVersionSource != "优化候选发布" || publishedVersionCandidateID != candidate.ID {
		t.Fatalf("published version source=%s candidate=%s, want 优化候选发布 %s", publishedVersionSource, publishedVersionCandidateID, candidate.ID)
	}
	updateAfterPublishW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationOptimizationCandidate(updateAfterPublishW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-optimization-candidates/"+candidate.ID, map[string]any{
		"candidate_name":    "不应允许编辑",
		"candidate_content": "发布后不能修改。",
	}), "id", candidate.ID))
	if updateAfterPublishW.Code != http.StatusConflict {
		t.Fatalf("update after publish status = %d, body = %s", updateAfterPublishW.Code, updateAfterPublishW.Body.String())
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

func TestPromptEvaluationOptimizationCandidateCanBeRejected(t *testing.T) {
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
		"拒绝优化候选提示词",
		"请澄清 {{issue_title}}，输出必须使用中文。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "拒绝优化候选运行",
		"asset_type": "优化运行",
		"payload": map[string]any{
			"cases": []map[string]any{
				{
					"名称":   "仍缺少验收口径",
					"变量":   map[string]any{"issue_title": "权限异常"},
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
	var runID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id::text
		FROM prompt_evaluation_run
		WHERE workspace_id = $1 AND asset_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, testWorkspaceID, asset.ID).Scan(&runID); err != nil {
		t.Fatalf("load failed run: %v", err)
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

	rejectW := httptest.NewRecorder()
	testHandler.RejectPromptEvaluationOptimizationCandidate(rejectW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidate.ID+"/reject", map[string]any{
		"reason": "候选没有覆盖原有通过用例。",
	}), "id", candidate.ID))
	if rejectW.Code != http.StatusOK {
		t.Fatalf("reject status = %d, body = %s", rejectW.Code, rejectW.Body.String())
	}
	var rejected PromptEvaluationOptimizationCandidateResponse
	if err := json.Unmarshal(rejectW.Body.Bytes(), &rejected); err != nil {
		t.Fatalf("decode rejected candidate: %v", err)
	}
	if rejected.Status != "已拒绝" {
		t.Fatalf("rejected status = %s, want 已拒绝", rejected.Status)
	}
	manual, ok := rejected.Metrics.(map[string]any)["人工处理"].(map[string]any)
	if !ok || manual["处理结果"] != "已拒绝" || manual["拒绝原因"] != "候选没有覆盖原有通过用例。" {
		t.Fatalf("manual handling metrics = %#v", rejected.Metrics)
	}

	publishW := httptest.NewRecorder()
	testHandler.PublishPromptEvaluationOptimizationCandidate(publishW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-optimization-candidates/"+candidate.ID+"/publish", nil), "id", candidate.ID))
	if publishW.Code != http.StatusConflict {
		t.Fatalf("publish after reject status = %d, body = %s", publishW.Code, publishW.Body.String())
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

func assertPromptEvaluationCaseAssertions(t *testing.T, caseID string, expected []string) {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT expected_text
		FROM prompt_evaluation_case_assertion
		WHERE case_id = $1
		ORDER BY assertion_index ASC
	`, caseID)
	if err != nil {
		t.Fatalf("query case assertions: %v", err)
	}
	defer rows.Close()
	actual := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan case assertion: %v", err)
		}
		actual = append(actual, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate case assertions: %v", err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("case assertions = %#v, want %#v", actual, expected)
	}
	for idx := range expected {
		if actual[idx] != expected[idx] {
			t.Fatalf("case assertions = %#v, want %#v", actual, expected)
		}
	}
}

func assertPromptEvaluationDatasetRows(t *testing.T, assetID string, expected []string) {
	t.Helper()
	actual := loadPromptEvaluationDatasetRows(t, assetID)
	if len(actual) != len(expected) {
		t.Fatalf("dataset rows = %#v, want %#v", actual, expected)
	}
	for idx := range expected {
		if actual[idx] != expected[idx] {
			t.Fatalf("dataset rows = %#v, want %#v", actual, expected)
		}
	}
}

func assertPromptEvaluationDatasetRowsContain(t *testing.T, assetID string, expected []string) {
	t.Helper()
	actual := loadPromptEvaluationDatasetRows(t, assetID)
	seen := map[string]bool{}
	for _, item := range actual {
		seen[item] = true
	}
	for _, item := range expected {
		if !seen[item] {
			t.Fatalf("dataset rows = %#v, want to contain %#v", actual, expected)
		}
	}
}

func loadPromptEvaluationDatasetRows(t *testing.T, assetID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT row_name
		FROM prompt_evaluation_dataset_row
		WHERE dataset_asset_id = $1
		ORDER BY row_index ASC
	`, assetID)
	if err != nil {
		t.Fatalf("query dataset rows: %v", err)
	}
	defer rows.Close()
	actual := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan dataset row: %v", err)
		}
		actual = append(actual, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dataset rows: %v", err)
	}
	return actual
}

func assertPromptEvaluationTestSuiteCases(t *testing.T, assetID string, expected []string) {
	t.Helper()
	actual := loadPromptEvaluationTestSuiteCases(t, assetID)
	if len(actual) != len(expected) {
		t.Fatalf("test suite cases = %#v, want %#v", actual, expected)
	}
	for idx := range expected {
		if actual[idx] != expected[idx] {
			t.Fatalf("test suite cases = %#v, want %#v", actual, expected)
		}
	}
}

func assertPromptEvaluationTestSuiteCasesContain(t *testing.T, assetID string, expected []string) {
	t.Helper()
	actual := loadPromptEvaluationTestSuiteCases(t, assetID)
	seen := map[string]bool{}
	for _, item := range actual {
		seen[item] = true
	}
	for _, item := range expected {
		if !seen[item] {
			t.Fatalf("test suite cases = %#v, want to contain %#v", actual, expected)
		}
	}
}

func loadPromptEvaluationTestSuiteCases(t *testing.T, assetID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT case_name
		FROM prompt_evaluation_test_suite_case
		WHERE test_suite_asset_id = $1
		ORDER BY case_index ASC
	`, assetID)
	if err != nil {
		t.Fatalf("query test suite cases: %v", err)
	}
	defer rows.Close()
	actual := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan test suite case: %v", err)
		}
		actual = append(actual, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate test suite cases: %v", err)
	}
	return actual
}

func assertPromptEvaluationExperimentDimensions(t *testing.T, assetID string, expected []string) {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT dimension_name
		FROM prompt_evaluation_experiment_dimension
		WHERE experiment_asset_id = $1
		ORDER BY dimension_index ASC
	`, assetID)
	if err != nil {
		t.Fatalf("query experiment dimensions: %v", err)
	}
	defer rows.Close()
	actual := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan experiment dimension: %v", err)
		}
		actual = append(actual, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate experiment dimensions: %v", err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("experiment dimensions = %#v, want %#v", actual, expected)
	}
	for idx := range expected {
		if actual[idx] != expected[idx] {
			t.Fatalf("experiment dimensions = %#v, want %#v", actual, expected)
		}
	}
}

func containsAll(value string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
