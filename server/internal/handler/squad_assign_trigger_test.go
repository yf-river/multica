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
		"task_id":         taskID,
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
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		VALUES ($1, 'codex', 'gpt-5.3-codex-spark', 120, 45, 9, 3)
		ON CONFLICT (task_id, provider, model) DO UPDATE SET
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens
	`, taskID); err != nil {
		t.Fatalf("insert task usage: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, content)
		VALUES
			($1, 9001, 'thinking', '分析阶段输入'),
			($1, 9002, 'text', '输出阶段结论'),
			($1, 9003, 'tool_result', '工具结果')
		ON CONFLICT DO NOTHING
	`, taskID); err != nil {
		t.Fatalf("insert task messages: %v", err)
	}
	metricsReq := newRequest("GET", "/api/issues/"+created.ID+"/sop-runs?workspace_id="+testWorkspaceID, nil)
	metricsReq = withURLParam(metricsReq, "id", created.ID)
	metricsW := httptest.NewRecorder()
	testHandler.ListIssueSOPRuns(metricsW, metricsReq)
	if metricsW.Code != http.StatusOK {
		t.Fatalf("ListIssueSOPRuns(metrics): expected 200, got %d: %s", metricsW.Code, metricsW.Body.String())
	}
	var metricsResp struct {
		Items []SquadSOPRunResponse `json:"items"`
	}
	if err := json.NewDecoder(metricsW.Body).Decode(&metricsResp); err != nil {
		t.Fatalf("decode SOP metrics response: %v", err)
	}
	if len(metricsResp.Items) == 0 {
		t.Fatalf("expected SOP run in metrics response")
	}
	stageMetrics, _ := metricsResp.Items[0].Metrics["阶段指标"].([]any)
	if len(stageMetrics) == 0 {
		t.Fatalf("missing 阶段指标 in SOP metrics: %+v", metricsResp.Items[0].Metrics)
	}
	var acceptance map[string]any
	for _, raw := range stageMetrics {
		stage, _ := raw.(map[string]any)
		if stage["step_key"] == "acceptance" {
			acceptance = stage
			break
		}
	}
	if acceptance == nil {
		t.Fatalf("acceptance stage metrics missing: %+v", stageMetrics)
	}
	if got := acceptance["duration_ms"]; got != float64(123) {
		t.Fatalf("acceptance duration_ms = %v, want 123", got)
	}
	if got := acceptance["input_tokens"]; got != float64(120) {
		t.Fatalf("acceptance input_tokens = %v, want 120", got)
	}
	if got := acceptance["output_tokens"]; got != float64(45) {
		t.Fatalf("acceptance output_tokens = %v, want 45", got)
	}
	if got := acceptance["agent_turn_count"]; got != float64(2) {
		t.Fatalf("acceptance agent_turn_count = %v, want 2", got)
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
			'squad_sop', 'llm.usage_reported', '模型用量已上报',
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
	stageRows, _ := agentSummary["sop_stage_breakdown"].([]any)
	if len(stageRows) == 0 {
		t.Fatalf("agent summary missing sop_stage_breakdown: %#v", agentSummary)
	}
	var summaryAcceptance map[string]any
	for _, raw := range stageRows {
		row, _ := raw.(map[string]any)
		if row["step_key"] == "acceptance" {
			summaryAcceptance = row
			break
		}
	}
	if summaryAcceptance == nil {
		t.Fatalf("acceptance stage missing from sop_stage_breakdown: %#v", stageRows)
	}
	if got := summaryAcceptance["duration_ms"]; got != float64(123) {
		t.Fatalf("summary acceptance duration_ms = %v, want 123", got)
	}
	if got := summaryAcceptance["input_tokens"]; got != float64(36) {
		t.Fatalf("summary acceptance input_tokens = %v, want 36", got)
	}
	if got := summaryAcceptance["agent_turn_count"]; got != float64(2) {
		t.Fatalf("summary acceptance agent_turn_count = %v, want 2", got)
	}
}

