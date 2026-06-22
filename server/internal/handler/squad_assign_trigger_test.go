package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestCreateIssueAssignedToSquadEnqueuesLeader verifies that creating an
// issue with assignee_type=squad immediately enqueues a task for the squad
// leader (mirrors the agent-assignee parking-lot rule: skip backlog only).
func TestCreateIssueAssignedToSquadEnqueuesLeader(t *testing.T) {
	ctx := context.Background()

	// Look up the seeded test agent — it has a runtime, so it can lead a squad.
	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	// Create a squad with that agent as leader.
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, '{"profile_key":"user-center-test","steps":[{"key":"clarify","name":"需求澄清","role_key":"captain"},{"key":"acceptance","name":"验收","role_key":"acceptor"}]}'::jsonb)
		RETURNING id
	`, testWorkspaceID, "Trigger Test Squad", leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	defer testPool.Exec(ctx, `DELETE FROM squad WHERE id = $1`, squadID)

	// Create an issue assigned to the squad.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Squad-assigned at creation",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	defer func() {
		cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
		cleanupReq = withURLParam(cleanupReq, "id", created.ID)
		testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
	}()

	// A task for the squad leader should now exist for this issue.
	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
	`, created.ID, leaderID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount == 0 {
		t.Fatalf("expected squad-leader task to be enqueued after squad-assigned create, got 0")
	}
	var taskID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text, runtime_id::text FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, created.ID, leaderID).Scan(&taskID, &runtimeID); err != nil {
		t.Fatalf("load leader task: %v", err)
	}

	var runID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM squad_sop_run
		WHERE issue_id = $1 AND squad_id = $2 AND profile_key = 'user-center-test'
	`, created.ID, squadID).Scan(&runID); err != nil {
		t.Fatalf("expected squad SOP run to be created: %v", err)
	}

	var eventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_step_event
		WHERE run_id = $1 AND step_key = 'clarify' AND event_type = '步骤开始'
	`, runID).Scan(&eventCount); err != nil {
		t.Fatalf("count SOP step events: %v", err)
	}
	if eventCount == 0 {
		t.Fatalf("expected initial SOP step event after squad leader task enqueue, got 0")
	}

	listReq := newRequest("GET", "/api/issues/"+created.ID+"/sop-runs?workspace_id="+testWorkspaceID, nil)
	listReq = withURLParam(listReq, "id", created.ID)
	listW := httptest.NewRecorder()
	testHandler.ListIssueSOPRuns(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("ListIssueSOPRuns: expected 200, got %d: %s", listW.Code, listW.Body.String())
	}

	completeStepReq := newRequest("POST", "/api/sop-runs/"+runID+"/steps/clarify/events?workspace_id="+testWorkspaceID, map[string]any{
		"event_type": "步骤完成",
		"evidence":   map[string]any{"阶段产物": "需求已澄清"},
		"reason":     "进入验收阶段",
	})
	completeStepReq = withURLParams(completeStepReq, "runId", runID, "stepId", "clarify")
	completeStepW := httptest.NewRecorder()
	testHandler.RecordSOPStepEvent(completeStepW, completeStepReq)
	if completeStepW.Code != http.StatusCreated {
		t.Fatalf("RecordSOPStepEvent(clarify complete): expected 201, got %d: %s", completeStepW.Code, completeStepW.Body.String())
	}
	var currentStep, runStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT current_step_key, status FROM squad_sop_run WHERE id = $1
	`, runID).Scan(&currentStep, &runStatus); err != nil {
		t.Fatalf("load progressed SOP run: %v", err)
	}
	if currentStep != "acceptance" || runStatus != "进行中" {
		t.Fatalf("SOP run after clarify completion: step=%s status=%s, want acceptance/进行中", currentStep, runStatus)
	}

	unknownStepReq := newRequest("POST", "/api/sop-runs/"+runID+"/steps/deploy/events?workspace_id="+testWorkspaceID, map[string]any{
		"event_type": "追加证据",
		"evidence":   map[string]any{"结果": "不应接受"},
	})
	unknownStepReq = withURLParams(unknownStepReq, "runId", runID, "stepId", "deploy")
	unknownStepW := httptest.NewRecorder()
	testHandler.RecordSOPStepEvent(unknownStepW, unknownStepReq)
	if unknownStepW.Code != http.StatusBadRequest {
		t.Fatalf("RecordSOPStepEvent(unknown step): expected 400, got %d: %s", unknownStepW.Code, unknownStepW.Body.String())
	}

	eventReq := newRequest("POST", "/api/sop-runs/"+runID+"/steps/acceptance/events?workspace_id="+testWorkspaceID, map[string]any{
		"event_type":      "测试结果",
		"status":          "进行中",
		"step_name":       "验收",
		"role_key":        "acceptor",
		"evidence":        map[string]any{"测试命令": "go test ./internal/handler", "结果": "通过"},
		"reason":          "补充测试证据",
		"duration_ms":     123,
		"created_by_type": "agent",
		"created_by_id":   leaderID,
	})
	eventReq = withURLParams(eventReq, "runId", runID, "stepId", "acceptance")
	eventW := httptest.NewRecorder()
	testHandler.RecordSOPStepEvent(eventW, eventReq)
	if eventW.Code != http.StatusCreated {
		t.Fatalf("RecordSOPStepEvent: expected 201, got %d: %s", eventW.Code, eventW.Body.String())
	}
	var recordedEvent SquadSOPEventResponse
	if err := json.NewDecoder(eventW.Body).Decode(&recordedEvent); err != nil {
		t.Fatalf("decode SOP event: %v", err)
	}
	if recordedEvent.CreatedByType == "agent" {
		t.Fatalf("SOP event actor trusted spoofed request payload: %#v", recordedEvent)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT current_step_key, status FROM squad_sop_run WHERE id = $1
	`, runID).Scan(&currentStep, &runStatus); err != nil {
		t.Fatalf("load SOP run after evidence event: %v", err)
	}
	if currentStep != "acceptance" || runStatus != "进行中" {
		t.Fatalf("SOP run changed after 测试结果 evidence: step=%s status=%s, want acceptance/进行中", currentStep, runStatus)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_trace_event (
			workspace_id, task_id, issue_id, squad_id, agent_id, runtime_id,
			source, event_type, event_name, status, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			failure_reason, error_type, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			'squad_sop', 'squad.leader.completed', '编码小队队长任务完成',
			'completed', 'codex', 'gpt-5.3-codex-spark',
			36, 19, 5, 7, '无', '', '{}'::jsonb
		)
	`, testWorkspaceID, taskID, created.ID, squadID, leaderID, runtimeID); err != nil {
		t.Fatalf("insert leader trace: %v", err)
	}

	summaryReq := newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/observability/summary", nil)
	summaryReq = withURLParam(summaryReq, "id", testWorkspaceID)
	summaryW := httptest.NewRecorder()
	testHandler.GetWorkspaceObservabilitySummary(summaryW, summaryReq)
	if summaryW.Code != http.StatusOK {
		t.Fatalf("GetWorkspaceObservabilitySummary: expected 200, got %d: %s", summaryW.Code, summaryW.Body.String())
	}

	agentSummaryReq := newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/observability/summary?agent_id="+leaderID, nil)
	agentSummaryReq = withURLParam(agentSummaryReq, "id", testWorkspaceID)
	agentSummaryW := httptest.NewRecorder()
	testHandler.GetWorkspaceObservabilitySummary(agentSummaryW, agentSummaryReq)
	if agentSummaryW.Code != http.StatusOK {
		t.Fatalf("GetWorkspaceObservabilitySummary(agent): expected 200, got %d: %s", agentSummaryW.Code, agentSummaryW.Body.String())
	}
	var agentSummary map[string]any
	if err := json.NewDecoder(agentSummaryW.Body).Decode(&agentSummary); err != nil {
		t.Fatalf("decode agent summary: %v", err)
	}
	agentMetrics := agentSummary["指标"].(map[string]any)
	if got := agentMetrics["输入 token"]; got != float64(36) {
		t.Fatalf("agent 输入 token = %v, want 36", got)
	}
	if got := agentMetrics["预估成本"]; got == nil || got == float64(0) {
		t.Fatalf("agent 预估成本 = %v, want > 0", got)
	}
}

func TestWorkspaceObservabilitySummaryFiltersSOPByProject(t *testing.T) {
	ctx := context.Background()

	var leaderID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&leaderID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, '{"profile_key":"project-filter-test","steps":[{"key":"clarify","name":"需求澄清","role_key":"captain"}]}'::jsonb)
		RETURNING id
	`, testWorkspaceID, "Project Filter Squad", leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	defer testPool.Exec(ctx, `DELETE FROM squad WHERE id = $1`, squadID)

	var projectA string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, "观测项目 A").Scan(&projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	defer testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectA)

	var projectB string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, "观测项目 B").Scan(&projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	defer testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectB)

	createProjectIssue := func(title, projectID string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":         title,
			"project_id":    projectID,
			"assignee_type": "squad",
			"assignee_id":   squadID,
		})
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateIssue(%s): expected 201, got %d: %s", title, w.Code, w.Body.String())
		}
		var created IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
		t.Cleanup(func() {
			cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
			cleanupReq = withURLParam(cleanupReq, "id", created.ID)
			testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
		})
		return created.ID
	}

	issueA := createProjectIssue("项目 A 小队观测", projectA)
	_ = createProjectIssue("项目 B 小队观测", projectB)

	var runCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM squad_sop_run WHERE issue_id = $1 AND squad_id = $2
	`, issueA, squadID).Scan(&runCount); err != nil {
		t.Fatalf("count project A runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("project A run count = %d, want 1", runCount)
	}

	summaryReq := newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/observability/summary?project_id="+projectA, nil)
	summaryReq = withURLParam(summaryReq, "id", testWorkspaceID)
	summaryW := httptest.NewRecorder()
	testHandler.GetWorkspaceObservabilitySummary(summaryW, summaryReq)
	if summaryW.Code != http.StatusOK {
		t.Fatalf("GetWorkspaceObservabilitySummary: expected 200, got %d: %s", summaryW.Code, summaryW.Body.String())
	}

	var summary map[string]any
	if err := json.NewDecoder(summaryW.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	metrics, ok := summary["指标"].(map[string]any)
	if !ok {
		t.Fatalf("summary missing 指标: %#v", summary)
	}
	if got := metrics["SOP 执行数"]; got != float64(1) {
		t.Fatalf("SOP 执行数 = %v, want 1", got)
	}
	if got := metrics["SOP 事件数"]; got != float64(1) {
		t.Fatalf("SOP 事件数 = %v, want 1", got)
	}
}

func TestBuildObservabilitySummaryIncludesCostBreakdown(t *testing.T) {
	runtimeID := parseUUID("00000000-0000-0000-0000-000000000001")
	traces := []db.TaskTraceEvent{
		{
			RuntimeID:        runtimeID,
			Provider:         "codex",
			Model:            "gpt-5.3-codex-spark",
			InputTokens:      36,
			OutputTokens:     19,
			CacheReadTokens:  5,
			CacheWriteTokens: 7,
		},
		{
			Provider:     "unknown",
			Model:        "unpriced-model",
			InputTokens:  10,
			OutputTokens: 5,
		},
	}
	summary := buildObservabilitySummary(nil, traces, 0, observabilitySummarySampleLimit)
	metricsMap := summary["指标"].(map[string]any)
	if cost, ok := metricsMap["预估成本"].(float64); !ok || cost <= 0 {
		t.Fatalf("预估成本 = %v, want > 0", metricsMap["预估成本"])
	}
	modelRows := summary["model_breakdown"].([]map[string]any)
	if len(modelRows) < 2 || modelRows[0]["名称"] != "gpt-5.3-codex-spark" || modelRows[0]["价格已知"] != true {
		t.Fatalf("model_breakdown = %#v", modelRows)
	}
	runtimeRows := summary["runtime_breakdown"].([]map[string]any)
	if len(runtimeRows) == 0 || runtimeRows[0]["runtime"] != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("runtime_breakdown = %#v", runtimeRows)
	}
	unpriced := metricsMap["缺少模型价格"].([]map[string]any)
	if len(unpriced) != 1 || unpriced[0]["原因"] != "unknown/unpriced-model" {
		t.Fatalf("缺少模型价格 = %#v", unpriced)
	}
}

func TestBuildObservabilitySummaryDedupesRepeatedUsageReports(t *testing.T) {
	taskID := parseUUID("00000000-0000-0000-0000-000000000101")
	runtimeID := parseUUID("00000000-0000-0000-0000-000000000102")
	base := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC)
	traces := []db.TaskTraceEvent{
		{
			TaskID:           taskID,
			RuntimeID:        runtimeID,
			EventType:        "llm.usage_reported",
			Provider:         "codex",
			Model:            "gpt-5.3-codex-spark",
			InputTokens:      10,
			OutputTokens:     5,
			CacheReadTokens:  1,
			CacheWriteTokens: 1,
			CreatedAt:        pgtype.Timestamptz{Time: base, Valid: true},
		},
		{
			TaskID:           taskID,
			RuntimeID:        runtimeID,
			EventType:        "llm.usage_reported",
			Provider:         "codex",
			Model:            "gpt-5.3-codex-spark",
			InputTokens:      30,
			OutputTokens:     15,
			CacheReadTokens:  3,
			CacheWriteTokens: 2,
			CreatedAt:        pgtype.Timestamptz{Time: base.Add(time.Minute), Valid: true},
		},
	}
	summary := buildObservabilitySummary(nil, traces, 0, observabilitySummarySampleLimit)
	metricsMap := summary["指标"].(map[string]any)
	if got := metricsMap["输入 token"]; got != int64(30) {
		t.Fatalf("输入 token = %v, want latest usage report 30", got)
	}
	if got := metricsMap["输出 token"]; got != int64(15) {
		t.Fatalf("输出 token = %v, want latest usage report 15", got)
	}
	if got := metricsMap["缓存读 token"]; got != int64(3) {
		t.Fatalf("缓存读 token = %v, want latest usage report 3", got)
	}
	if got := summary["task_trace_total"]; got != 2 {
		t.Fatalf("task_trace_total = %v, want raw trace count 2", got)
	}
	modelRows := summary["model_breakdown"].([]map[string]any)
	if len(modelRows) != 1 || modelRows[0]["输入 token"] != int64(30) || modelRows[0]["任务数"] != 1 {
		t.Fatalf("model_breakdown = %#v, want deduped latest usage only", modelRows)
	}
}

func TestBuildObservabilitySummaryMarksPossibleTruncation(t *testing.T) {
	traces := make([]db.TaskTraceEvent, observabilitySummarySampleLimit)
	summary := buildObservabilitySummary(nil, traces, 12, observabilitySummarySampleLimit)
	metricsMap := summary["指标"].(map[string]any)
	if got := summary["task_trace_maybe_truncated"]; got != true {
		t.Fatalf("task_trace_maybe_truncated = %v, want true", got)
	}
	if got := summary["task_trace_sample_total"]; got != observabilitySummarySampleLimit {
		t.Fatalf("task_trace_sample_total = %v, want %d", got, observabilitySummarySampleLimit)
	}
	if got := metricsMap["汇总完整性"]; got != "可能截断" {
		t.Fatalf("汇总完整性 = %v, want 可能截断", got)
	}
	completeness := summary["summary_completeness"].(map[string]any)
	if got := completeness["说明"]; !strings.Contains(got.(string), "最近样本") {
		t.Fatalf("summary_completeness 说明 = %v, want 最近样本 warning", got)
	}
}
