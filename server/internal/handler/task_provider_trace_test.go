package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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
	t.Cleanup(func() {
		// 本表不建外键/级联：任务行删掉不会带走关联，测试自己按应用层同样的方式清理。
		testPool.Exec(ctx, `DELETE FROM task_provider_trace WHERE task_id = $1`, taskID)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

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

	// 重复上报：行数不变。已有 exact 的 session 不被后到的不同值改写——那是"冲突"，
	// 不是"更新"；两个值都记进 metadata 供审计。
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "session_id": "sess-1-renamed", "provider_trace_id": "trace-a", "observation_space": "codebuddy-model-request-prod"},
	}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting exact report must return 409: got %d body=%s", rec.Code, rec.Body.String())
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
	if session != "sess-1" || space != "" || correlation != "exact" {
		t.Fatalf("exact session must not be rewritten by a conflicting report: session=%q space=%q correlation=%q", session, space, correlation)
	}
	var conflict string
	if err := testPool.QueryRow(ctx, `
		SELECT metadata->>'session_conflict' FROM task_provider_trace
		 WHERE task_id = $1 AND provider_trace_id = 'trace-a'
	`, taskID).Scan(&conflict); err != nil {
		t.Fatalf("read conflict record: %v", err)
	}
	if conflict == "" {
		t.Fatalf("conflicting session must be recorded, not silently dropped")
	}

	// 弱来源不能把已有 exact 降级：correlation/source/session 都保持原样。
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "session_id": "sess-9", "provider_trace_id": "trace-a", "correlation": "inferred_window", "source": "agentlens_query"},
	}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("downgrade attempt: got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := testPool.QueryRow(ctx, `
		SELECT session_id, correlation, source FROM task_provider_trace
		 WHERE task_id = $1 AND provider_trace_id = 'trace-a'
	`, taskID).Scan(&session, &correlation, &space); err != nil {
		t.Fatalf("reload trace-a: %v", err)
	}
	if correlation != "exact" || space != "stream_json" || session != "sess-1" {
		t.Fatalf("inferred_window must not downgrade exact: correlation=%q source=%q session=%q", correlation, space, session)
	}

	// 反过来可以升级：先落一条 inferred_window，再用 exact 覆盖成精确关联。
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "session_id": "sess-exact-2", "provider_trace_id": "trace-b", "correlation": "exact", "source": "stream_json"},
	}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upgrade attempt: got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := testPool.QueryRow(ctx, `
		SELECT session_id, correlation, source FROM task_provider_trace
		 WHERE task_id = $1 AND provider_trace_id = 'trace-b'
	`, taskID).Scan(&session, &correlation, &space); err != nil {
		t.Fatalf("reload trace-b: %v", err)
	}
	if correlation != "exact" || space != "stream_json" || session != "sess-exact-2" {
		t.Fatalf("exact must upgrade inferred_window: correlation=%q source=%q session=%q", correlation, space, session)
	}

	// 精确来源没自报 session 时，升级不得把历史猜测当成精确 session。
	for _, item := range []map[string]any{
		{"provider": "codebuddy", "provider_trace_id": "trace-c", "session_id": "guessed-session", "correlation": "inferred_window", "source": "agentlens_query"},
		{"provider": "codebuddy", "provider_trace_id": "trace-c", "correlation": "exact", "source": "stream_json"},
	} {
		if rec := post(map[string]any{"traces": []map[string]any{item}}); rec.Code != http.StatusNoContent {
			t.Fatalf("unknown exact session: got %d body=%s", rec.Code, rec.Body.String())
		}
	}
	if err := testPool.QueryRow(ctx, `SELECT session_id, correlation FROM task_provider_trace
		WHERE task_id=$1 AND provider_trace_id='trace-c'`, taskID).Scan(&session, &correlation); err != nil {
		t.Fatal(err)
	}
	if session != "" || correlation != "exact" {
		t.Fatalf("unknown exact session must stay unknown: session=%q correlation=%q", session, correlation)
	}

	// 整批校验：一条非法 correlation 让整个请求 400，同一批里的合法条目也不能先落库。
	before, _ := countRows()
	rec = post(map[string]any{"traces": []map[string]any{
		{"provider": "codebuddy", "provider_trace_id": "trace-d"},
		{"provider": "codebuddy", "provider_trace_id": "trace-e", "correlation": "guessed"},
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid correlation: got %d body=%s", rec.Code, rec.Body.String())
	}
	if after, _ := countRows(); after != before {
		t.Fatalf("a rejected batch must not persist any of its rows: before=%d after=%d", before, after)
	}

	// 执行日志端点把链路挂回任务行，0..N 条都要能读出来。
	taskList := []AgentTaskResponse{{ID: taskID}}
	testHandler.hydrateTaskTraces(ctx, parseUUID(issueID), taskList)
	if len(taskList[0].Traces) != 3 {
		t.Fatalf("expected 3 hydrated traces, got %#v", taskList[0].Traces)
	}
	for _, tr := range taskList[0].Traces {
		if tr.ProviderTraceID == "" || tr.Provider != "codebuddy" {
			t.Fatalf("unexpected hydrated trace: %#v", tr)
		}
	}

	// 本表不建外键/级联，任务行删除必须由应用层同一事务里的 DeleteTaskBatch 带走关联。
	// 这里走真实查询而不是手写 DELETE，验证清理确实挂在既有删除路径上。
	if err := testHandler.Queries.DeleteTaskBatch(ctx, []pgtype.UUID{parseUUID(taskID)}); err != nil {
		t.Fatalf("delete task batch: %v", err)
	}
	if total, _ := countRows(); total != 0 {
		t.Fatalf("deleting the task must remove its trace links, %d rows left", total)
	}
}

func TestTaskProviderSDKTraceProvenance(t *testing.T) {
	ctx := context.Background()
	agent := dbfx.Agent(t, "SDK trace fixture", testRuntimeID)
	issue := dbfx.Issue(t, "SDK trace fixture")
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": issue, "status": "completed"})
	base := map[string]any{"provider": "codebuddy", "provider_trace_id": "actual-cid", "session_id": "actual-session", "correlation": "exact", "source": "local_session_jsonl", "sdk_trace_id": strings.Repeat("a", 32), "sdk_trace_source_sha256": strings.Repeat("b", 64), "sdk_trace_source_path": "providerData.traceId"}
	copyPayload := func() map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	post := func(rows ...map[string]any) *httptest.ResponseRecorder {
		return testutil.Call(t, testHandler.ReportTaskTraces, withURLParam(newRequest(http.MethodPost, "/traces", map[string]any{"traces": rows}), "taskId", task)).ResponseRecorder
	}
	for i := 0; i < 2; i++ {
		if r := post(base); r.Code != 204 {
			t.Fatalf("write/replay: %d %s", r.Code, r.Body.String())
		}
	}
	load := func() map[string]any {
		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT metadata FROM task_provider_trace WHERE task_id=$1 AND provider_trace_id='actual-cid'`, task).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, key := range []string{"sdk_trace_id", "sdk_trace_source_sha256", "sdk_trace_source_path"} {
		if load()[key] != base[key] {
			t.Fatalf("metadata missing %s", key)
		}
	}
	rows := []AgentTaskResponse{{ID: task}}
	testHandler.hydrateTaskTraces(ctx, parseUUID(issue), rows)
	if len(rows[0].Traces) != 1 || rows[0].Traces[0].SDKTraceID != base["sdk_trace_id"] || rows[0].Traces[0].SDKTraceSourceSHA256 != base["sdk_trace_source_sha256"] || rows[0].Traces[0].SDKTraceSourcePath != "providerData.traceId" {
		t.Fatalf("response missing source: %+v", rows)
	}
	legacy := map[string]any{"provider": "codebuddy", "provider_trace_id": "actual-cid", "session_id": "actual-session"}
	if r := post(legacy); r.Code != 204 {
		t.Fatalf("legacy daemon: %d %s", r.Code, r.Body.String())
	}
	for _, key := range []string{"sdk_trace_id", "sdk_trace_source_sha256"} {
		changed := copyPayload()
		changed[key] = strings.Repeat("c", len(base[key].(string)))
		other := map[string]any{"provider": "codebuddy", "provider_trace_id": "must-roll-back"}
		if r := post(other, changed); r.Code != 409 {
			t.Fatalf("conflict %s: %d %s", key, r.Code, r.Body.String())
		}
		if load()[key] != base[key] {
			t.Fatalf("conflict overwrote %s", key)
		}
		var n int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_provider_trace WHERE task_id=$1 AND provider_trace_id='must-roll-back'`, task).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial conflicting batch: %d %v", n, err)
		}
	}
	for _, change := range []map[string]any{{"session_id": ""}, {"provider": "codex"}, {"correlation": "inferred_window"}, {"source": "stream_json"}, {"sdk_trace_id": "bad"}, {"sdk_trace_source_sha256": "bad"}, {"sdk_trace_source_path": "_meta.traceparent"}} {
		bad := copyPayload()
		for k, v := range change {
			bad[k] = v
		}
		if r := post(bad); r.Code != 400 {
			t.Fatalf("invalid provenance %v: %d %s", change, r.Code, r.Body.String())
		}
	}
	wrongSession := copyPayload()
	wrongSession["session_id"] = "wrong-session"
	if r := post(wrongSession); r.Code != 409 {
		t.Fatalf("wrong SDK session: %d %s", r.Code, r.Body.String())
	}
	newSDK := copyPayload()
	newSDK["provider_trace_id"] = "mixed-new-cid"
	oldWrongSession := map[string]any{"provider": "codebuddy", "provider_trace_id": "actual-cid", "session_id": "wrong-session"}
	if r := post(newSDK, oldWrongSession); r.Code != 409 {
		t.Fatalf("mixed session conflict: %d %s", r.Code, r.Body.String())
	}
	var mixedRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_provider_trace WHERE task_id=$1 AND provider_trace_id='mixed-new-cid'`, task).Scan(&mixedRows); err != nil || mixedRows != 0 {
		t.Fatalf("mixed conflict persisted new SDK association: %d %v", mixedRows, err)
	}
	if load()["session_conflict"] == nil || load()["sdk_trace_id"] != base["sdk_trace_id"] {
		t.Fatalf("mixed rollback lost existing evidence: %+v", load())
	}
	legacy["session_id"] = "conflicting-session"
	if r := post(legacy); r.Code != 409 {
		t.Fatalf("session conflict: %d %s", r.Code, r.Body.String())
	}
	if load()["session_conflict"] == nil || load()["sdk_trace_id"] != base["sdk_trace_id"] {
		t.Fatalf("lost independent conflict audit: %+v", load())
	}
	beforeAudit, _ := json.Marshal(load())
	if err := testHandler.Queries.RecordTaskProviderSessionConflict(ctx, db.RecordTaskProviderSessionConflictParams{
		TaskID: parseUUID(task), Provider: "codebuddy", ProviderTraceID: "actual-cid", SessionID: "incoming-stale", ExpectedSessionID: "superseded-root",
	}); err != nil {
		t.Fatal(err)
	}
	afterAudit, _ := json.Marshal(load())
	if string(beforeAudit) != string(afterAudit) {
		t.Fatal("stale original session rewrote conflict audit")
	}
	// 同一CID同时补不同SDK身份：一个成功，一个明确409，不能相互覆盖。
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, id := range []string{strings.Repeat("d", 32), strings.Repeat("e", 32)} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			p := copyPayload()
			p["provider_trace_id"] = "concurrent-cid"
			p["sdk_trace_id"] = id
			req := withURLParam(newRequest(http.MethodPost, "/traces", map[string]any{"traces": []map[string]any{p}}), "taskId", task)
			r := httptest.NewRecorder()
			testHandler.ReportTaskTraces(r, req)
			codes <- r.Code
		}(id)
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for code := range codes {
		count[code]++
	}
	if count[204] != 1 || count[409] != 1 {
		t.Fatalf("concurrent source changed silently: %v", count)
	}
}