func TestCompleteTaskClosesSquadSOPRun(t *testing.T) {
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id, instructions
		)
		VALUES ($1, 'pm', 'sop sync fixture', 'local', '{}'::jsonb, $2, 'personal', 1, $3, '')
		RETURNING id
	`, testWorkspaceID, testRuntimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, '{"profile_key":"sop-sync-test","steps":[{"key":"pm","name":"pm","role_key":"pm"},{"key":"05-verify","name":"05-测试","role_key":"05-verify"}]}'::jsonb)
		RETURNING id
	`, testWorkspaceID, "SOP Sync Squad", agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID) })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "SOP run closes on leader complete",
		"status":        "todo",
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
	t.Cleanup(func() {
		cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
		cleanupReq = withURLParam(cleanupReq, "id", created.ID)
		testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
	})

	var taskID, runID string
	if err := testPool.QueryRow(ctx, `
		SELECT atq.id::text, ssr.id::text
		FROM agent_task_queue atq
		JOIN squad_sop_run ssr ON ssr.issue_id = atq.issue_id
		WHERE atq.issue_id = $1 AND atq.agent_id = $2
		ORDER BY atq.created_at DESC
		LIMIT 1
	`, created.ID, agentID).Scan(&taskID, &runID); err != nil {
		t.Fatalf("load task/run: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'dispatched' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("mark task dispatched: %v", err)
	}

	startW := httptest.NewRecorder()
	startReq := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/start", nil, testWorkspaceID, "sop-sync-daemon")
	startReq = withURLParam(startReq, "taskId", taskID)
	testHandler.StartTask(startW, startReq)
	if startW.Code != http.StatusOK {
		t.Fatalf("StartTask: expected 200, got %d: %s", startW.Code, startW.Body.String())
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("mark issue done: %v", err)
	}

	completeW := httptest.NewRecorder()
	completeReq := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", map[string]any{
		"output": "verify passed",
	}, testWorkspaceID, "sop-sync-daemon")
	completeReq = withURLParam(completeReq, "taskId", taskID)
	testHandler.CompleteTask(completeW, completeReq)
	if completeW.Code != http.StatusOK {
		t.Fatalf("CompleteTask: expected 200, got %d: %s", completeW.Code, completeW.Body.String())
	}

	var status, currentStep string
	var completedAt *time.Time
	if err := testPool.QueryRow(ctx, `
		SELECT status, current_step_key, completed_at
		FROM squad_sop_run
		WHERE id = $1
	`, runID).Scan(&status, &currentStep, &completedAt); err != nil {
		t.Fatalf("load SOP run: %v", err)
	}
	if status != "已完成" || currentStep != "pm" || completedAt == nil {
		t.Fatalf("SOP run = status %s step %s completed_at %v, want 已完成/pm/non-nil", status, currentStep, completedAt)
	}
	var completedEvents int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM squad_sop_step_event
		WHERE run_id = $1 AND task_id = $2 AND step_key = 'pm' AND event_type = '步骤完成'
	`, runID, taskID).Scan(&completedEvents); err != nil {
		t.Fatalf("count completed events: %v", err)
	}
	if completedEvents != 1 {
		t.Fatalf("completed event count = %d, want 1", completedEvents)
	}
}

func TestFailTaskDoesNotCloseSquadSOPRunWhenIssueHasActiveContinuation(t *testing.T) {
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id, instructions
		)
		VALUES ($1, 'pm', 'sop failure continuation fixture', 'local', '{}'::jsonb, $2, 'personal', 1, $3, '')
		RETURNING id
	`, testWorkspaceID, testRuntimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, sop_profile)
		VALUES ($1, $2, '', $3, $4, '{"profile_key":"sop-failure-continuation-test","steps":[{"key":"pm","name":"pm","role_key":"pm"},{"key":"05-verify","name":"05-测试","role_key":"05-verify"}]}'::jsonb)
		RETURNING id
	`, testWorkspaceID, "SOP Failure Continuation Squad", agentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID) })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "SOP run stays open when continuation exists",
		"status":        "in_progress",
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
	t.Cleanup(func() {
		cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
		cleanupReq = withURLParam(cleanupReq, "id", created.ID)
		testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
	})

	var taskID, runID string
	if err := testPool.QueryRow(ctx, `
		SELECT atq.id::text, ssr.id::text
		FROM agent_task_queue atq
		JOIN squad_sop_run ssr ON ssr.issue_id = atq.issue_id
		WHERE atq.issue_id = $1 AND atq.agent_id = $2
		ORDER BY atq.created_at DESC
		LIMIT 1
	`, created.ID, agentID).Scan(&taskID, &runID); err != nil {
		t.Fatalf("load task/run: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'dispatched' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("mark task dispatched: %v", err)
	}
	startW := httptest.NewRecorder()
	startReq := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/start", nil, testWorkspaceID, "sop-failure-continuation-daemon")
	startReq = withURLParam(startReq, "taskId", taskID)
	testHandler.StartTask(startW, startReq)
	if startW.Code != http.StatusOK {
		t.Fatalf("StartTask: expected 200, got %d: %s", startW.Code, startW.Body.String())
	}

	var continuationTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, testRuntimeID, created.ID).Scan(&continuationTaskID); err != nil {
		t.Fatalf("create continuation task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, continuationTaskID)
	})

	failW := httptest.NewRecorder()
	failReq := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/fail", map[string]any{
		"error":          "provider failed after queuing continuation",
		"failure_reason": "api_invalid_request",
	}, testWorkspaceID, "sop-failure-continuation-daemon")
	failReq = withURLParam(failReq, "taskId", taskID)
	testHandler.FailTask(failW, failReq)
	if failW.Code != http.StatusOK {
		t.Fatalf("FailTask: expected 200, got %d: %s", failW.Code, failW.Body.String())
	}

	var status, currentStep string
	var completedAt *time.Time
	if err := testPool.QueryRow(ctx, `
		SELECT status, current_step_key, completed_at
		FROM squad_sop_run
		WHERE id = $1
	`, runID).Scan(&status, &currentStep, &completedAt); err != nil {
		t.Fatalf("load SOP run: %v", err)
	}
	if status != "进行中" || currentStep != "pm" || completedAt != nil {
		t.Fatalf("SOP run = status %s step %s completed_at %v, want 进行中/pm/nil", status, currentStep, completedAt)
	}
	var failedEvents int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM squad_sop_step_event
		WHERE run_id = $1 AND task_id = $2 AND event_type = '步骤失败'
	`, runID, taskID).Scan(&failedEvents); err != nil {
		t.Fatalf("count failed events: %v", err)
	}
	if failedEvents != 0 {
		t.Fatalf("failed event count = %d, want 0 while continuation task is active", failedEvents)
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
	summary := buildObservabilitySummary(nil, nil, traces, nil, 0, 0)
	metricsMap := summary["指标"].(map[string]any)
	if cost, ok := metricsMap["预估成本"].(float64); !ok || cost <= 0 {
		t.Fatalf("预估成本 = %v, want > 0", metricsMap["预估成本"])
	}
	modelRows := summary["model_breakdown"].([]map[string]any)
	if len(modelRows) < 2 || modelRows[0]["名称"] != "openai/gpt-5.3-codex-spark" || modelRows[0]["价格已知"] != true {
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
	summary := buildObservabilitySummary(nil, nil, traces, nil, 0, 0)
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

func TestBuildObservabilitySummaryMarksFullCompleteness(t *testing.T) {
	traces := make([]db.TaskTraceEvent, observabilitySummaryPageSize)
	summary := buildObservabilitySummary(nil, nil, traces, nil, int64(len(traces)), 0)
	metricsMap := summary["指标"].(map[string]any)
	if got := summary["task_trace_maybe_truncated"]; got != false {
		t.Fatalf("task_trace_maybe_truncated = %v, want false", got)
	}
	if got := summary["task_trace_sample_total"]; got != observabilitySummaryPageSize {
		t.Fatalf("task_trace_sample_total = %v, want %d", got, observabilitySummaryPageSize)
	}
	if got := metricsMap["汇总完整性"]; got != "完整" {
		t.Fatalf("汇总完整性 = %v, want 完整", got)
	}
	completeness := summary["summary_completeness"].(map[string]any)
	if got := completeness["说明"]; !strings.Contains(got.(string), "全量汇总") {
		t.Fatalf("summary_completeness 说明 = %v, want 全量汇总", got)
	}
}
