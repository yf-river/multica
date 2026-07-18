package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func loadDashboardRuntimeAgent(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()

	var runtimeID, agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("fetch runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("fetch agent: %v", err)
	}
	return runtimeID, agentID
}

func insertDashboardIssue(t *testing.T, ctx context.Context, title, projectID string) string {
	t.Helper()
	return insertDashboardIssueWithDB(t, ctx, testPool, title, projectID)
}

func insertDashboardIssueWithDB(t *testing.T, ctx context.Context, db dashboardQueryRower, title, projectID string) string {
	t.Helper()
	var issueID string
	if err := db.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type, project_id, number)
		VALUES (
			$1, $2, $3, 'member', NULLIF($4, '')::uuid,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1)
		)
		RETURNING id
	`, testWorkspaceID, title, testUserID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("insert dashboard issue: %v", err)
	}
	return issueID
}

func insertDashboardProject(t *testing.T, ctx context.Context, title string) string {
	t.Helper()
	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, title).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })
	return projectID
}

type dashboardQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertDashboardTask(t *testing.T, ctx context.Context, db dashboardQueryRower, agentID, issueID, runtimeID string, createdAt time.Time) string {
	t.Helper()
	var taskID string
	if err := db.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, context, created_at)
		VALUES ($1, NULLIF($2, '')::uuid, $3, 'completed', '{}'::jsonb, $4)
		RETURNING id
	`, agentID, issueID, runtimeID, createdAt).Scan(&taskID); err != nil {
		t.Fatalf("insert dashboard task: %v", err)
	}
	return taskID
}

func rollupDashboardUsage(t *testing.T, ctx context.Context) {
	t.Helper()

	if _, err := testPool.Exec(ctx, `
		SELECT rollup_task_usage_hourly_window('1970-01-01'::timestamptz, now() + interval '1 hour')
	`); err != nil {
		t.Fatalf("roll up dashboard usage: %v", err)
	}
}

type dashboardDailyRow struct {
	Date        string `json:"date"`
	Model       string `json:"model"`
	InputTokens int64  `json:"input_tokens"`
}

func dashboardViewerDates(t *testing.T, instant time.Time) (string, string) {
	t.Helper()
	utcDate := instant.UTC().Format("2006-01-02")
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load Los Angeles location: %v", err)
	}
	losAngelesDate := instant.In(location).Format("2006-01-02")
	if utcDate == losAngelesDate {
		t.Fatalf("test setup: UTC and Los Angeles dates must differ, both %s", utcDate)
	}
	return utcDate, losAngelesDate
}

func readDashboardDailyModel(t *testing.T, timezone, model string) dashboardDailyRow {
	t.Helper()
	rows := readDashboardDailyRows(t, "/api/dashboard/usage/daily?days=10&tz="+timezone)
	for _, row := range rows {
		if row.Model == model {
			return row
		}
	}
	t.Fatalf("tz=%s: model %s not found in %v", timezone, model, rows)
	return dashboardDailyRow{}
}

func readDashboardDailyRows(t *testing.T, path string) []dashboardDailyRow {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.GetDashboardUsageDaily(response, newRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("daily usage: expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var rows []dashboardDailyRow
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatalf("decode daily usage: %v", err)
	}
	return rows
}

func dashboardDailyTokenTotal(rows []dashboardDailyRow, model string) int64 {
	var total int64
	for _, row := range rows {
		if row.Model == model {
			total += row.InputTokens
		}
	}
	return total
}

func beginDashboardRollupTransaction(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollup transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func resetDashboardRollupState(t *testing.T, ctx context.Context, tx pgx.Tx, watermark time.Time, lastError any) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
		DELETE FROM task_usage_hourly_dirty;
		DELETE FROM task_usage_hourly;
		DELETE FROM task_usage;
	`); err != nil {
		t.Fatalf("clear rollup tables: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		SET watermark_at = $1, last_error = $2
		WHERE id = 1
	`, watermark, lastError); err != nil {
		t.Fatalf("reset rollup watermark: %v", err)
	}
}

