package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestReportTaskUsagePreservesExecutedAt 固定历史补齐的入账语义：
//   - 受控入口可以带上真实执行时间，落库写进 created_at，而不是补齐当天；
//   - 已有行更正只更新数值与 updated_at，原始 created_at 保留；
//   - 将来时间、早于任务创建的时间都拒绝——宁可失败，也不写一条无法解释的行；
//   - 写入后回读核对，数值不一致就不返回成功。
func TestReportTaskUsagePreservesExecutedAt(t *testing.T) {
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("fetch runtime: %v", err)
	}
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("fetch agent: %v", err)
	}
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type)
		VALUES ($1, 'usage executed_at test', $2, 'member') RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, created_at)
		VALUES ($1, $2, $3, 'completed', now() - interval '3 hours') RETURNING id
	`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM task_usage WHERE task_id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	post := func(body any) *httptest.ResponseRecorder {
		req := withURLParam(newRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/usage", body), "taskId", taskID)
		rec := httptest.NewRecorder()
		testHandler.ReportTaskUsage(rec, req)
		return rec
	}

	executed := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	rec := post(map[string]any{"usage": []map[string]any{{
		"provider": "codebuddy", "model": "deepseek-v4.1-flash",
		"input_tokens": 10, "output_tokens": 20, "cache_read_tokens": 30, "cache_write_tokens": 40,
		"executed_at": executed.Format(time.RFC3339),
	}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("report with executed_at: got %d body=%s", rec.Code, rec.Body.String())
	}
	var createdAt time.Time
	if err := testPool.QueryRow(ctx, `
		SELECT created_at FROM task_usage WHERE task_id = $1 AND provider = 'codebuddy' AND model = 'deepseek-v4.1-flash'
	`, taskID).Scan(&createdAt); err != nil {
		t.Fatalf("load created_at: %v", err)
	}
	if diff := createdAt.Sub(executed); diff > time.Second || diff < -time.Second {
		t.Fatalf("created_at must keep the real execution time, got %s want %s", createdAt, executed)
	}

	// 更正同一行：数值更新，created_at 保持原值。
	later := executed.Add(30 * time.Minute)
	rec = post(map[string]any{"usage": []map[string]any{{
		"provider": "codebuddy", "model": "deepseek-v4.1-flash",
		"input_tokens": 11, "output_tokens": 21, "cache_read_tokens": 31, "cache_write_tokens": 41,
		"executed_at": later.Format(time.RFC3339),
	}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("correction: got %d body=%s", rec.Code, rec.Body.String())
	}
	var input int64
	if err := testPool.QueryRow(ctx, `
		SELECT input_tokens, created_at FROM task_usage
		 WHERE task_id = $1 AND provider = 'codebuddy' AND model = 'deepseek-v4.1-flash'
	`, taskID).Scan(&input, &createdAt); err != nil {
		t.Fatalf("reload usage: %v", err)
	}
	if input != 11 {
		t.Fatalf("correction must update tokens, got %d", input)
	}
	if diff := createdAt.Sub(executed); diff > time.Second || diff < -time.Second {
		t.Fatalf("correction must not rewrite created_at, got %s want %s", createdAt, executed)
	}

	// 将来时间：拒绝。
	rec = post(map[string]any{"usage": []map[string]any{{
		"provider": "codebuddy", "model": "deepseek-v4.1-flash", "input_tokens": 1,
		"executed_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
	}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("future executed_at must be rejected, got %d", rec.Code)
	}

	// 早于任务创建：拒绝（来源不是这次执行）。
	rec = post(map[string]any{"usage": []map[string]any{{
		"provider": "codebuddy", "model": "deepseek-v4.1-flash", "input_tokens": 1,
		"executed_at": time.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339),
	}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("executed_at before the task must be rejected, got %d", rec.Code)
	}
}
