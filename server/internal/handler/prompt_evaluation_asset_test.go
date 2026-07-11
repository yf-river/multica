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
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestBuildPromptEvaluationExecutionEvidencePairsToolCalls(t *testing.T) {
	run := PromptEvaluationRunResponse{
		ID:              "run-1",
		RunKind:         "Agent执行",
		Status:          "通过",
		TriggerSource:   "智能体调试场",
		RuntimeProvider: "codebuddy",
		Model:           "deepseek-v4-pro-ioa",
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
		tool       string
		output     string
		wantSignal bool
		wantReason string
	}{
		{
			name:       "http status",
			tool:       "curl",
			output:     "Error: HTTP 503 from upstream",
			wantSignal: true,
			wantReason: "工具结果包含 HTTP 状态码 503",
		},
		{
			name:       "exit status",
			tool:       "Bash",
			output:     "command failed with exit status 2",
			wantSignal: true,
			wantReason: "工具结果包含非零退出码 2",
		},
		{
			name:       "zero exit",
			tool:       "Bash",
			output:     "command exited with status 0",
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "successful helm output with failed counter",
			tool: "Bash",
			output: `==> Linting helm/public
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "read source with failure text",
			tool: "Read",
			output: `return biz_err.NewFromCodeWithErr(
				consts.ErrDatabaseUpdateFailed,
				err,
			).WithMsg("密码修改失败").Log(l.Ctx)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "successful bash output containing source failure text",
			tool: "Bash",
			output: `Stdout: consts.ErrDatabaseUpdateFailed
return biz_err.NewFromCodeWithErrMsgDetails(consts.ErrDatabaseUpdateFailed, err, "密码修改失败", "changePWD UpdatesMapById err")

Stderr: (empty)
Exit Code: 0
Signal: (none)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "nonzero bash exit still fails",
			tool:       "Bash",
			output:     "Stdout: ok\nStderr: command failed\nExit Code: 2\nSignal: (none)",
			wantSignal: true,
			wantReason: "工具结果包含非零退出码 2",
		},
		{
			name:       "json text envelope with successful helm failed counter",
			tool:       "Bash",
			output:     `[{"type":"text","text":"Command: helm lint helm/public 2>&1\nStdout: ==> Linting helm/public\n1 chart(s) linted, 0 chart(s) failed\n\nStderr: (empty)\nExit Code: 0\nSignal: (none)"}]`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "successful comment output containing pass zero failed",
			tool:       "Bash",
			output:     `[{"type":"text","text":"Command: multica issue comment add AIS-145 --content-file ./reply.md\nStdout: | helm/public | PASS (0 failed) |\nComment added to issue AIS-145.\nExit Code: 0"}]`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "git diff source failure words are not tool failures",
			tool: "Bash",
			output: `Command: git diff
Stdout: diff --git a/check_rendered_rules.sh b/check_rendered_rules.sh
+fail() {
+  echo "FAIL: missing render output"
+}

Stderr: (empty)
Exit Code: 0
Signal: (none)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "git branch timeout substring is branch content",
			tool: "Bash",
			output: `Command: git branch -a 2>&1
Stdout: * agent/issue/4b46b5a9
  remotes/origin/v2.1.0_qc_timeout
  v2.1.0_qc_timeout

Stderr: (empty)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "read source text with errors is content",
			tool:       "Read",
			output:     `[{"type":"text","text":"var ErrPasswordWeak = errors.New(\"密码强度校验失败\")\nreturn consts.ErrDataProcessFailed"}]`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "grep source text with failures is content",
			tool:       "Grep",
			output:     `[{"type":"text","text":"password_hash_generator_test.go:19:// TestGeneratePasswordHash 生成密码哈希的测试\nresp_code_test.go:42:ErrPasswordExpired"}]`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "read tool use error still fails",
			tool:       "Read",
			output:     `[{"type":"text","text":"<tool_use_error>File does not exist.</tool_use_error>"}]`,
			wantSignal: true,
			wantReason: "工具调用返回错误",
		},
		{
			name: "local artifact curl content is not failed by keywords",
			tool: "Bash",
			output: `Command: curl -s http://localhost:18760/uploads/workspaces/ws/artifact.md 2>&1 | head -80
Stdout: # 验证结果
失败场景：密码过短时应返回业务错误。

Stderr: (empty)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "comment list content is not failed by keywords",
			tool: "Bash",
			output: `Command: multica issue comment list issue-1 --output json 2>&1
Stdout: [{"content":"用户希望确认错误处理与失败用例。"}]

Stderr: (empty)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "task create success with failures path is not failed",
			tool:       "TaskCreate",
			output:     `[{"type":"text","text":"Task #7 created successfully: 读取 harness/testing.md 和 failures.md"}]`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "issue get json content is not failed by keywords",
			tool: "Bash",
			output: `Command: multica issue get IDA-12 --output json 2>&1
Stdout: {"source_summary_error":"","title":"错误处理需求"}

Stderr: (empty)`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name: "metadata set success is not failed by keywords",
			tool: "Bash",
			output: `Command: multica issue metadata set IDA-12 --key pr_number --value 113
Stdout: KEY VALUE TYPE
pr_number 113 number`,
			wantSignal: false,
			wantReason: "",
		},
		{
			name:       "mcp error line still fails",
			tool:       "DeferExecuteTool",
			output:     `[{"type":"text","text":"Error: Unable to resolve Gongfeng merge request id from list endpoint"}]`,
			wantSignal: true,
			wantReason: "工具结果包含错误信息",
		},
		{
			name: "python traceback still fails",
			tool: "Bash",
			output: `Command: python3 -c 'raise RuntimeError("boom")'
Stdout: (empty)
Stderr: Traceback (most recent call last):
  File "<string>", line 1, in <module>
RuntimeError: boom`,
			wantSignal: true,
			wantReason: "工具结果包含异常信息",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSignal, gotReason := promptEvaluationToolFailureSignal(tt.tool, tt.output)
			if gotSignal != tt.wantSignal || gotReason != tt.wantReason {
				t.Fatalf("promptEvaluationToolFailureSignal(%q) = (%v, %q), want (%v, %q)", tt.output, gotSignal, gotReason, tt.wantSignal, tt.wantReason)
			}
		})
	}
}

func TestBuildPromptEvaluationToolCallChainsIgnoresReadSourceFailureText(t *testing.T) {
	messages := []protocol.TaskMessagePayload{
		{
			TaskID:    "task-1",
			Seq:       1,
			Type:      "tool_use",
			Tool:      "Read",
			Input:     map[string]any{"file_path": "internal/logic/change_pwd_logic.go"},
			CreatedAt: "2026-06-24T00:00:01Z",
		},
		{
			TaskID: "task-1",
			Seq:    2,
			Type:   "tool_result",
			Tool:   "Read",
			Output: `consts.ErrDatabaseUpdateFailed
return biz_err.NewFromCodeWithErrMsgDetails(consts.ErrDatabaseUpdateFailed, err, "密码修改失败", "changePWD UpdatesMapById err")`,
			CreatedAt: "2026-06-24T00:00:02Z",
		},
	}

	chains := buildPromptEvaluationToolCallChains(messages)
	if len(chains) != 1 {
		t.Fatalf("tool chains = %+v, want one chain", chains)
	}
	if chains[0].FailureSignal || chains[0].ResultCategory != "已返回" || chains[0].FailureReason != "" {
		t.Fatalf("read source chain = %+v, want normal result", chains[0])
	}

	summary := buildPromptEvaluationToolCallSummary(chains)
	if len(summary) != 1 {
		t.Fatalf("tool summary = %+v, want one row", summary)
	}
	if summary[0].NeedsAttention || summary[0].FailureSignalCalls != 0 {
		t.Fatalf("read source summary = %+v, want no attention", summary[0])
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
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "套件用例", "变量": map[string]any{"输入": "登录失败"}, "期望包含": []string{"边界"}}},
			"通过标准":  []string{"变量完整", "输出中文"},
		},
	}), "id", created.ID))
	if updateW.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateW.Code, updateW.Body.String())
	}
	var updated PromptEvaluationAssetResponse
	if err := json.Unmarshal(updateW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.AssetType != "测试套件" || updated.PromptID == nil || *updated.PromptID != promptID {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.StructuredCaseCount != 1 ||
		updated.LinkedPromptCount != 1 ||
		updated.DatasetRowCount != 0 {
		t.Fatalf("updated asset profile = %+v", updated)
	}
	assertPromptEvaluationDatasetRows(t, created.ID, nil)
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
	partialUpdateW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationAsset(partialUpdateW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-assets/"+created.ID, map[string]any{
		"asset_type": "测试套件",
	}), "id", created.ID))
	if partialUpdateW.Code != http.StatusOK {
		t.Fatalf("partial update status = %d, body = %s", partialUpdateW.Code, partialUpdateW.Body.String())
	}
	var partiallyUpdated PromptEvaluationAssetResponse
	if err := json.Unmarshal(partialUpdateW.Body.Bytes(), &partiallyUpdated); err != nil {
		t.Fatalf("decode partial update response: %v", err)
	}
	if partiallyUpdated.ID != created.ID || partiallyUpdated.AssetType != "测试套件" || partiallyUpdated.StructuredCaseCount != 1 {
		t.Fatalf("partial update did not preserve current asset fields: %+v", partiallyUpdated)
	}
}

func TestPromptEvaluationAssetExperimentDimensionsDoNotBlockCreateOrUpdate(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPrompt(t, testWorkspaceID, "实验维度提示词")

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "实验维度不阻塞创建",
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{{
				"名称":   "维度用例",
				"变量":   map[string]any{"issue_title": "登录失败"},
				"期望包含": []string{"边界"},
			}},
			"实验维度": []string{"命中率", "中文一致性"},
		},
		"status": "启用",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ExperimentDimensionCount != 2 {
		t.Fatalf("created experiment dimension count = %d, want 2", created.ExperimentDimensionCount)
	}

	updateW := httptest.NewRecorder()
	testHandler.UpdatePromptEvaluationAsset(updateW, withURLParam(newRequest(http.MethodPut, "/api/prompt-evaluation-assets/"+created.ID, map[string]any{
		"payload": map[string]any{
			"cases": []map[string]any{{
				"名称":   "更新维度用例",
				"变量":   map[string]any{"issue_title": "登录失败"},
				"期望包含": []string{"边界"},
			}},
			"实验维度": []string{"命中率", "缺失变量", "中文一致性"},
		},
	}), "id", created.ID))
	if updateW.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateW.Code, updateW.Body.String())
	}
	var updated PromptEvaluationAssetResponse
	if err := json.Unmarshal(updateW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.ExperimentDimensionCount != 3 {
		t.Fatalf("updated experiment dimension count = %d, want 3", updated.ExperimentDimensionCount)
	}
}

func TestPromptEvaluationDatasetExportImportProtocol(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE workspace_id = $1`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})
	promptID := createPromptEvaluationTestPrompt(t, testWorkspaceID, "导入导出提示词")

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":   promptID,
		"name":        "完整协议源数据集",
		"description": "验证完整导入导出",
		"asset_type":  "数据集",
		"payload": map[string]any{"cases": []map[string]any{{
			"case_name":         "载荷用例",
			"variables":         map[string]any{"需求": "登录失败"},
			"expected_contains": []string{"边界"},
			"input":             map[string]any{"来源": "payload"},
			"expected":          map[string]any{"结论": "追问"},
			"tags":              []string{"载荷", "协议"},
		}}},
		"status": "启用",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create dataset status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var sourceAsset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &sourceAsset); err != nil {
		t.Fatalf("decode source asset: %v", err)
	}

	createCaseW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationCase(createCaseW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases", map[string]any{
		"asset_id":          sourceAsset.ID,
		"prompt_id":         promptID,
		"case_name":         "手工用例",
		"variables":         map[string]any{"模块": "usercenter"},
		"expected_contains": []string{"验收"},
		"input":             map[string]any{"来源": "manual"},
		"expected":          map[string]any{"结论": "补充验收"},
		"tags":              []string{"手工", "协议"},
		"status":            "启用",
	}))
	if createCaseW.Code != http.StatusCreated {
		t.Fatalf("create manual case status = %d, body = %s", createCaseW.Code, createCaseW.Body.String())
	}

	exportW := httptest.NewRecorder()
	testHandler.ExportPromptEvaluationDataset(exportW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+sourceAsset.ID+"/dataset-export", nil), "id", sourceAsset.ID))
	if exportW.Code != http.StatusOK {
		t.Fatalf("export status = %d, body = %s", exportW.Code, exportW.Body.String())
	}
	var exported PromptEvaluationDatasetExportResponse
	if err := json.Unmarshal(exportW.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode export response: %v", err)
	}
	if exported.Schema != promptEvaluationDatasetExportV1 || exported.CaseCount != 2 || len(exported.Cases) != 2 {
		t.Fatalf("exported protocol = %+v", exported)
	}
	sources := map[string]bool{}
	for _, item := range exported.Cases {
		sources[item.Source] = true
	}
	if !sources["payload"] || !sources["manual"] {
		t.Fatalf("exported sources = %+v", sources)
	}

	importW := httptest.NewRecorder()
	testHandler.ImportPromptEvaluationDataset(importW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets/dataset-import", map[string]any{
		"name":        "完整协议导入副本",
		"description": "由完整协议导入",
		"prompt_id":   promptID,
		"status":      "启用",
		"export":      exported,
	}))
	if importW.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", importW.Code, importW.Body.String())
	}
	var imported ImportPromptEvaluationDatasetResponse
	if err := json.Unmarshal(importW.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if imported.CaseCount != 2 || imported.SourceAssetID != sourceAsset.ID || imported.Asset.AssetType != "数据集" || imported.Asset.DatasetRowCount != 2 {
		t.Fatalf("imported response = %+v", imported)
	}
	if imported.Asset.StructuredCaseCount != 2 || imported.Asset.StructuredAssertionCount != 2 {
		t.Fatalf("imported asset profile = %+v", imported.Asset)
	}
	assertPromptEvaluationDatasetRows(t, imported.Asset.ID, []string{"载荷用例", "手工用例"})

	rows, err := testPool.Query(context.Background(), `
		SELECT source
		FROM prompt_evaluation_case
		WHERE workspace_id = $1 AND asset_id = $2
		ORDER BY case_index ASC, id ASC
	`, testWorkspaceID, imported.Asset.ID)
	if err != nil {
		t.Fatalf("query imported sources: %v", err)
	}
	defer rows.Close()
	var importedSources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			t.Fatalf("scan imported source: %v", err)
		}
		importedSources = append(importedSources, source)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate imported sources: %v", err)
	}
	if len(importedSources) != 2 || importedSources[0] != "payload" || importedSources[1] != "manual" {
		t.Fatalf("imported sources = %#v", importedSources)
	}
	var importedCaseCount, importedAssertionCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*)::int FROM prompt_evaluation_case WHERE workspace_id = $1 AND asset_id = $2),
			(SELECT count(*)::int FROM prompt_evaluation_case_assertion WHERE workspace_id = $1 AND asset_id = $2)
	`, testWorkspaceID, imported.Asset.ID).Scan(&importedCaseCount, &importedAssertionCount); err != nil {
		t.Fatalf("count imported facts: %v", err)
	}
	if importedCaseCount != 2 || importedAssertionCount != 2 {
		t.Fatalf("imported facts case=%d assertion=%d", importedCaseCount, importedAssertionCount)
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
		Provider:      "codebuddy",
		Model:         "deepseek-v4-pro-ioa",
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
			"对比维度": []string{"命中率", "中文一致性"},
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
	assertPromptEvaluationDimensionScores(t, runID, []expectedPromptEvaluationDimensionScore{
		{name: "命中率", status: "已评分", source: "local_run", passed: 1, total: 1},
		{name: "中文一致性", status: "已评分", source: "local_run", passed: 1, total: 1},
	})
	scoresW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationDimensionScores(scoresW, newRequest(http.MethodGet, "/api/prompt-evaluation-dimension-scores?run_id="+runID, nil))
	if scoresW.Code != http.StatusOK {
		t.Fatalf("list dimension scores status = %d, body = %s", scoresW.Code, scoresW.Body.String())
	}
	var scoresResp struct {
		Items []PromptEvaluationDimensionScoreResponse `json:"items"`
		Total int                                      `json:"total"`
	}
	if err := json.Unmarshal(scoresW.Body.Bytes(), &scoresResp); err != nil {
		t.Fatalf("decode dimension scores response: %v", err)
	}
	if scoresResp.Total != 2 || scoresResp.Items[0].RunID != runID || scoresResp.Items[0].Source != "local_run" {
		t.Fatalf("dimension scores response = %+v", scoresResp)
	}
	summaryScoresW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationDimensionScoreSummaries(summaryScoresW, newRequest(http.MethodGet, "/api/prompt-evaluation-dimension-score-summaries?asset_id="+created.ID, nil))
	if summaryScoresW.Code != http.StatusOK {
		t.Fatalf("list dimension score summaries status = %d, body = %s", summaryScoresW.Code, summaryScoresW.Body.String())
	}
	var summaryScoresResp struct {
		Items []PromptEvaluationDimensionScoreSummaryResponse `json:"items"`
		Total int                                             `json:"total"`
	}
	if err := json.Unmarshal(summaryScoresW.Body.Bytes(), &summaryScoresResp); err != nil {
		t.Fatalf("decode dimension score summaries response: %v", err)
	}
	if summaryScoresResp.Total != 2 {
		t.Fatalf("dimension score summaries total = %d, items = %+v", summaryScoresResp.Total, summaryScoresResp.Items)
	}
	if summaryScoresResp.Items[0].RunCount != 1 || summaryScoresResp.Items[0].ScoredRunCount != 1 || summaryScoresResp.Items[0].PassedCases != 1 || summaryScoresResp.Items[0].TotalCases != 1 || summaryScoresResp.Items[0].Score != 1 {
		t.Fatalf("dimension score summary aggregate = %+v", summaryScoresResp.Items[0])
	}
	if summaryScoresResp.Items[0].LatestStatus != "已评分" || summaryScoresResp.Items[0].LatestSource != "local_run" || summaryScoresResp.Items[0].LatestRule == "" || summaryScoresResp.Items[0].LatestEvidence == "" {
		t.Fatalf("dimension score summary latest fields = %+v", summaryScoresResp.Items[0])
	}
	trendScoresW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationDimensionScoreTrends(trendScoresW, newRequest(http.MethodGet, "/api/prompt-evaluation-dimension-score-trends?asset_id="+created.ID, nil))
	if trendScoresW.Code != http.StatusOK {
		t.Fatalf("list dimension score trends status = %d, body = %s", trendScoresW.Code, trendScoresW.Body.String())
	}
	var trendScoresResp struct {
		Items []PromptEvaluationDimensionScoreTrendResponse `json:"items"`
		Total int                                           `json:"total"`
	}
	if err := json.Unmarshal(trendScoresW.Body.Bytes(), &trendScoresResp); err != nil {
		t.Fatalf("decode dimension score trends response: %v", err)
	}
	if trendScoresResp.Total != 2 {
		t.Fatalf("dimension score trends total = %d, items = %+v", trendScoresResp.Total, trendScoresResp.Items)
	}
	if trendScoresResp.Items[0].Period == "" || trendScoresResp.Items[0].PromptVersion != 1 || trendScoresResp.Items[0].RunCount != 1 || trendScoresResp.Items[0].Score != 1 {
		t.Fatalf("dimension score trend aggregate = %+v", trendScoresResp.Items[0])
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
	if _, ok := summary.Assets["评估维度数"]; !ok {
		t.Fatalf("summary missing evaluation dimension metric: %#v", summary.Assets)
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

func TestGetPromptEvaluationSummaryIncludesDevelopmentFixtures(t *testing.T) {
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
			($1, $2, 'Agent执行', '通过', 1, 1, 13, 9, 0.02),
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
	if allSummary.Assets["资产总数"] != 2 || allSummary.RunStatus["运行总数"] != 3 || allSummary.Metrics["输入token"].(float64) != 124 || allSummary.Metrics["智能体运行数"].(float64) != 1 {
		t.Fatalf("all summary should include acceptance fixtures, assets=%#v status=%#v metrics=%#v", allSummary.Assets, allSummary.RunStatus, allSummary.Metrics)
	}

	compatW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationSummary(compatW, newRequest(http.MethodGet, "/api/prompt-evaluation-summary?include_acceptance_fixtures=false", nil))
	if compatW.Code != http.StatusOK {
		t.Fatalf("compat summary status = %d, body = %s", compatW.Code, compatW.Body.String())
	}
	var compatSummary PromptEvaluationSummaryResponse
	if err := json.Unmarshal(compatW.Body.Bytes(), &compatSummary); err != nil {
		t.Fatalf("decode compat summary: %v", err)
	}
	if compatSummary.Assets["资产总数"] != 2 || compatSummary.RunStatus["运行总数"] != 3 || compatSummary.Metrics["输入token"].(float64) != 124 {
		t.Fatalf("compat summary should include acceptance fixtures, assets=%#v status=%#v metrics=%#v", compatSummary.Assets, compatSummary.RunStatus, compatSummary.Metrics)
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
			"schema":         "multica.training_evaluation.payload.v1",
			"语义版本":           "multica.training_evaluation.v1",
			"cases": []map[string]any{
				{
					"case_name":         "规范数据集用例",
					"variables":         map[string]any{"project": "user-center", "issue_title": "登录失败"},
					"expected_contains": []string{"user-center", "登录失败"},
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
		Items      []PromptEvaluationCaseResponse `json:"items"`
		Total      int                            `json:"total"`
		TotalCount int                            `json:"total_count"`
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
	if listed.Total != 2 || listed.TotalCount != 2 || listedManual == nil {
		t.Fatalf("listed cases = %+v", listed)
	}
	if len(listedManual.Assertions) != 1 || listedManual.Assertions[0].ExpectedText != "可观测证据" {
		t.Fatalf("listed assertions = %+v", listedManual.Assertions)
	}
	firstPageW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(firstPageW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&limit=1&sort_by=case_index&sort_direction=asc", nil))
	if firstPageW.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body = %s", firstPageW.Code, firstPageW.Body.String())
	}
	var firstPage struct {
		Items         []PromptEvaluationCaseResponse `json:"items"`
		Total         int                            `json:"total"`
		TotalCount    int                            `json:"total_count"`
		Limit         int                            `json:"limit"`
		Offset        int                            `json:"offset"`
		HasMore       bool                           `json:"has_more"`
		NextCursor    *string                        `json:"next_cursor"`
		SortBy        string                         `json:"sort_by"`
		SortDirection string                         `json:"sort_direction"`
	}
	if err := json.Unmarshal(firstPageW.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if firstPage.Total != 2 || firstPage.TotalCount != 2 || firstPage.Limit != 1 || firstPage.Offset != 0 || !firstPage.HasMore || firstPage.NextCursor == nil || firstPage.SortBy != "case_index" || firstPage.SortDirection != "asc" {
		t.Fatalf("first page metadata = %+v", firstPage)
	}
	secondPageW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(secondPageW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&limit=1&cursor="+*firstPage.NextCursor, nil))
	if secondPageW.Code != http.StatusOK {
		t.Fatalf("second page status = %d, body = %s", secondPageW.Code, secondPageW.Body.String())
	}
	var secondPage struct {
		Items      []PromptEvaluationCaseResponse `json:"items"`
		TotalCount int                            `json:"total_count"`
		Offset     int                            `json:"offset"`
		HasMore    bool                           `json:"has_more"`
		NextCursor *string                        `json:"next_cursor"`
	}
	if err := json.Unmarshal(secondPageW.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if secondPage.TotalCount != 2 || secondPage.Offset != 1 || secondPage.HasMore || secondPage.NextCursor != nil || len(secondPage.Items) != 1 {
		t.Fatalf("second page metadata = %+v", secondPage)
	}
	mismatchedCursorW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(mismatchedCursorW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&limit=1&sort_direction=desc&cursor="+*firstPage.NextCursor, nil))
	if mismatchedCursorW.Code != http.StatusBadRequest {
		t.Fatalf("mismatched cursor status = %d, body = %s", mismatchedCursorW.Code, mismatchedCursorW.Body.String())
	}
	filteredW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(filteredW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&source=manual&tag=user-center&keyword=%E5%8F%AF%E8%A7%82%E6%B5%8B&limit=1", nil))
	if filteredW.Code != http.StatusOK {
		t.Fatalf("filtered case status = %d, body = %s", filteredW.Code, filteredW.Body.String())
	}
	var filtered struct {
		Items []PromptEvaluationCaseResponse `json:"items"`
		Total int                            `json:"total"`
	}
	if err := json.Unmarshal(filteredW.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered cases: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != created.ID {
		t.Fatalf("filtered cases = %+v", filtered)
	}
	tagSummaryW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCaseTagSummaries(tagSummaryW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases/tag-summaries?asset_id="+asset.ID+"&source=manual&keyword=%E5%8F%AF%E8%A7%82%E6%B5%8B&limit=5", nil))
	if tagSummaryW.Code != http.StatusOK {
		t.Fatalf("tag summary status = %d, body = %s", tagSummaryW.Code, tagSummaryW.Body.String())
	}
	var tagSummary struct {
		Items []PromptEvaluationCaseTagSummaryResponse `json:"items"`
		Total int                                      `json:"total"`
	}
	if err := json.Unmarshal(tagSummaryW.Body.Bytes(), &tagSummary); err != nil {
		t.Fatalf("decode tag summary: %v", err)
	}
	if tagSummary.Total == 0 || !promptEvaluationTagSummaryContains(tagSummary.Items, "user-center", 1) {
		t.Fatalf("tag summary = %+v", tagSummary)
	}
	createSecondAssetW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createSecondAssetW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "评测用例 CRUD 第二数据集",
		"asset_type": "数据集",
		"payload":    map[string]any{"cases": []any{}},
	}))
	if createSecondAssetW.Code != http.StatusCreated {
		t.Fatalf("create second asset status = %d, body = %s", createSecondAssetW.Code, createSecondAssetW.Body.String())
	}
	var secondAsset PromptEvaluationAssetResponse
	if err := json.Unmarshal(createSecondAssetW.Body.Bytes(), &secondAsset); err != nil {
		t.Fatalf("decode second asset: %v", err)
	}
	createSecondCaseW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationCase(createSecondCaseW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases", map[string]any{
		"asset_id":          secondAsset.ID,
		"case_name":         "跨数据集标签分布",
		"variables":         map[string]any{"issue_title": "标签治理"},
		"expected_contains": []string{"跨数据集"},
		"tags":              []string{"user-center", "跨集标签"},
		"status":            "启用",
	}))
	if createSecondCaseW.Code != http.StatusCreated {
		t.Fatalf("create second case status = %d, body = %s", createSecondCaseW.Code, createSecondCaseW.Body.String())
	}
	tagDatasetSummaryW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCaseTagDatasetSummaries(tagDatasetSummaryW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases/tag-dataset-summaries?limit=5&top_dataset_limit=5", nil))
	if tagDatasetSummaryW.Code != http.StatusOK {
		t.Fatalf("tag dataset summary status = %d, body = %s", tagDatasetSummaryW.Code, tagDatasetSummaryW.Body.String())
	}
	var tagDatasetSummary struct {
		Items []PromptEvaluationCaseTagDatasetSummaryResponse `json:"items"`
		Total int                                             `json:"total"`
	}
	if err := json.Unmarshal(tagDatasetSummaryW.Body.Bytes(), &tagDatasetSummary); err != nil {
		t.Fatalf("decode tag dataset summary: %v", err)
	}
	if tagDatasetSummary.Total == 0 || !promptEvaluationTagDatasetSummaryContains(tagDatasetSummary.Items, "user-center", 2, 2) {
		t.Fatalf("tag dataset summary = %+v", tagDatasetSummary)
	}
	invalidLimitW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(invalidLimitW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&limit=9999", nil))
	if invalidLimitW.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, body = %s", invalidLimitW.Code, invalidLimitW.Body.String())
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

	bulkTagsW := httptest.NewRecorder()
	testHandler.BulkUpdatePromptEvaluationCaseTags(bulkTagsW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases/bulk-tags", map[string]any{
		"asset_id": asset.ID,
		"source":   "payload",
		"tag":      "治理标签",
		"tags":     []string{"批量验收"},
		"mode":     "追加",
		"limit":    50,
	}))
	if bulkTagsW.Code != http.StatusOK {
		t.Fatalf("bulk tags status = %d, body = %s", bulkTagsW.Code, bulkTagsW.Body.String())
	}
	var bulkTags struct {
		Operation PromptEvaluationCaseOperationResponse `json:"operation"`
		Cases     []PromptEvaluationCaseResponse        `json:"cases"`
	}
	if err := json.Unmarshal(bulkTagsW.Body.Bytes(), &bulkTags); err != nil {
		t.Fatalf("decode bulk tags: %v", err)
	}
	if bulkTags.Operation.OperationType != "批量追加标签" || bulkTags.Operation.ChangedCount != 1 || len(bulkTags.Cases) != 1 {
		t.Fatalf("bulk operation = %+v cases = %+v", bulkTags.Operation, bulkTags.Cases)
	}
	if !containsAll(strings.Join(stringListFromAny(bulkTags.Cases[0].Tags), ","), []string{"治理标签", "批量验收"}) {
		t.Fatalf("bulk updated tags = %+v", bulkTags.Cases[0].Tags)
	}
	operationsW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCaseOperations(operationsW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+asset.ID+"/case-operations", nil), "id", asset.ID))
	if operationsW.Code != http.StatusOK {
		t.Fatalf("list operations status = %d, body = %s", operationsW.Code, operationsW.Body.String())
	}
	var operations struct {
		Items []PromptEvaluationCaseOperationResponse `json:"items"`
		Total int                                     `json:"total"`
	}
	if err := json.Unmarshal(operationsW.Body.Bytes(), &operations); err != nil {
		t.Fatalf("decode operations: %v", err)
	}
	if operations.Total != 1 || operations.Items[0].OperationType != "批量追加标签" || operations.Items[0].ChangedCount != 1 {
		t.Fatalf("operations = %+v", operations)
	}
	backgroundTagsW := httptest.NewRecorder()
	testHandler.BulkUpdatePromptEvaluationCaseTags(backgroundTagsW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases/bulk-tags", map[string]any{
		"asset_id":       asset.ID,
		"source":         "payload",
		"tag":            "批量验收",
		"tags":           []string{"后台验收"},
		"mode":           "追加",
		"execution_mode": "后台",
		"limit":          50,
	}))
	if backgroundTagsW.Code != http.StatusAccepted {
		t.Fatalf("background bulk tags status = %d, body = %s", backgroundTagsW.Code, backgroundTagsW.Body.String())
	}
	var backgroundTags struct {
		Operation PromptEvaluationCaseOperationResponse `json:"operation"`
	}
	if err := json.Unmarshal(backgroundTagsW.Body.Bytes(), &backgroundTags); err != nil {
		t.Fatalf("decode background bulk tags: %v", err)
	}
	if backgroundTags.Operation.Status != "已入队" {
		t.Fatalf("background operation status = %+v", backgroundTags.Operation)
	}
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin background operation consumer tx: %v", err)
	}
	if _, err := testHandler.consumePromptEvaluationCaseOperation(context.Background(), testHandler.Queries.WithTx(tx), events.Event{
		Type:        promptEvaluationCaseOperationRequestedEvent,
		WorkspaceID: testWorkspaceID,
		Payload: map[string]any{
			"operation_id": backgroundTags.Operation.ID,
		},
	}); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("consume background operation: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit background operation consumer tx: %v", err)
	}
	mustExec(t, context.Background(), `
		DELETE FROM domain_event_outbox
		WHERE event_type = $1 AND payload->>'operation_id' = $2
	`, promptEvaluationCaseOperationRequestedEvent, backgroundTags.Operation.ID)
	var completedBackground PromptEvaluationCaseOperationResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollW := httptest.NewRecorder()
		testHandler.ListPromptEvaluationCaseOperations(pollW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+asset.ID+"/case-operations", nil), "id", asset.ID))
		if pollW.Code != http.StatusOK {
			t.Fatalf("poll operations status = %d, body = %s", pollW.Code, pollW.Body.String())
		}
		var polled struct {
			Items []PromptEvaluationCaseOperationResponse `json:"items"`
			Total int                                     `json:"total"`
		}
		if err := json.Unmarshal(pollW.Body.Bytes(), &polled); err != nil {
			t.Fatalf("decode polled operations: %v", err)
		}
		for _, item := range polled.Items {
			if item.ID == backgroundTags.Operation.ID && item.Status == "已完成" {
				completedBackground = item
				break
			}
		}
		if completedBackground.ID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if completedBackground.Status != "已完成" || completedBackground.ChangedCount != 1 || completedBackground.CompletedAt == nil {
		t.Fatalf("background operation did not complete: %+v", completedBackground)
	}
	backgroundCasesW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCases(backgroundCasesW, newRequest(http.MethodGet, "/api/prompt-evaluation-cases?asset_id="+asset.ID+"&tag="+url.QueryEscape("后台验收"), nil))
	if backgroundCasesW.Code != http.StatusOK {
		t.Fatalf("list background cases status = %d, body = %s", backgroundCasesW.Code, backgroundCasesW.Body.String())
	}
	var backgroundCases struct {
		Items []PromptEvaluationCaseResponse `json:"items"`
	}
	if err := json.Unmarshal(backgroundCasesW.Body.Bytes(), &backgroundCases); err != nil {
		t.Fatalf("decode background cases: %v", err)
	}
	if len(backgroundCases.Items) != 1 || !containsAll(strings.Join(stringListFromAny(backgroundCases.Items[0].Tags), ","), []string{"后台验收", "批量验收"}) {
		t.Fatalf("background updated cases = %+v", backgroundCases.Items)
	}
	reloadedAsset, err := testHandler.Queries.GetPromptEvaluationAssetInWorkspace(context.Background(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: parseUUID(asset.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatalf("reload asset after bulk tags: %v", err)
	}
	if !containsAll(string(reloadedAsset.Payload), []string{"治理标签", "批量验收", "最近批量用例操作"}) {
		t.Fatalf("asset payload was not synced after bulk tags: %s", string(reloadedAsset.Payload))
	}
	var reloadedPayload map[string]any
	if err := json.Unmarshal(reloadedAsset.Payload, &reloadedPayload); err != nil {
		t.Fatalf("decode reloaded asset payload: %v", err)
	}
	payloadCases, ok := reloadedPayload["cases"].([]any)
	if !ok || len(payloadCases) != 1 {
		t.Fatalf("normalized payload cases were not preserved: %#v", reloadedPayload["cases"])
	}
	payloadCase, ok := payloadCases[0].(map[string]any)
	if !ok || !containsAll(strings.Join(stringListFromAny(payloadCase["tags"]), ","), []string{"治理标签", "批量验收"}) {
		t.Fatalf("normalized payload case tags were not synced: %#v", payloadCases[0])
	}
	createVersionBeforeRenameW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationDatasetVersion(createVersionBeforeRenameW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/dataset-versions", map[string]any{
		"version_label": "重命名前",
	}), "id", asset.ID))
	if createVersionBeforeRenameW.Code != http.StatusCreated {
		t.Fatalf("create version before rename status = %d, body = %s", createVersionBeforeRenameW.Code, createVersionBeforeRenameW.Body.String())
	}

	renameTagsW := httptest.NewRecorder()
	testHandler.BulkUpdatePromptEvaluationCaseTags(renameTagsW, newRequest(http.MethodPost, "/api/prompt-evaluation-cases/bulk-tags", map[string]any{
		"asset_id":   asset.ID,
		"source_tag": "治理标签",
		"target_tag": "治理展示",
		"mode":       "重命名",
		"limit":      50,
	}))
	if renameTagsW.Code != http.StatusOK {
		t.Fatalf("bulk rename tags status = %d, body = %s", renameTagsW.Code, renameTagsW.Body.String())
	}
	var renamedTags struct {
		Operation PromptEvaluationCaseOperationResponse `json:"operation"`
		Cases     []PromptEvaluationCaseResponse        `json:"cases"`
	}
	if err := json.Unmarshal(renameTagsW.Body.Bytes(), &renamedTags); err != nil {
		t.Fatalf("decode bulk rename tags: %v", err)
	}
	if renamedTags.Operation.OperationType != "批量重命名/合并标签" || renamedTags.Operation.ChangedCount != 1 || len(renamedTags.Cases) != 1 {
		t.Fatalf("bulk rename operation = %+v cases = %+v", renamedTags.Operation, renamedTags.Cases)
	}
	renamedCaseTags := strings.Join(stringListFromAny(renamedTags.Cases[0].Tags), ",")
	if !containsAll(renamedCaseTags, []string{"治理展示", "批量验收"}) || strings.Contains(renamedCaseTags, "治理标签") {
		t.Fatalf("bulk renamed tags = %+v", renamedTags.Cases[0].Tags)
	}
	operationsAfterRenameW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationCaseOperations(operationsAfterRenameW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+asset.ID+"/case-operations", nil), "id", asset.ID))
	if operationsAfterRenameW.Code != http.StatusOK {
		t.Fatalf("list operations after rename status = %d, body = %s", operationsAfterRenameW.Code, operationsAfterRenameW.Body.String())
	}
	if err := json.Unmarshal(operationsAfterRenameW.Body.Bytes(), &operations); err != nil {
		t.Fatalf("decode operations after rename: %v", err)
	}
	if operations.Total != 3 || operations.Items[0].OperationType != "批量重命名/合并标签" || operations.Items[0].ChangedCount != 1 {
		t.Fatalf("operations after rename = %+v", operations)
	}
	reloadedAsset, err = testHandler.Queries.GetPromptEvaluationAssetInWorkspace(context.Background(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: parseUUID(asset.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatalf("reload asset after bulk rename: %v", err)
	}
	if err := json.Unmarshal(reloadedAsset.Payload, &reloadedPayload); err != nil {
		t.Fatalf("decode reloaded asset after rename: %v", err)
	}
	payloadCases, ok = reloadedPayload["cases"].([]any)
	if !ok || len(payloadCases) != 1 {
		t.Fatalf("renamed payload cases were not preserved: %#v", reloadedPayload["cases"])
	}
	payloadCase, ok = payloadCases[0].(map[string]any)
	renamedPayloadTags := strings.Join(stringListFromAny(payloadCase["tags"]), ",")
	if !ok || !containsAll(renamedPayloadTags, []string{"治理展示", "批量验收"}) || strings.Contains(renamedPayloadTags, "治理标签") {
		t.Fatalf("renamed payload case tags were not synced: %#v", payloadCases[0])
	}
	createVersionAfterRenameW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationDatasetVersion(createVersionAfterRenameW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+asset.ID+"/dataset-versions", map[string]any{
		"version_label": "重命名后",
	}), "id", asset.ID))
	if createVersionAfterRenameW.Code != http.StatusCreated {
		t.Fatalf("create version after rename status = %d, body = %s", createVersionAfterRenameW.Code, createVersionAfterRenameW.Body.String())
	}
	tagTrendsW := httptest.NewRecorder()
	testHandler.ListPromptEvaluationDatasetVersionTagTrends(tagTrendsW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+asset.ID+"/dataset-versions/tag-trends?version_limit=2&limit=20", nil), "id", asset.ID))
	if tagTrendsW.Code != http.StatusOK {
		t.Fatalf("tag trends status = %d, body = %s", tagTrendsW.Code, tagTrendsW.Body.String())
	}
	var tagTrends struct {
		Items []PromptEvaluationDatasetVersionTagTrendResponse `json:"items"`
		Total int                                              `json:"total"`
	}
	if err := json.Unmarshal(tagTrendsW.Body.Bytes(), &tagTrends); err != nil {
		t.Fatalf("decode tag trends: %v", err)
	}
	if tagTrends.Total == 0 ||
		!promptEvaluationDatasetVersionTagTrendContains(tagTrends.Items, 1, "治理标签", 1) ||
		!promptEvaluationDatasetVersionTagTrendContains(tagTrends.Items, 2, "治理展示", 1) {
		t.Fatalf("tag trends = %+v", tagTrends)
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
	if resp.TaskID == "" || resp.ChatSessionID == "" || resp.AgentID == "" || resp.RuntimeID != runtimeID || resp.Model != "deepseek-v4-pro-ioa" {
		t.Fatalf("agent run response = %+v, runtimeID=%s", resp, runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["trace/task id"] != resp.TaskID || recent["状态"] != "已入队" || recent["评估结论"] != "等待智能体执行完成" {
		t.Fatalf("recent agent run = %#v", recent)
	}
	if resp.Run.ID == "" || resp.Run.Status != "已入队" || resp.Run.RunKind != "Agent执行" || resp.Run.TriggerSource != "评测运行" || resp.Run.TaskID == nil || *resp.Run.TaskID != resp.TaskID {
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
		VALUES ($1, 'codebuddy', 'deepseek-v4-pro-ioa', 11, 7, 2, 3, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}
	structuredOutput := `Agent 输出：