func insertDashboardTransactionUsage(t *testing.T, ctx context.Context, tx pgx.Tx, taskID, model string, inputTokens int64, createdAt time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_usage (
			task_id, provider, model, input_tokens, output_tokens, created_at, updated_at
		)
		VALUES ($1, 'claude', $2, $3, 0, $4, $4)
	`, taskID, model, inputTokens, createdAt); err != nil {
		t.Fatalf("insert dashboard usage: %v", err)
	}
}

func TestDashboardEndpoints(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	// Two issues: one bound to a project, one not.
	projectID := insertDashboardProject(t, ctx, "dashboard test project")

	mkIssue := func(withProject bool) string {
		pid := ""
		if withProject {
			pid = projectID
		}
		id := insertDashboardIssue(t, ctx, "dashboard test", pid)
		t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, id) })
		return id
	}
	projectIssueID := mkIssue(true)
	otherIssueID := mkIssue(false)

	now := time.Now().UTC()
	started := now.Add(-30 * time.Minute)
	completed := started.Add(10 * time.Minute) // 600s run

	mkTaskWithUsage := func(issueID string, status string, tokens int64) {
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, started_at, completed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, now())
			RETURNING id
		`, agentID, issueID, runtimeID, status, started, completed).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		if _, err := testPool.Exec(ctx, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, 'claude', 'claude-3-5-sonnet', $2, 0, now())
		`, taskID, tokens); err != nil {
			t.Fatalf("insert task_usage: %v", err)
		}
		t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	}

	mkTaskWithUsage(projectIssueID, "completed", 1000)
	mkTaskWithUsage(otherIssueID, "completed", 500)

	rollupDashboardUsage(t, ctx)

	type byAgentRow struct {
		AgentID     string `json:"agent_id"`
		Model       string `json:"model"`
		InputTokens int64  `json:"input_tokens"`
	}
	type runtimeRow struct {
		AgentID      string `json:"agent_id"`
		TotalSeconds int64  `json:"total_seconds"`
		TaskCount    int32  `json:"task_count"`
	}

	// daily — workspace-wide
	{
		total := dashboardDailyTokenTotal(readDashboardDailyRows(t, "/api/dashboard/usage/daily?days=1"), "claude-3-5-sonnet")
		if total < 1500 {
			t.Errorf("daily ws: expected >=1500 tokens (1000+500), got %d", total)
		}
	}

	// daily — project-scoped
	{
		total := dashboardDailyTokenTotal(readDashboardDailyRows(t, "/api/dashboard/usage/daily?days=1&project_id="+projectID), "claude-3-5-sonnet")
		// Project filter must exclude the 500-token "other" issue. Token total
		// for this project must be >= 1000 (our task) and < 1500 (would only
		// reach 1500 if filter leaked).
		if total < 1000 {
			t.Errorf("daily project: expected >=1000 tokens, got %d", total)
		}
		if total >= 1500 {
			t.Errorf("daily project: filter leaked — expected <1500 tokens, got %d", total)
		}
	}

	// by-agent — project-scoped
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardUsageByAgent(w, newRequest("GET", "/api/dashboard/usage/by-agent?days=1&project_id="+projectID, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("by-agent project: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var rows []byAgentRow
		_ = json.NewDecoder(w.Body).Decode(&rows)
		found := false
		for _, r := range rows {
			if r.AgentID == agentID && r.InputTokens >= 1000 {
				found = true
			}
		}
		if !found {
			t.Errorf("by-agent project: expected agent %s with >=1000 tokens; got %v", agentID, rows)
		}
	}

	// agent-runtime — project-scoped
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardAgentRunTime(w, newRequest("GET", "/api/dashboard/agent-runtime?days=1&project_id="+projectID, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("agent-runtime: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var rows []runtimeRow
		_ = json.NewDecoder(w.Body).Decode(&rows)
		var seconds int64
		var tasks int32
		for _, r := range rows {
			if r.AgentID == agentID {
				seconds += r.TotalSeconds
				tasks += r.TaskCount
			}
		}
		if tasks < 1 {
			t.Errorf("agent-runtime: expected >=1 task for agent, got %d", tasks)
		}
		if seconds < 600 {
			t.Errorf("agent-runtime: expected >=600s (one 10-minute run), got %d", seconds)
		}
	}

	// agent-runtime — invalid project_id rejected
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardAgentRunTime(w, newRequest("GET", "/api/dashboard/agent-runtime?project_id=not-a-uuid", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("agent-runtime: expected 400 for invalid uuid, got %d", w.Code)
		}
	}

	// Workspace-wide by-agent through the same rollup, just to confirm
	// the no-project-filter shape matches up.
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardUsageByAgent(w, newRequest("GET", "/api/dashboard/usage/by-agent?days=1", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("by-agent ws: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var aRows []byAgentRow
		_ = json.NewDecoder(w.Body).Decode(&aRows)
		var aTotal int64
		for _, r := range aRows {
			if r.AgentID == agentID && r.Model == "claude-3-5-sonnet" {
				aTotal += r.InputTokens
			}
		}
		if aTotal < 1500 {
			t.Errorf("by-agent ws: expected >=1500 tokens across workspace, got %d", aTotal)
		}
	}
}

func TestDashboardUsageDailyBucketsByViewerTimezone(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly WHERE runtime_id = $1 AND provider = 'tz-bucket-test'`, runtimeID)
	})
	// One bucket at 04:00 UTC two days ago. 04:00 UTC is still the
	// previous evening in America/Los_Angeles (UTC-7/-8), so the UTC
	// viewer and the LA viewer must see this row under different dates.
	var bucketHour time.Time
	if err := testPool.QueryRow(ctx, `
		INSERT INTO task_usage_hourly (
			bucket_hour, workspace_id, runtime_id, agent_id, project_id,
			provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count
		)
		VALUES (
			((CURRENT_DATE - 2)::timestamp + interval '4 hours') AT TIME ZONE 'UTC',
			$1, $2, $3, NULL, 'tz-bucket-test', 'tz-bucket-model',
			999, 0, 0, 0, 1
		)
		ON CONFLICT ON CONSTRAINT uq_task_usage_hourly_key DO UPDATE
			SET input_tokens = EXCLUDED.input_tokens
		RETURNING bucket_hour
	`, testWorkspaceID, runtimeID, agentID).Scan(&bucketHour); err != nil {
		t.Fatalf("seed hourly row: %v", err)
	}

	utcDate, laDate := dashboardViewerDates(t, bucketHour)
	if got := readDashboardDailyModel(t, "UTC", "tz-bucket-model").Date; got != utcDate {
		t.Errorf("UTC viewer: expected date %s, got %s", utcDate, got)
	}
	if got := readDashboardDailyModel(t, "America/Los_Angeles", "tz-bucket-model").Date; got != laDate {
		t.Errorf("LA viewer: expected date %s, got %s", laDate, got)
	}
}

