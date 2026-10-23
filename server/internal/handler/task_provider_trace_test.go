package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReportTaskTracesPersistsAndIsIdempotent 覆盖链路关联的写入契约：
//   - 一条执行可以写入 0..N 条链路；
//   - 重复上报（daemon 重试、重启重放）不产生重复行；
//   - 空 trace id 不写库，也不报错——造一行空关联比缺一行更糟；
//   - 非法 correlation 直接 400，不做"猜一个"。
func TestReportTaskTracesPersistsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("fetch runtime: %v", err)
	}
	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("fetch agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type)
		VALUES ($1, 'task provider trace test', $2, 'member')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status)
		VALUES ($1, $2, $3, 'completed')
		RETURNING id
	`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	post := func(body any) *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/traces", body), "taskId", taskID)
		rec := httptest.NewRecorder()
		testHandler.ReportTaskTraces(rec, req)
		return rec
	}

	// 0 条：daemon 不会调用；直接上报空数组也必须是无害的 no-op。
	if rec := post(map[string]any{"traces": []any{}}); rec.Code != http.StatusNoContent {
		t.Fatalf("empty payload: got %d body=%s", rec.Code, rec.Body.String())
	}

	rec := post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "session_id": "sess-1", "provider_trace_id": "trace-a"},
		{"provider": "codebuddy", "session_id": "sess-1", "provider_trace_id": ""},
		{"provider": "codebuddy", "session_id": "sess-2", "provider_trace_id": "trace-b", "correlation": "inferred_window", "source": "agentlens_query"},
	}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: got %d body=%s", rec.Code, rec.Body.String())
	}

	countRows := func() (int, int) {
		var total, emptyID int
		if err := testPool.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE provider_trace_id = '')
			FROM task_provider_trace WHERE task_id = $1
		`, taskID).Scan(&total, &emptyID); err != nil {
			t.Fatalf("count traces: %v", err)
		}
		return total, emptyID
	}
	if total, emptyID := countRows(); total != 2 || emptyID != 0 {
		t.Fatalf("expected 2 stored traces and no empty id, got total=%d empty=%d", total, emptyID)
	}

	// 重复上报：行数不变，可变字段按最新值刷新。
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "session_id": "sess-1-renamed", "provider_trace_id": "trace-a", "observation_space": "codebuddy-model-request-prod"},
	}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("re-report: got %d body=%s", rec.Code, rec.Body.String())
	}
	if total, _ := countRows(); total != 2 {
		t.Fatalf("re-report must not duplicate rows, got %d", total)
	}
	var session, space, correlation string
	if err := testPool.QueryRow(ctx, `
		SELECT session_id, observation_space, correlation
		FROM task_provider_trace WHERE task_id = $1 AND provider_trace_id = 'trace-a'
	`, taskID).Scan(&session, &space, &correlation); err != nil {
		t.Fatalf("load trace-a: %v", err)
	}
	if session != "sess-1-renamed" || space != "codebuddy-model-request-prod" || correlation != "exact" {
		t.Fatalf("unexpected refresh: session=%q space=%q correlation=%q", session, space, correlation)
	}

	// 非法 correlation 必须拒绝，不能落成"看起来精确"的关联。
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "provider_trace_id": "trace-c", "correlation": "guessed"},
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid correlation: got %d body=%s", rec.Code, rec.Body.String())
	}
	if total, _ := countRows(); total != 2 {
		t.Fatalf("invalid correlation must not write, got %d rows", total)
	}

	// 执行日志端点把链路挂回任务行，0..N 条都要能读出来。
	taskList := []AgentTaskResponse{{ID: taskID}}
	testHandler.hydrateTaskTraces(ctx, parseUUID(issueID), taskList)
	if len(taskList[0].Traces) != 2 {
		t.Fatalf("expected 2 hydrated traces, got %#v", taskList[0].Traces)
	}
	for _, tr := range taskList[0].Traces {
		if tr.ProviderTraceID == "" || tr.Provider != "codebuddy" {
			t.Fatalf("unexpected hydrated trace: %#v", tr)
		}
	}
}