` + "```json" + `
{"schema_version":1,"schema":"multica.training_evaluation.agent_verdict.v1","case_results":[{"case_index":0,"status":"通过","output":"登录失败：已覆盖验收条件和 trace/任务标识","failure_reason":"无","conclusion":"通过","命中":["登录失败","验收条件","trace/任务标识"],"缺失":[],"evidence":{"命中":["登录失败","验收条件","trace/任务标识"]}}],"summary":{"total_cases":1,"passed_cases":1,"failed_cases":0,"failure_reason":"无","conclusion":"Agent 已返回结构化逐用例评估"}}
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
		Model:         "deepseek-v4-pro-ioa",
		InputTokens:   16,
		OutputTokens:  7,
		FailureReason: "无",
		ErrorType:     "",
		Metadata:      []byte(`{"阶段":"训练评估"}`),
	}); err != nil {
		t.Fatalf("insert task trace event: %v", err)
	}
	// task_usage is the canonical billing record. The deliberately different
	// trace count proves that run and trial totals do not double-read trace data.

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
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project completed prompt evaluation task: %v", err)
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
	if evidence.Run.Status != "通过" || evidence.Run.PassedCases != 1 || evidence.Run.FailedCases != 0 || evidence.Run.InputTokens != 11 || evidence.Run.OutputTokens != 7 {
		t.Fatalf("auto-synced run = %+v", evidence.Run)
	}
	if evidence.Run.EstimatedCost <= 0 {
		t.Fatalf("auto-synced run estimated cost = %v, want > 0", evidence.Run.EstimatedCost)
	}
	runMetrics, ok := evidence.Run.Metrics.(map[string]any)
	if !ok {
		t.Fatalf("run metrics type = %T", evidence.Run.Metrics)
	}
	dimensionScores, ok := runMetrics["实验维度评分"].([]any)
	if !ok || len(dimensionScores) != 3 {
		t.Fatalf("run metrics missing agent dimension scores: %#v", runMetrics)
	}
	firstDimensionScore, _ := dimensionScores[0].(map[string]any)
	if firstDimensionScore["维度名称"] != "命中率" || firstDimensionScore["状态"] != "已评分" || firstDimensionScore["通过用例数"] != float64(1) {
		t.Fatalf("first agent dimension score = %#v", firstDimensionScore)
	}
	runEvidence, ok := evidence.Run.Evidence.(map[string]any)
	if !ok {
		t.Fatalf("run evidence type = %T", evidence.Run.Evidence)
	}
	if evidenceScores, ok := runEvidence["实验维度评分"].([]any); !ok || len(evidenceScores) != 3 {
		t.Fatalf("run evidence missing agent dimension scores: %#v", runEvidence)
	}
	assertPromptEvaluationDimensionScores(t, resp.Run.ID, []expectedPromptEvaluationDimensionScore{
		{name: "命中率", status: "已评分", source: "agent_sync", passed: 1, total: 1},
		{name: "缺失变量", status: "已评分", source: "agent_sync", passed: 1, total: 1},
		{name: "中文一致性", status: "已评分", source: "agent_sync", passed: 1, total: 1},
	})
	if evidence.Trials[0].Status != "通过" || evidence.Trials[0].FailureReason != "无" || evidence.Trials[0].InputTokens != 11 || evidence.Trials[0].OutputTokens != 7 {
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

func TestRunPromptEvaluationAssetAgentRestoresArchivedTrainingAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 测试运行时', '{}'::jsonb, $4, 'personal', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codebuddy-restore-"+randomID()[:8], "prompt-eval-codebuddy-restore-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}
	archived, err := testHandler.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Name:               promptEvaluationAgentName,
		Description:        "archived prompt evaluation agent",
		RuntimeMode:        "local",
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          parseUUID(runtimeID),
		Scope:              "workspace",
		MaxConcurrentTasks: 1,
		OwnerID:            parseUUID(testUserID),
		Instructions:       promptEvaluationAgentInstructions(),
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
	})
	if err != nil {
		t.Fatalf("create archived training agent fixture: %v", err)
	}
	if _, err := testHandler.Queries.ArchiveAgent(context.Background(), db.ArchiveAgentParams{
		ID:         archived.ID,
		ArchivedBy: parseUUID(testUserID),
	}); err != nil {
		t.Fatalf("archive training agent fixture: %v", err)
	}

	promptID := createPromptEvaluationTestPromptWithContent(t, testWorkspaceID, "恢复归档训练智能体提示词", "请评估 {{issue_title}}。", `[]`)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "恢复归档训练智能体实验",
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "恢复归档", "变量": map[string]any{"issue_title": "恢复归档"}, "期望包含": []string{"恢复"}}},
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
	if resp.AgentID != uuidToString(archived.ID) {
		t.Fatalf("expected archived agent to be restored and reused, got agent_id=%s want=%s", resp.AgentID, uuidToString(archived.ID))
	}
	var archivedAt *time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT archived_at FROM agent WHERE id = $1`, resp.AgentID).Scan(&archivedAt); err != nil {
		t.Fatalf("load restored agent archive state: %v", err)
	}
	if archivedAt != nil {
		t.Fatalf("expected restored training agent archived_at nil, got %v", archivedAt)
	}
}

func TestPromptEvaluationAgentModelCanBeConfigured(t *testing.T) {
	t.Setenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL", "")
	if got := promptEvaluationAgentModel(); got != "deepseek-v4-pro-ioa" {
		t.Fatalf("default prompt evaluation agent model = %q", got)
	}
	t.Setenv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL", "custom-eval-model")
	if got := promptEvaluationAgentModel(); got != "custom-eval-model" {
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
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 过期测试运行时', '{}'::jsonb, $4, 'personal', now() - interval '5 minutes')
	`, testWorkspaceID, "prompt-eval-codebuddy-stale-"+randomID()[:8], "prompt-eval-codebuddy-stale-"+randomID()[:8], testUserID); err != nil {
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
		"asset_type": "测试套件",
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
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project failed prompt evaluation task: %v", err)
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
		"asset_type": "测试套件",
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
	if missing.Status != "缺失" || !strings.Contains(missing.Fix, "启动 multica 守护进程") || missing.Runtime != nil {
		t.Fatalf("missing readiness = %+v", missing)
	}

	var offlineRuntimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'offline', 'CodeBuddy 离线测试运行时', '{}'::jsonb, $4, 'personal', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-codebuddy-offline-"+randomID()[:8], "prompt-eval-codebuddy-offline-"+randomID()[:8], testUserID).Scan(&offlineRuntimeID); err != nil {
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
		"asset_type": "测试套件",
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
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 私有测试运行时', '{}'::jsonb, $4, 'personal', now())
	`, testWorkspaceID, "prompt-eval-codebuddy-private-"+randomID()[:8], "prompt-eval-codebuddy-private-"+randomID()[:8], runtimeOwnerID); err != nil {
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
	if noPermission.Status != "无权限" || !strings.Contains(noPermission.Fix, "运行时所有者") || noPermission.Runtime != nil {
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
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.CompleteTask(completeW, withURLParam(completeReq, "taskId", resp.TaskID))
	if completeW.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completeW.Code, completeW.Body.String())
	}
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project completed prompt evaluation task: %v", err)
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
		VALUES ($1, 'codebuddy', 'deepseek-v4-pro-ioa', 5, 1, 0, 0, now())
	`, resp.TaskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+resp.TaskID+"/fail", map[string]any{
		"error":          "智能体执行超时",
		"failure_reason": "命令超时",
		"session_id":     "prompt-eval-failed-session",
		"work_dir":       "/tmp/prompt-eval",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project failed prompt evaluation task: %v", err)
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
		"error":          "402 当前账号 Token 额度已耗尽",
		"failure_reason": "agent_error.provider_quota_limit",
		"session_id":     "prompt-eval-snapshot-session",
		"work_dir":       "/tmp/prompt-eval-snapshot",
	}, testWorkspaceID, "prompt-eval-codebuddy-daemon")
	testHandler.FailTask(failW, withURLParam(failReq, "taskId", resp.TaskID))
	if failW.Code != http.StatusOK {
		t.Fatalf("fail status = %d, body = %s", failW.Code, failW.Body.String())
	}
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project failed prompt evaluation task: %v", err)
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
	if !ok || summary["运行状态"] != "失败" || summary["失败原因"] != "402 当前账号 Token 额度已耗尽" || summary["trace/task id"] != resp.TaskID {
		t.Fatalf("snapshot summary = %#v", snapshot.Summary)
	}
	insightSummary, ok := summary["服务端解释"].(map[string]any)
	if !ok || insightSummary["质量判断"] != "质量风险高" || insightSummary["建议动作"] == "" || insightSummary["维度摘要数"].(float64) < 1 {
		t.Fatalf("snapshot insight summary = %#v", summary["服务端解释"])
	}
	payload, ok := snapshot.Evidence.(map[string]any)
	if !ok || payload["语义版本"] != "multica.prompt_evaluation.evidence_snapshot.v1" || payload["运行证据"] == nil {
		t.Fatalf("snapshot evidence payload = %#v", snapshot.Evidence)
	}
	insight, ok := payload["服务端解释快照"].(map[string]any)
	if !ok || insight["语义版本"] != "multica.prompt_evaluation.evidence_snapshot.insight.v1" || insight["质量判断"] != "质量风险高" {
		t.Fatalf("snapshot insight payload = %#v", payload["服务端解释快照"])
	}
	if scores, ok := insight["维度评分摘要"].([]any); !ok || len(scores) < 1 {
		t.Fatalf("snapshot insight missing dimension summaries: %#v", insight)
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

func TestPromptEvaluationAssetEvidenceSnapshotsArchiveRecentRuns(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	created, resp, _ := createPromptEvaluationAgentRunFixture(t, "资产级证据快照实验", "需要批量归档")

	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAssetEvidenceSnapshots(createW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/evidence-snapshots?snapshot_type=验收归档", nil), "id", created.ID))
	if createW.Code != http.StatusCreated {
		t.Fatalf("asset snapshot status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var batch PromptEvaluationAssetEvidenceSnapshotResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &batch); err != nil {
		t.Fatalf("decode asset snapshot response: %v", err)
	}
	if batch.AssetID != created.ID || batch.SnapshotType != "验收归档" || batch.TotalRuns != 1 || batch.CreatedCount != 1 || batch.SkippedCount != 0 || len(batch.Items) != 1 {
		t.Fatalf("asset snapshot batch = %+v", batch)
	}
	if batch.Items[0].RunID != resp.Run.ID || batch.Items[0].Evidence != nil {
		t.Fatalf("asset snapshot item = %+v", batch.Items[0])
	}

	retryW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAssetEvidenceSnapshots(retryW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/evidence-snapshots?snapshot_type=验收归档", nil), "id", created.ID))
	if retryW.Code != http.StatusCreated {
		t.Fatalf("asset snapshot retry status = %d, body = %s", retryW.Code, retryW.Body.String())
	}
	var retry PromptEvaluationAssetEvidenceSnapshotResponse
	if err := json.Unmarshal(retryW.Body.Bytes(), &retry); err != nil {
		t.Fatalf("decode asset snapshot retry response: %v", err)
	}
	if retry.CreatedCount != 0 || retry.SkippedCount != 1 || len(retry.Skipped) != 1 || retry.Skipped[0].RunID != resp.Run.ID {
		t.Fatalf("asset snapshot retry = %+v", retry)
	}

	exportW := httptest.NewRecorder()
	testHandler.GetPromptEvaluationAssetEvidenceSnapshotPackage(exportW, withURLParam(newRequest(http.MethodGet, "/api/prompt-evaluation-assets/"+created.ID+"/evidence-snapshots/export?snapshot_type=验收归档", nil), "id", created.ID))
	if exportW.Code != http.StatusOK {
		t.Fatalf("asset snapshot export status = %d, body = %s", exportW.Code, exportW.Body.String())
	}
	var archivePackage PromptEvaluationAssetEvidenceArchivePackage
	if err := json.Unmarshal(exportW.Body.Bytes(), &archivePackage); err != nil {
		t.Fatalf("decode asset snapshot export response: %v", err)
	}
	if archivePackage.SchemaVersion != "multica.prompt_evaluation.asset_evidence_archive.v1" ||
		archivePackage.AssetID != created.ID ||
		archivePackage.SnapshotType != "验收归档" ||
		archivePackage.TotalRuns != 1 ||
		archivePackage.ArchivedRunCount != 1 ||
		archivePackage.MissingRunCount != 0 ||
		len(archivePackage.Items) != 1 {
		t.Fatalf("asset snapshot export = %+v", archivePackage)
	}
	if archivePackage.Items[0].Run.ID != resp.Run.ID || len(archivePackage.Items[0].Snapshots) != 1 || archivePackage.Items[0].Snapshots[0].Evidence == nil {
		t.Fatalf("asset snapshot export item = %+v", archivePackage.Items[0])
	}
	evidence, ok := archivePackage.Items[0].Snapshots[0].Evidence.(map[string]any)
	if !ok || evidence["服务端解释快照"] == nil {
		t.Fatalf("asset snapshot export evidence missing insight: %#v", archivePackage.Items[0].Snapshots[0].Evidence)
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
		VALUES ($1, 'codebuddy', 'deepseek-v4-pro-ioa', 13, 8, 1, 2, now())
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
		Model:         "deepseek-v4-pro-ioa",
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
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project failed prompt evaluation task: %v", err)
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
	if !containsAll(candidate.CandidateContent, []string{"真实智能体输出摘要", "Agent 输出：缺少验收条件", "训练评估失败证据", "预估成本", "维度优先级", "命中率"}) {
		t.Fatalf("candidate content missing agent evidence: %s", candidate.CandidateContent)
	}
	if !containsAll(candidate.Rationale, []string{"维度评分弱项", "真实运行证据"}) {
		t.Fatalf("candidate rationale missing dimension or agent evidence: %s", candidate.Rationale)
	}
	source := candidate.SourceFailureSummary.(map[string]any)
	runtimeEvidence, ok := source["真实Agent运行证据"].(map[string]any)
	if !ok {
		t.Fatalf("source summary missing runtime evidence: %#v", source)
	}
	if len(runtimeEvidence["task消息"].([]any)) != 1 || len(runtimeEvidence["trace事件"].([]any)) < 1 || len(runtimeEvidence["task用量"].([]any)) != 1 {
		t.Fatalf("runtime evidence incomplete: %#v", runtimeEvidence)
	}
	weakDimensions, ok := source["失败维度"].([]any)
	if !ok || len(weakDimensions) != 3 {
		t.Fatalf("source summary missing weak dimensions: %#v", source)
	}
	if source["候选优先级"] != "高" {
		t.Fatalf("source priority = %#v", source["候选优先级"])
	}
	candidateMetrics := candidate.Metrics.(map[string]any)
	if candidateMetrics["候选优先级"] != "高" || candidateMetrics["候选优先级依据"] == "" {
		t.Fatalf("candidate metrics missing priority: %#v", candidateMetrics)
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
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project cancelled prompt evaluation task: %v", err)
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
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project batch failed evaluation task: %v", err)
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
	if _, err := projectPromptEvaluationTerminalTask(context.Background(), resp.TaskID); err != nil {
		t.Fatalf("project retrying evaluation task: %v", err)
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

func TestRunPromptEvaluationAssetAgentDefaultsExperimentDimensions(t *testing.T) {
	t.Skip("experiment assets were removed from training evaluation")
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 默认维度运行时', '{}'::jsonb, $4, 'personal', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-default-dimension-daemon-"+randomID()[:8], "prompt-eval-default-dimension-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}
	promptID := createPromptEvaluationTestPromptWithContent(
		t,
		testWorkspaceID,
		"默认维度提示词",
		"请评估 {{issue_title}}，输出中文结论。",
		`[]`,
	)
	createW := httptest.NewRecorder()
	testHandler.CreatePromptEvaluationAsset(createW, newRequest(http.MethodPost, "/api/prompt-evaluation-assets", map[string]any{
		"prompt_id":  promptID,
		"name":       "默认维度实验",
		"asset_type": "测试套件",
		"payload": map[string]any{
			"cases": []map[string]any{{"名称": "默认维度用例", "变量": map[string]any{"issue_title": "默认维度"}, "期望包含": []string{"中文结论"}}},
		},
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createW.Code, createW.Body.String())
	}
	var created PromptEvaluationAssetResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ExperimentDimensionCount != 3 {
		t.Fatalf("experiment dimension count = %d, want 3", created.ExperimentDimensionCount)
	}
	assertPromptEvaluationExperimentDimensions(t, created.ID, []string{"命中率", "缺失变量", "中文一致性"})

	runW := httptest.NewRecorder()
	testHandler.RunPromptEvaluationAssetAgent(runW, withURLParam(newRequest(http.MethodPost, "/api/prompt-evaluation-assets/"+created.ID+"/agent-run", nil), "id", created.ID))
	if runW.Code != http.StatusAccepted {
		t.Fatalf("agent run status = %d, body = %s", runW.Code, runW.Body.String())
	}
	var resp PromptEvaluationAgentRunResponse
	if err := json.Unmarshal(runW.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode agent run response: %v", err)
	}
	assertPromptEvaluationDimensionScores(t, resp.Run.ID, []expectedPromptEvaluationDimensionScore{
		{name: "命中率", status: "待执行", source: "run_metrics", passed: 0, total: 1},
		{name: "缺失变量", status: "待执行", source: "run_metrics", passed: 0, total: 1},
		{name: "中文一致性", status: "待执行", source: "run_metrics", passed: 0, total: 1},
	})
}

func TestRunPromptEvaluationAssetAgentUsesRequestedAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	cleanupPromptEvaluationAgentRunTest(t)
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 指定运行时', '{}'::jsonb, $4, 'personal', now())
		RETURNING id
	`, testWorkspaceID, "prompt-eval-selected-daemon-"+randomID()[:8], "prompt-eval-selected-"+randomID()[:8], testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create codebuddy runtime: %v", err)
	}
	agent, err := testHandler.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		Name:               "训练评估指定执行智能体",
		Description:        "用于验证智能体调试场显式选择执行者。",
		RuntimeMode:        "local",
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          parseUUID(runtimeID),
		Scope:              "workspace",
		MaxConcurrentTasks: 1,
		OwnerID:            parseUUID(testUserID),
		Instructions:       "只输出结构化评估结论。",
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: "deepseek-v4-pro-ioa", Valid: true},
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
		"asset_type": "测试套件",
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
	if resp.AgentID != uuidToString(agent.ID) || resp.RuntimeID != runtimeID || resp.Model != "deepseek-v4-pro-ioa" {
		t.Fatalf("agent run did not use requested agent: resp=%+v agent=%s runtime=%s", resp, uuidToString(agent.ID), runtimeID)
	}
	payload := resp.Asset.Payload.(map[string]any)
	recent := payload["最近Agent运行"].(map[string]any)
	if recent["agent_id"] != uuidToString(agent.ID) || recent["执行Agent"] != "训练评估指定执行智能体" || recent["模型"] != "deepseek-v4-pro-ioa" {
		t.Fatalf("recent agent run did not record requested agent: %#v", recent)
	}
}

func createPromptEvaluationAgentRunFixture(t *testing.T, assetName string, caseName string) (PromptEvaluationAssetResponse, PromptEvaluationAgentRunResponse, string) {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, scope, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codebuddy', 'online', 'CodeBuddy 测试运行时', '{}'::jsonb, $4, 'personal', now())
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
		"asset_type": "测试套件",
		"payload": map[string]any{
			"对比维度":  []string{"命中率", "缺失变量", "中文一致性"},
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
		"asset_type": "测试套件",
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
		"asset_type": "测试套件",
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

type expectedPromptEvaluationDimensionScore struct {
	name   string
	status string
	source string
	passed int
	total  int
}

func assertPromptEvaluationDimensionScores(t *testing.T, runID string, expected []expectedPromptEvaluationDimensionScore) {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT dimension_name, status, source, passed_cases, total_cases
		FROM prompt_evaluation_dimension_score
		WHERE run_id = $1
		ORDER BY dimension_index ASC
	`, runID)
	if err != nil {
		t.Fatalf("query dimension scores: %v", err)
	}
	defer rows.Close()
	actual := []expectedPromptEvaluationDimensionScore{}
	for rows.Next() {
		var item expectedPromptEvaluationDimensionScore
		if err := rows.Scan(&item.name, &item.status, &item.source, &item.passed, &item.total); err != nil {
			t.Fatalf("scan dimension score: %v", err)
		}
		actual = append(actual, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dimension scores: %v", err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("dimension scores = %#v, want %#v", actual, expected)
	}
	for idx := range expected {
		if actual[idx] != expected[idx] {
			t.Fatalf("dimension scores = %#v, want %#v", actual, expected)
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

func promptEvaluationTagSummaryContains(items []PromptEvaluationCaseTagSummaryResponse, tag string, count int32) bool {
	for _, item := range items {
		if item.Tag == tag && item.CaseCount == count {
			return true
		}
	}
	return false
}

func promptEvaluationTagDatasetSummaryContains(items []PromptEvaluationCaseTagDatasetSummaryResponse, tag string, count int32, datasetCount int32) bool {
	for _, item := range items {
		if item.Tag == tag && item.CaseCount == count && item.DatasetCount == datasetCount && len(item.TopDatasets) == int(datasetCount) {
			return true
		}
	}
	return false
}

func promptEvaluationDatasetVersionTagTrendContains(items []PromptEvaluationDatasetVersionTagTrendResponse, version int32, tag string, count int32) bool {
	for _, item := range items {
		if item.Version == version && item.Tag == tag && item.CaseCount == count {
			return true
		}
	}
	return false
}