func TestDashboardRunTimeDailyBucketsByViewerTimezone(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	// Issue tagged so we can clean up just this test's rows.
	issueID := insertDashboardIssue(t, ctx, "runtime-daily tz test", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	// completed_at at 04:00 UTC two days ago — still the prior evening in LA.
	// started_at 10 minutes earlier so the run has a non-zero duration.
	var completedAt time.Time
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, started_at, completed_at, created_at)
		VALUES (
			$1, $2, $3, 'completed',
			((CURRENT_DATE - 2)::timestamp + interval '3 hours 50 minutes') AT TIME ZONE 'UTC',
			((CURRENT_DATE - 2)::timestamp + interval '4 hours') AT TIME ZONE 'UTC',
			now()
		)
		RETURNING id, completed_at
	`, agentID, issueID, runtimeID).Scan(&taskID, &completedAt); err != nil {
		t.Fatalf("insert completed task: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	utcDate, laDate := dashboardViewerDates(t, completedAt)

	readRow := func(tz string) (string, int64, int32) {
		w := httptest.NewRecorder()
		testHandler.GetDashboardRunTimeDaily(w, newRequest("GET", "/api/dashboard/runtime/daily?days=10&tz="+tz, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("tz=%s: expected 200, got %d: %s", tz, w.Code, w.Body.String())
		}
		var rows []struct {
			Date         string `json:"date"`
			TotalSeconds int64  `json:"total_seconds"`
			TaskCount    int32  `json:"task_count"`
		}
		_ = json.NewDecoder(w.Body).Decode(&rows)
		want := utcDate
		if tz == "America/Los_Angeles" {
			want = laDate
		}
		for _, r := range rows {
			if r.Date == want {
				return r.Date, r.TotalSeconds, r.TaskCount
			}
		}
		t.Fatalf("tz=%s: no row on expected date %s in %v", tz, want, rows)
		return "", 0, 0
	}

	if date, secs, count := readRow("UTC"); date != utcDate || count < 1 || secs < 600 {
		t.Errorf("UTC viewer: got date=%s seconds=%d count=%d, want date=%s seconds>=600 count>=1",
			date, secs, count, utcDate)
	}
	if date, secs, count := readRow("America/Los_Angeles"); date != laDate || count < 1 || secs < 600 {
		t.Errorf("LA viewer: got date=%s seconds=%d count=%d, want date=%s seconds>=600 count>=1",
			date, secs, count, laDate)
	}
}

func TestRollupTaskUsageHourlyIdempotentAndWatermark(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	issueID := insertDashboardIssue(t, ctx, "rollup idempotency", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, runtimeID, time.Now().UTC().Add(-20*time.Minute))

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES ($1, 'claude', 'rollup-idem-model', 3333, 0, now() - interval '20 minutes')
	`, taskID); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}

	readTotal := func() int64 {
		var total int64
		if err := testPool.QueryRow(ctx, `
			SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
			WHERE runtime_id = $1 AND model = 'rollup-idem-model'
		`, runtimeID).Scan(&total); err != nil {
			t.Fatalf("read total: %v", err)
		}
		return total
	}

	// Idempotency: two passes over the same range must not double-count.
	for range 2 {
		rollupDashboardUsage(t, ctx)
	}
	if got := readTotal(); got != 3333 {
		t.Errorf("idempotency: expected exactly 3333 tokens after two passes, got %d", got)
	}

	// Watermark advance: park the watermark an hour back, run the cron
	// entry, confirm it moved forward to ~now()-5min with no error.
	if _, err := testPool.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = now() - interval '1 hour', last_error = 'stale'
		 WHERE id = 1
	`); err != nil {
		t.Fatalf("park watermark: %v", err)
	}
	if _, err := testPool.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
		t.Fatalf("rollup_task_usage_hourly: %v", err)
	}
	var watermarkAge time.Duration
	var lastError *string
	var ageSeconds float64
	if err := testPool.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM (now() - watermark_at)), last_error
		FROM task_usage_hourly_rollup_state WHERE id = 1
	`).Scan(&ageSeconds, &lastError); err != nil {
		t.Fatalf("read rollup state: %v", err)
	}
	watermarkAge = time.Duration(ageSeconds) * time.Second
	// Watermark should sit at now()-5min (the cron upper bound), well
	// short of the 1-hour-back value we parked it at.
	if watermarkAge > 10*time.Minute {
		t.Errorf("watermark did not advance: still %s behind now()", watermarkAge)
	}
	if lastError != nil {
		t.Errorf("expected last_error cleared, got %q", *lastError)
	}
}

