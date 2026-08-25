package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/requestctx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRuntimeToResponseRejectsNonObjectMetadata(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`"string"`)} {
		if _, err := runtimeToResponse(db.AgentRuntime{Metadata: raw}); err == nil {
			t.Fatalf("runtimeToResponse metadata=%s expected an error", raw)
		}
	}
	response, err := runtimeToResponse(db.AgentRuntime{Metadata: []byte(`{"version":"1.2.3"}`)})
	if err != nil {
		t.Fatalf("runtimeToResponse object metadata: %v", err)
	}
	if response.Metadata["version"] != "1.2.3" {
		t.Fatalf("metadata = %#v", response.Metadata)
	}
}

func TestRuntimeHandlersRejectMalformedRuntimeID(t *testing.T) {
	tests := []struct {
		name, method, path string
		handle             func(http.ResponseWriter, *http.Request)
	}{
		{"usage", http.MethodGet, "/api/runtimes/not-a-uuid/usage", testHandler.GetRuntimeUsage},
		{"usage by task", http.MethodGet, "/api/runtimes/not-a-uuid/usage/by-task", testHandler.GetRuntimeUsageByTask},
		{"task activity", http.MethodGet, "/api/runtimes/not-a-uuid/task-activity", testHandler.GetRuntimeTaskActivity},
		{"delete", http.MethodDelete, "/api/runtimes/not-a-uuid", testHandler.DeleteAgentRuntime},
		{"archive-agents-and-delete", http.MethodPost, "/api/runtimes/not-a-uuid/archive-agents-and-delete", testHandler.ArchiveAgentsAndDeleteRuntime},
		{"models", http.MethodPost, "/api/runtimes/not-a-uuid/models", testHandler.InitiateListModels},
		{"local skills", http.MethodPost, "/api/runtimes/not-a-uuid/local-skills", testHandler.InitiateListLocalSkills},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest(tt.method, tt.path, nil)
			req = withURLParam(req, "runtimeId", "not-a-uuid")
			tt.handle(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400 for malformed runtimeId, got %d: %s", tt.name, w.Code, w.Body.String())
			}
		})
	}
}

func TestListAgentRuntimesClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	member, err := testHandler.getWorkspaceMember(context.Background(), testUserID, testWorkspaceID)
	if err != nil {
		t.Fatalf("load workspace member: %v", err)
	}
	ctx, cancel := context.WithCancel(requestctx.WithWorkspace(context.Background(), testWorkspaceID, member))
	cancel()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodGet, "/api/runtimes?workspace_id="+testWorkspaceID, nil).WithContext(ctx)

	testHandler.ListAgentRuntimes(w, req)

	if w.Code != 499 {
		t.Fatalf("expected 499 for canceled runtime list request, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNewUsageResponseIncludesCost(t *testing.T) {
	priced := newUsageResponse("codex", "gpt-5.3-codex-spark", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !priced.Priced {
		t.Fatalf("expected spark row to be priced")
	}
	if priced.CostUSD != 16.1 {
		t.Fatalf("cost = %v, want 16.1", priced.CostUSD)
	}
	if priced.InputCostUSD != 1.75 || priced.OutputCostUSD != 14 || priced.CacheReadCostUSD != 0.175 || priced.CacheWriteCostUSD != 0.175 {
		t.Fatalf("unexpected breakdown: %+v", priced)
	}
	encoded, err := json.Marshal(priced)
	if err != nil {
		t.Fatalf("marshal usage response: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if wire["provider"] != "codex" || wire["input_tokens"] != float64(1_000_000) || wire["cost_usd"] != 16.1 || wire["priced"] != true {
		t.Fatalf("usage response wire fields = %#v", wire)
	}

	unpriced := newUsageResponse("fictional", "unknown-model", 1_000_000, 0, 0, 0)
	if unpriced.Priced {
		t.Fatalf("expected unknown model to be unpriced")
	}
	if unpriced.CostUSD != 0 {
		t.Fatalf("unpriced cost = %v, want 0", unpriced.CostUSD)
	}
}

func TestGetRuntimeUsage_BucketsByUsageTime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
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
		VALUES ($1, 'runtime usage test', $2, 'member')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterdayLate := today.Add(-2 * time.Minute)
	todayEarly := today.Add(5 * time.Minute)
	yesterdayMorning := today.Add(-19 * time.Hour)

	insertTaskWithUsage := func(enqueueAt, usageAt time.Time, inputTokens int64) {
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, created_at)
			VALUES ($1, $2, $3, 'completed', $4)
			RETURNING id
		`, agentID, issueID, runtimeID, enqueueAt).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, 'claude', 'claude-3-5-sonnet', $2, 0, $3)
		`, taskID, inputTokens, usageAt); err != nil {
			t.Fatalf("insert task_usage: %v", err)
		}
		t.Cleanup(func() {
			mustExec(t, ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
	}

	insertTaskWithUsage(yesterdayLate, todayEarly, 1000)          // cross-midnight
	insertTaskWithUsage(yesterdayMorning, yesterdayMorning, 2000) // full-day yesterday

	if _, err := testPool.Exec(ctx, `
		SELECT rollup_task_usage_hourly_window('-infinity'::timestamptz, 'infinity'::timestamptz)
	`); err != nil {
		t.Fatalf("rollup window: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `
			DELETE FROM task_usage_hourly
			 WHERE runtime_id = $1
			   AND DATE(bucket_hour AT TIME ZONE 'UTC') IN ($2::date, $3::date)
		`, runtimeID, today, today.Add(-24*time.Hour))
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/runtimes/"+runtimeID+"/usage?days=1", nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.GetRuntimeUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetRuntimeUsage: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []runtimeUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byDate := make(map[string]int64)
	for _, r := range resp {
		byDate[r.Date] += r.InputTokens
	}

	todayKey := today.Format("2006-01-02")
	yesterdayKey := today.Add(-24 * time.Hour).Format("2006-01-02")

	if byDate[todayKey] != 1000 {
		t.Errorf("cross-midnight task: today bucket expected 1000 input tokens, got %d (full map: %v)", byDate[todayKey], byDate)
	}
	if byDate[yesterdayKey] != 2000 {
		t.Errorf("yesterday morning task: yesterday bucket expected 2000 input tokens, got %d (full map: %v)", byDate[yesterdayKey], byDate)
	}
}

func TestGetRuntimeUsageByTask_GroupsByTaskAndModel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	runtimeID := handlerTestRuntimeID(t)
	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("fetch agent: %v", err)
	}

	issueNumber := int(time.Now().UnixNano() % 1_000_000_000)
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type, number)
		VALUES ($1, 'runtime by task usage', $2, 'member', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	now := time.Now().UTC()
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, started_at, completed_at, created_at)
		VALUES ($1, $2, $3, 'completed', $4, $5, $4)
		RETURNING id
	`, agentID, issueID, runtimeID, now.Add(-time.Minute), now).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	insertUsage := func(provider, model string, input, output int64, createdAt time.Time) {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, taskID, provider, model, input, output, createdAt); err != nil {
			t.Fatalf("insert task_usage: %v", err)
		}
	}
	insertUsage("anthropic", "task-model-a", 1500, 15, now)
	insertUsage("cursor", "task-model-b", 200, 20, now)
	insertUsage("cursor", "old-task-model", 9999, 0, now.AddDate(0, 0, -10))

	w := httptest.NewRecorder()
	req := newRequest(http.MethodGet, "/api/runtimes/"+runtimeID+"/usage/by-task?days=1&tz=UTC", nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.GetRuntimeUsageByTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetRuntimeUsageByTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []runtimeUsageByTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	byModel := map[string]runtimeUsageByTaskResponse{}
	for _, row := range resp {
		if row.TaskID == taskID {
			byModel[row.Model] = row
		}
	}
	if len(byModel) != 2 {
		t.Fatalf("expected two current model rows for task, got %d: %+v", len(byModel), byModel)
	}
	if _, ok := byModel["old-task-model"]; ok {
		t.Fatalf("old usage row should be excluded by days cutoff: %+v", byModel["old-task-model"])
	}
	rowA := byModel["task-model-a"]
	if rowA.Provider != "anthropic" || rowA.InputTokens != 1500 || rowA.OutputTokens != 15 {
		t.Fatalf("model-a aggregate = %+v, want provider anthropic input 1500 output 15", rowA)
	}
	if rowA.IssueID == nil || *rowA.IssueID != issueID {
		t.Fatalf("issue_id = %v, want %s", rowA.IssueID, issueID)
	}
	if rowA.IssueNumber != int32(issueNumber) || rowA.IssueTitle != "runtime by task usage" {
		t.Fatalf("issue metadata = number %d title %q", rowA.IssueNumber, rowA.IssueTitle)
	}
	if rowA.StartedAt == nil || rowA.CompletedAt == nil {
		t.Fatalf("started_at/completed_at should be present: %+v", rowA)
	}
}

func TestListRuntimeUsageBucketsByViewerTimezone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := handlerTestRuntimeID(t)

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	cutoff := time.Date(2026, 5, 4, 0, 0, 0, 0, loc)
	cutoffDate := cutoff.Format("2006-01-02")
	extraDate := cutoff.AddDate(0, 0, -1).Format("2006-01-02")

	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly WHERE runtime_id = $1 AND provider = 'cutoff-test'`, runtimeID)
	})

	var agentID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY id LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("pick fixture agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage_hourly (
			bucket_hour, workspace_id, runtime_id, agent_id, project_id,
			provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count
		)
		VALUES
			(($1::date + interval '4 hours') AT TIME ZONE 'Asia/Shanghai', $3, $4, $5, NULL,
				'cutoff-test', 'old-day',    111, 0, 0, 0, 1),
			(($2::date + interval '4 hours') AT TIME ZONE 'Asia/Shanghai', $3, $4, $5, NULL,
				'cutoff-test', 'cutoff-day', 222, 0, 0, 0, 1)
		ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_key DO UPDATE
			SET input_tokens = EXCLUDED.input_tokens,
			    output_tokens = EXCLUDED.output_tokens,
			    cache_read_tokens = EXCLUDED.cache_read_tokens,
			    cache_write_tokens = EXCLUDED.cache_write_tokens,
			    event_count = EXCLUDED.event_count
	`, extraDate, cutoffDate, testWorkspaceID, runtimeID, agentID); err != nil {
		t.Fatalf("seed hourly rows: %v", err)
	}

	resp, err := testHandler.listRuntimeUsage(ctx, parseUUID(runtimeID), "Asia/Shanghai", pgtype.Timestamptz{
		Time:  cutoff,
		Valid: true,
	})
	if err != nil {
		t.Fatalf("listRuntimeUsage: %v", err)
	}
	byDate := make(map[string]int64)
	for _, row := range resp {
		if row.Provider == "cutoff-test" {
			byDate[row.Date] += row.InputTokens
		}
	}
	if byDate[cutoffDate] != 222 {
		t.Fatalf("expected cutoff date %s to be included with 222 tokens, got map %v", cutoffDate, byDate)
	}
	if byDate[extraDate] != 0 {
		t.Fatalf("expected extra date %s to be excluded, got map %v", extraDate, byDate)
	}
}

func TestResolveViewingTZ(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var userID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, account, timezone)
		 VALUES ('TZ Resolve', 'tz-resolve@multica.ai', 'Asia/Tokyo') RETURNING id`,
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM "user" WHERE id = $1`, userID) })

	var bareUserID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, account)
		 VALUES ('TZ Bare', 'tz-bare@multica.ai') RETURNING id`,
	).Scan(&bareUserID); err != nil {
		t.Fatalf("insert bare user: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM "user" WHERE id = $1`, bareUserID) })
	var badTZUserID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, account, timezone)
		 VALUES ('TZ Bad', 'tz-bad@multica.ai', 'Bad/Zone') RETURNING id`,
	).Scan(&badTZUserID); err != nil {
		t.Fatalf("insert bad-tz user: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM "user" WHERE id = $1`, badTZUserID) })

	for _, tc := range []struct {
		name, path, userID, want string
	}{
		{"query override", "/api/dashboard/usage/daily?tz=America/New_York", userID, "America/New_York"},
		{"stored timezone", "/api/dashboard/usage/daily", userID, "Asia/Tokyo"},
		{"invalid query uses stored", "/api/dashboard/usage/daily?tz=Mars/Olympus", userID, "Asia/Tokyo"},
		{"missing preference", "/api/dashboard/usage/daily", bareUserID, "UTC"},
		{"invalid stored timezone", "/api/dashboard/usage/daily", badTZUserID, "UTC"},
		{"unauthenticated", "/api/dashboard/usage/daily", "", "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(http.MethodGet, tc.path, nil)
			if tc.userID != "" {
				req.Header.Set("X-User-ID", tc.userID)
			}
			if got := testHandler.resolveViewingTZ(req); got != tc.want {
				t.Fatalf("resolveViewingTZ = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeHeatmapEndpointsUseViewerTZ(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := handlerTestRuntimeID(t)

	cases := []struct {
		name   string
		path   string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{"usage by-hour", "/api/runtimes/" + runtimeID + "/usage/by-hour?tz=Asia/Shanghai", testHandler.GetRuntimeUsageByHour},
		{"task activity", "/api/runtimes/" + runtimeID + "/activity?tz=Asia/Shanghai", testHandler.GetRuntimeTaskActivity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest("GET", c.path, nil)
			req = withURLParam(req, "runtimeId", runtimeID)
			c.handle(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d: %s", c.name, w.Code, w.Body.String())
			}
		})
	}
}