func TestRollupTaskUsageHourlyFastForwardsEmptyHistory(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	tx := beginDashboardRollupTransaction(t, ctx)

	resetDashboardRollupState(t, ctx, tx, time.Unix(0, 0).UTC(), "stale")

	if _, err := tx.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
		t.Fatalf("rollup_task_usage_hourly: %v", err)
	}

	var ageSeconds float64
	var lastError *string
	if err := tx.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM (now() - watermark_at)), last_error
		  FROM task_usage_hourly_rollup_state
		 WHERE id = 1
	`).Scan(&ageSeconds, &lastError); err != nil {
		t.Fatalf("read rollup state: %v", err)
	}
	if age := time.Duration(ageSeconds) * time.Second; age > 10*time.Minute {
		t.Fatalf("empty history should fast-forward near now()-5min, got age %s", age)
	}
	if lastError != nil {
		t.Fatalf("expected last_error cleared, got %q", *lastError)
	}
}

func TestRollupTaskUsageHourlyFastForwardsToFirstUsage(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	tx := beginDashboardRollupTransaction(t, ctx)

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)
	usageAt := time.Now().UTC().Add(-20 * time.Minute)
	const model = "rollup-fast-forward-model"

	resetDashboardRollupState(t, ctx, tx, time.Unix(0, 0).UTC(), "stale")
	issueID := insertDashboardIssueWithDB(t, ctx, tx, "rollup fast-forward", "")
	taskID := insertDashboardTask(t, ctx, tx, agentID, issueID, runtimeID, usageAt)
	insertDashboardTransactionUsage(t, ctx, tx, taskID, model, 1234, usageAt)

	if _, err := tx.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
		t.Fatalf("rollup_task_usage_hourly: %v", err)
	}

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0)
		  FROM task_usage_hourly
		 WHERE runtime_id = $1 AND model = $2
	`, runtimeID, model).Scan(&total); err != nil {
		t.Fatalf("read hourly total: %v", err)
	}
	if total != 1234 {
		t.Fatalf("expected first real usage to be aggregated, got %d", total)
	}
}

func TestRollupTaskUsageHourlyReassignBetweenRuntimes(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	oldRuntimeID := handlerTestRuntimeID(t)
	var newRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, 'reassign-target-hourly', 'cloud', 'reassign-target-hourly', 'online', '{}'::jsonb, '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID).Scan(&newRuntimeID); err != nil {
		t.Fatalf("create dest runtime: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM agent_runtime WHERE id = $1`, newRuntimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("fetch agent: %v", err)
	}
	issueID := insertDashboardIssue(t, ctx, "reassign hourly test", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	usageAt := time.Date(2021, 3, 14, 1, 0, 0, 0, time.UTC)
	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, oldRuntimeID, usageAt)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at, updated_at)
		VALUES ($1, 'claude', 'm-reassign-hourly', 700, 70, $2, $2)
	`, taskID, usageAt); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly WHERE model = 'm-reassign-hourly'`)
		mustExec(t, ctx, `DELETE FROM task_usage_hourly_dirty WHERE model = 'm-reassign-hourly'`)
	})

	runWindow := func(label string) {
		if _, err := testPool.Exec(ctx, `
			SELECT rollup_task_usage_hourly_window('-infinity'::timestamptz, 'infinity'::timestamptz)
		`); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	runtimeTotal := func(rt string) int64 {
		var total int64
		if err := testPool.QueryRow(ctx, `
			SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
			WHERE runtime_id = $1 AND model = 'm-reassign-hourly'
		`, rt).Scan(&total); err != nil {
			t.Fatalf("runtime total: %v", err)
		}
		return total
	}

	runWindow("initial rollup")
	if old, new := runtimeTotal(oldRuntimeID), runtimeTotal(newRuntimeID); old != 700 || new != 0 {
		t.Fatalf("initial: expected old=700 new=0, got old=%d new=%d", old, new)
	}

	// Reassignment fires trg_atq_dirty_hourly, which enqueues the OLD and
	// NEW runtime buckets (same bucket_hour, two runtime_ids).
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET runtime_id = $1 WHERE id = $2`, newRuntimeID, taskID); err != nil {
		t.Fatalf("reassign task: %v", err)
	}
	var dirtyCount int
	_ = testPool.QueryRow(ctx, `SELECT COUNT(*) FROM task_usage_hourly_dirty WHERE model = 'm-reassign-hourly'`).Scan(&dirtyCount)
	if dirtyCount != 2 {
		t.Fatalf("expected 2 dirty entries (old+new runtime), got %d", dirtyCount)
	}

	runWindow("rollup after reassign")
	if old, new := runtimeTotal(oldRuntimeID), runtimeTotal(newRuntimeID); old != 0 || new != 700 {
		t.Fatalf("after reassign: expected old=0 new=700, got old=%d new=%d", old, new)
	}
	// The window function must drain every queue row whose enqueued_at
	// predates p_to — a regression on that DELETE pins recomputes forever.
	_ = testPool.QueryRow(ctx, `SELECT COUNT(*) FROM task_usage_hourly_dirty WHERE model = 'm-reassign-hourly'`).Scan(&dirtyCount)
	if dirtyCount != 0 {
		t.Errorf("expected dirty queue drained, got %d entries", dirtyCount)
	}
}

func TestRollupTaskUsageHourlyWorkspaceMismatch(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('ws-mismatch-hourly', 'ws-mismatch-hourly-' || gen_random_uuid()::text, 'WMH')
		RETURNING id
	`).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })

	var foreignRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, 'mismatch-rt-hourly', 'cloud', 'mismatch-rt-hourly', 'online', '{}'::jsonb, '{}'::jsonb, now())
		RETURNING id
	`, foreignWorkspaceID).Scan(&foreignRuntimeID); err != nil {
		t.Fatalf("create foreign runtime: %v", err)
	}
	var foreignAgentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config
		)
		VALUES ($1, 'mismatch-agent-hourly', '', 'cloud', '{}'::jsonb, $2, 'personal', 1, $3, '', '{}'::jsonb, '[]'::jsonb, NULL)
		RETURNING id
	`, foreignWorkspaceID, foreignRuntimeID, testUserID).Scan(&foreignAgentID); err != nil {
		t.Fatalf("create foreign agent: %v", err)
	}

	// Issue lives in the primary test workspace; the agent lives in the
	// foreign one — so agent.workspace_id != issue.workspace_id.
	issueID := insertDashboardIssue(t, ctx, "mismatch hourly test", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	usageAt := time.Date(2021, 9, 9, 1, 0, 0, 0, time.UTC)
	taskID := insertDashboardTask(t, ctx, testPool, foreignAgentID, issueID, foreignRuntimeID, usageAt)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at, updated_at)
		VALUES ($1, 'claude', 'm-mismatch-hourly', 333, 33, $2, $2)
	`, taskID, usageAt); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly WHERE model = 'm-mismatch-hourly'`)
		mustExec(t, ctx, `DELETE FROM task_usage_hourly_dirty WHERE model = 'm-mismatch-hourly'`)
	})

	if _, err := testPool.Exec(ctx, `
		SELECT rollup_task_usage_hourly_window('-infinity'::timestamptz, 'infinity'::timestamptz)
	`); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	wsTotal := func(ws string) int64 {
		var total int64
		if err := testPool.QueryRow(ctx, `
			SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
			WHERE workspace_id = $1 AND model = 'm-mismatch-hourly'
		`, ws).Scan(&total); err != nil {
			t.Fatalf("workspace total: %v", err)
		}
		return total
	}
	if got := wsTotal(foreignWorkspaceID); got != 333 {
		t.Fatalf("expected foreign workspace bucket = 333 (resolved from agent), got %d", got)
	}
	if got := wsTotal(testWorkspaceID); got != 0 {
		t.Errorf("expected primary workspace bucket = 0 (issue.workspace_id must not leak), got %d", got)
	}
}

func TestDashboardRollupReattributesOnProjectChange(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	projectA := insertDashboardProject(t, ctx, "dashboard reattr A")
	projectB := insertDashboardProject(t, ctx, "dashboard reattr B")

	issueID := insertDashboardIssue(t, ctx, "reattr issue", projectA)
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, runtimeID, time.Now().UTC())

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES ($1, 'claude', 'claude-3-5-sonnet', 7777, 0, now())
	`, taskID); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}

	// First rollup pass: tokens attributed to project A.
	rollupDashboardUsage(t, ctx)
	var aTokens int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2 AND agent_id = $3
	`, testWorkspaceID, projectA, agentID).Scan(&aTokens); err != nil {
		t.Fatalf("read A rollup: %v", err)
	}
	if aTokens < 7777 {
		t.Fatalf("project A: expected >=7777 tokens after first rollup, got %d", aTokens)
	}

	// Move the issue to project B. Trigger enqueues both A and B buckets.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectB, issueID); err != nil {
		t.Fatalf("reassign project: %v", err)
	}
	// Second rollup pass: A bucket drops to zero (deleted_empty), B
	// bucket gets the tokens.
	rollupDashboardUsage(t, ctx)

	var bTokens, aTokensAfter int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2 AND agent_id = $3
	`, testWorkspaceID, projectB, agentID).Scan(&bTokens); err != nil {
		t.Fatalf("read B rollup: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2 AND agent_id = $3
	`, testWorkspaceID, projectA, agentID).Scan(&aTokensAfter); err != nil {
		t.Fatalf("read A rollup after move: %v", err)
	}
	if bTokens < 7777 {
		t.Errorf("project B: expected >=7777 tokens after reassign + rollup, got %d", bTokens)
	}
	if aTokensAfter != 0 {
		t.Errorf("project A: expected 0 tokens after reassign + rollup, got %d", aTokensAfter)
	}
}

func TestDashboardRollupClearsOnIssueDelete(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	projectID := insertDashboardProject(t, ctx, "dashboard cascade test")

	issueID := insertDashboardIssue(t, ctx, "cascade issue", projectID)
	// No t.Cleanup deleting the issue — that's what the test exercises.

	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, runtimeID, time.Now().UTC())
	// Don't bother cleaning up taskID either; cascade will take it.

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES ($1, 'claude', 'claude-3-5-sonnet', 4242, 0, now())
	`, taskID); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}

	// First rollup: project bucket exists with 4242 tokens.
	rollupDashboardUsage(t, ctx)
	var before int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2
	`, testWorkspaceID, projectID).Scan(&before); err != nil {
		t.Fatalf("read before: %v", err)
	}
	if before < 4242 {
		t.Fatalf("project bucket: expected >=4242 tokens before delete, got %d", before)
	}

	// Delete the issue. Cascade removes atq + task_usage. The issue
	// BEFORE DELETE trigger should have enqueued the project bucket
	// before the cascade started.
	if _, err := testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID); err != nil {
		t.Fatalf("delete issue: %v", err)
	}

	rollupDashboardUsage(t, ctx)
	var after int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2
	`, testWorkspaceID, projectID).Scan(&after); err != nil {
		t.Fatalf("read after: %v", err)
	}
	if after != 0 {
		t.Errorf("project bucket: expected 0 tokens after issue delete, got %d", after)
	}
}

func TestDashboardRollupReattributesOnLinkTaskToIssue(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	// Quick-create task: issue_id is NULL at creation time.
	taskID := insertDashboardTask(t, ctx, testPool, agentID, "", runtimeID, time.Now().UTC())

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES ($1, 'claude', 'claude-3-5-sonnet', 1234, 0, now())
	`, taskID); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}

	// First rollup: tokens attributed to the no-project bucket (NULL).
	rollupDashboardUsage(t, ctx)
	var nullBefore int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id IS NULL AND agent_id = $2
	`, testWorkspaceID, agentID).Scan(&nullBefore); err != nil {
		t.Fatalf("read NULL bucket pre-link: %v", err)
	}
	if nullBefore < 1234 {
		t.Fatalf("NULL bucket: expected >=1234 tokens pre-link, got %d", nullBefore)
	}

	// Create a project + issue, then run the same UPDATE LinkTaskToIssue
	// uses. The atq trigger should enqueue OLD (NULL project) AND NEW
	// (the project's id) so the next rollup tick zeroes the NULL bucket
	// and populates the project bucket.
	projectID := insertDashboardProject(t, ctx, "dashboard link test")

	issueID := insertDashboardIssue(t, ctx, "link test issue", projectID)
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	// Mirror LinkTaskToIssue's UPDATE shape.
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET issue_id = $1 WHERE id = $2 AND issue_id IS NULL
	`, issueID, taskID); err != nil {
		t.Fatalf("link task to issue: %v", err)
	}

	rollupDashboardUsage(t, ctx)

	var projectAfter, nullAfter int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id = $2 AND agent_id = $3
	`, testWorkspaceID, projectID, agentID).Scan(&projectAfter); err != nil {
		t.Fatalf("read project bucket post-link: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
		WHERE workspace_id = $1 AND project_id IS NULL AND agent_id = $2
	`, testWorkspaceID, agentID).Scan(&nullAfter); err != nil {
		t.Fatalf("read NULL bucket post-link: %v", err)
	}
	if projectAfter < 1234 {
		t.Errorf("project bucket: expected >=1234 tokens after link, got %d", projectAfter)
	}
	if nullAfter != 0 {
		t.Errorf("NULL bucket: expected 0 tokens after link, got %d", nullAfter)
	}
}

func TestPruneTaskUsageHourlyDirty(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	// task_usage_hourly_dirty carries no FKs (it is a queue), so synthetic
	// UUIDs are fine. `provider` tags the rows for isolated cleanup.
	const tag = "ttl-prune-test"
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly_dirty WHERE provider = $1`, tag)
	})
	seed := func(model, age string) {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO task_usage_hourly_dirty (
				bucket_hour, workspace_id, runtime_id, agent_id, project_id,
				provider, model, enqueued_at
			)
			VALUES (
				date_trunc('hour', now()), gen_random_uuid(), gen_random_uuid(),
				gen_random_uuid(), NULL, $1, $2, now() - $3::interval
			)
		`, tag, model, age); err != nil {
			t.Fatalf("seed dirty row %s: %v", model, err)
		}
	}
	countModel := func(model string) int {
		var n int
		if err := testPool.QueryRow(ctx,
			`SELECT COUNT(*) FROM task_usage_hourly_dirty WHERE provider = $1 AND model = $2`,
			tag, model,
		).Scan(&n); err != nil {
			t.Fatalf("count dirty model: %v", err)
		}
		return n
	}

	seed("stale-row", "8 days")
	seed("fresh-row", "1 day")

	// Default 7-day retention: the 8-day row goes, the 1-day row stays.
	var pruned int64
	if err := testPool.QueryRow(ctx, `SELECT prune_task_usage_hourly_dirty()`).Scan(&pruned); err != nil {
		t.Fatalf("prune (default retention): %v", err)
	}
	if pruned < 1 {
		t.Errorf("expected prune to report at least the one stale row deleted, got %d", pruned)
	}
	if got := countModel("stale-row"); got != 0 {
		t.Errorf("default prune: expected stale row deleted, still %d present", got)
	}
	if got := countModel("fresh-row"); got != 1 {
		t.Errorf("default prune: expected fresh row kept, got %d", got)
	}

	// An explicit retention shorter than the surviving row's age drops it.
	if _, err := testPool.Exec(ctx, `SELECT prune_task_usage_hourly_dirty(interval '12 hours')`); err != nil {
		t.Fatalf("prune (12h retention): %v", err)
	}
	if got := countModel("fresh-row"); got != 0 {
		t.Errorf("12h-retention prune: expected fresh row deleted, still %d present", got)
	}

	// The cron entry folds the prune in so operators do
	// not need a second scheduled job. A single tick must drop a stale row.
	seed("cron-fold-row", "9 days")
	if _, err := testPool.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
		t.Fatalf("rollup_task_usage_hourly: %v", err)
	}
	if got := countModel("cron-fold-row"); got != 0 {
		t.Errorf("cron entry did not fold in the prune: stale row still present (%d)", got)
	}
}

func TestRollupTaskUsageHourlyCapsWindowAtOneDay(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	tx := beginDashboardRollupTransaction(t, ctx)

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)
	now := time.Now().UTC()

	resetDashboardRollupState(t, ctx, tx, now, nil)

	seedUsage := func(label string, usageAt time.Time) {
		issueID := insertDashboardIssueWithDB(t, ctx, tx, label, "")
		taskID := insertDashboardTask(t, ctx, tx, agentID, issueID, runtimeID, usageAt)
		insertDashboardTransactionUsage(t, ctx, tx, taskID, label, 100, usageAt)
	}

	seedUsage("rollup-cap-day-1", now.Add(-60*time.Hour))
	seedUsage("rollup-cap-day-2", now.Add(-36*time.Hour))

	park := func(behind string) {
		if _, err := tx.Exec(ctx, `
			UPDATE task_usage_hourly_rollup_state
			   SET watermark_at = now() - $1::interval, last_error = NULL
			 WHERE id = 1
		`, behind); err != nil {
			t.Fatalf("park watermark: %v", err)
		}
	}
	ageDays := func() float64 {
		var sec float64
		if err := tx.QueryRow(ctx, `
			SELECT EXTRACT(EPOCH FROM (now() - watermark_at))
			  FROM task_usage_hourly_rollup_state WHERE id = 1
		`).Scan(&sec); err != nil {
			t.Fatalf("read watermark: %v", err)
		}
		return sec / 86400
	}
	tick := func(label string) {
		if _, err := tx.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	// Park 3 days back. One tick advances by exactly one day (v_from + 1d,
	// well short of now()-5min), leaving the watermark ~2 days behind.
	park("3 days")
	tick("tick 1")
	if age := ageDays(); age < 1.9 || age > 2.1 {
		t.Fatalf("after one tick: expected watermark ~2 days behind, got %.3f days", age)
	}

	// A second tick advances another bounded day → ~1 day behind.
	tick("tick 2")
	if age := ageDays(); age < 0.9 || age > 1.1 {
		t.Fatalf("after two ticks: expected watermark ~1 day behind, got %.3f days", age)
	}

	// Once within a day of now, the tick snaps the watermark to now()-5min
	// (LEAST picks the now bound) rather than taking a further fixed day.
	tick("tick 3")
	if age := ageDays(); age > 0.02 {
		t.Fatalf("after catch-up: expected watermark within minutes of now, got %.3f days", age)
	}
}

func TestDashboardUsageDailyCrossMidnightFullPipeline(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	issueID := insertDashboardIssue(t, ctx, "cross-midnight pipeline test", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, runtimeID, time.Now().UTC())

	// Raw task_usage at 00:30 UTC two days ago — genuinely near UTC
	// midnight. 00:30 UTC is still the PRIOR evening (~16:30/17:30) in
	// America/Los_Angeles (UTC-7/-8), so the UTC viewer and the LA viewer
	// must see this row under different calendar days. Using CURRENT_DATE
	// keeps the row inside the days=10 window without a fixed-date drift.
	var usageAt time.Time
	if err := testPool.QueryRow(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES (
			$1, 'claude', 'cross-midnight-model', 8888, 0,
			((CURRENT_DATE - 2)::timestamp + interval '30 minutes') AT TIME ZONE 'UTC'
		)
		RETURNING created_at
	`, taskID).Scan(&usageAt); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM task_usage_hourly WHERE model = 'cross-midnight-model'`)
	})

	// Run the rollup so the raw row is aggregated into task_usage_hourly.
	rollupDashboardUsage(t, ctx)

	utcDate, laDate := dashboardViewerDates(t, usageAt)
	utcRow := readDashboardDailyModel(t, "UTC", "cross-midnight-model")
	if utcRow.InputTokens != 8888 {
		t.Errorf("UTC viewer: expected 8888 tokens, got %d", utcRow.InputTokens)
	}
	if got := utcRow.Date; got != utcDate {
		t.Errorf("UTC viewer: expected date %s, got %s", utcDate, got)
	}
	laRow := readDashboardDailyModel(t, "America/Los_Angeles", "cross-midnight-model")
	if laRow.InputTokens != 8888 {
		t.Errorf("LA viewer: expected 8888 tokens, got %d", laRow.InputTokens)
	}
	if got := laRow.Date; got != laDate {
		t.Errorf("LA viewer: expected date %s, got %s; row must NOT land on the UTC day %s",
			laDate, got, utcDate)
	}
}

func TestRollupTaskUsageHourlyConvergesOnTaskUsageDelete(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()

	runtimeID, agentID := loadDashboardRuntimeAgent(t, ctx)

	issueID := insertDashboardIssue(t, ctx, "tu-delete trigger test", "")
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM issue WHERE id = $1`, issueID) })

	taskID := insertDashboardTask(t, ctx, testPool, agentID, issueID, runtimeID, time.Now().UTC().Add(-30*time.Minute))

	var usageID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
		VALUES ($1, 'claude', 'tu-delete-model', 5050, 0, now() - interval '30 minutes')
		RETURNING id
	`, taskID).Scan(&usageID); err != nil {
		t.Fatalf("insert task_usage: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM task_usage_hourly WHERE model = 'tu-delete-model'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM task_usage_hourly_dirty WHERE model = 'tu-delete-model'`)
	})

	bucketTotal := func() int64 {
		var total int64
		if err := testPool.QueryRow(ctx, `
			SELECT COALESCE(SUM(input_tokens), 0) FROM task_usage_hourly
			WHERE runtime_id = $1 AND model = 'tu-delete-model'
		`, runtimeID).Scan(&total); err != nil {
			t.Fatalf("bucket total: %v", err)
		}
		return total
	}
	rollupDashboardUsage(t, ctx)
	if got := bucketTotal(); got != 5050 {
		t.Fatalf("initial: expected bucket = 5050, got %d", got)
	}

	// Delete the task_usage row directly — fires trg_tu_dirty_hourly,
	// which enqueues the bucket on task_usage_hourly_dirty.
	if _, err := testPool.Exec(ctx, `DELETE FROM task_usage WHERE id = $1`, usageID); err != nil {
		t.Fatalf("delete task_usage: %v", err)
	}
	var dirtyCount int
	_ = testPool.QueryRow(ctx, `SELECT COUNT(*) FROM task_usage_hourly_dirty WHERE model = 'tu-delete-model'`).Scan(&dirtyCount)
	if dirtyCount != 1 {
		t.Fatalf("expected 1 dirty entry from task_usage DELETE trigger, got %d", dirtyCount)
	}

	rollupDashboardUsage(t, ctx)
	if got := bucketTotal(); got != 0 {
		t.Errorf("after delete: expected bucket recomputed to 0, got %d", got)
	}
}
