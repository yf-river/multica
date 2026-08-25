package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagerEveryPlanRetriesFailedSamePlanTime(t *testing.T) {
	pool := integrationPool(t)
	job := newTestJobSpec(uniqueJobName(t, "every_plan_retry"))
	t.Cleanup(func() { cleanupExecutions(t, pool, job.Name) })

	job.CatchUpMode = CatchUpEveryPlan
	job.MaxPlansPerTick = 4
	job.CatchUpWindow = 24 * time.Hour
	// Long cadence so two consecutive runOnce calls land in the same
	// plan_time bucket — the test is about the cursor, not the bucket
	// math.
	job.Cadence = time.Hour
	job.MaxAttempts = 3
	job.RetryBackoff = []time.Duration{
		1 * time.Second, // attempt 1 → 2: sleep 1s
		1 * time.Second, // attempt 2 → 3
	}
	job.AllowStaleReentry = false

	var calls atomic.Int32
	job.Handler = func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		calls.Add(1)
		return HandlerResult{}, errors.New("simulated handler failure")
	}

	mgr := newManagerWithRunnerID(pool, "retry-runner")
	if err := mgr.Register(*job); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Tick 1: handler fails at plan_time T attempt 1.
	if err := mgr.runOnce(ctx); err != nil {
		t.Fatalf("first runOnce: %v", err)
	}

	rowsAfterTick1 := dumpJobRows(t, pool, job.Name)
	if len(rowsAfterTick1) != 1 {
		t.Fatalf("expected 1 row after tick 1, got %d: %+v", len(rowsAfterTick1), rowsAfterTick1)
	}
	r1 := rowsAfterTick1[0]
	if r1.Status != "FAILED" {
		t.Fatalf("expected FAILED after tick 1, got %q", r1.Status)
	}
	if r1.Attempt != 1 {
		t.Fatalf("expected attempt=1 after tick 1, got %d", r1.Attempt)
	}
	if r1.NextRetryAt.IsZero() {
		t.Fatalf("expected next_retry_at to be set after a retry-eligible failure")
	}

	planT := r1.PlanTime

	// Force next_retry_at into the past so the second tick sees the
	// retry as due. We deliberately use the DB's clock so this stays
	// independent of the app process clock (consistent with the rest
	// of the scheduler's time handling).
	if _, err := pool.Exec(ctx, `
		UPDATE sys_cron_executions
		   SET next_retry_at = now() - INTERVAL '1 minute'
		 WHERE id = $1
	`, r1.ID); err != nil {
		t.Fatalf("force next_retry_at into the past: %v", err)
	}

	// Tick 2: planner must keep cursor on plan_time T so tryClaim's
	// FAILED-with-retry branch fires.
	if err := mgr.runOnce(ctx); err != nil {
		t.Fatalf("second runOnce: %v", err)
	}

	rowsAfterTick2 := dumpJobRows(t, pool, job.Name)
	// Still exactly one row at plan_time T — the retry reuses the
	// same row, it does not create a new one.
	if len(rowsAfterTick2) != 1 {
		t.Fatalf("expected 1 row after tick 2 (retry reuses row), got %d: %+v",
			len(rowsAfterTick2), rowsAfterTick2)
	}
	r2 := rowsAfterTick2[0]
	if !r2.PlanTime.Equal(planT) {
		t.Fatalf("planner skipped past failed plan_time: tick1=%s tick2=%s",
			planT.Format(time.RFC3339), r2.PlanTime.Format(time.RFC3339))
	}
	if r2.Attempt != 2 {
		t.Fatalf("expected attempt=2 after retry, got %d", r2.Attempt)
	}
	if r2.Status != "FAILED" {
		// Handler still fails, so attempt 2 also lands as FAILED.
		// We still want to confirm the retry actually ran.
		t.Fatalf("expected attempt 2 to land FAILED again (handler still errors), got %q", r2.Status)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected handler called twice across two ticks, got %d calls", calls.Load())
	}
}

type rowSnapshot struct {
	ID          string
	PlanTime    time.Time
	Status      string
	Attempt     int
	MaxAttempts int
	NextRetryAt time.Time
}

func dumpJobRows(t *testing.T, pool *pgxpool.Pool, jobName string) []rowSnapshot {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, plan_time, status, attempt, max_attempts, COALESCE(next_retry_at, 'epoch'::timestamptz)
		  FROM sys_cron_executions
		 WHERE job_name = $1
		 ORDER BY plan_time ASC
	`, jobName)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	defer rows.Close()
	var out []rowSnapshot
	for rows.Next() {
		var r rowSnapshot
		if err := rows.Scan(&r.ID, &r.PlanTime, &r.Status, &r.Attempt, &r.MaxAttempts, &r.NextRetryAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		// Treat the 'epoch' COALESCE sentinel as zero so callers can
		// distinguish "no retry scheduled" from "retry scheduled at
		// some real timestamp".
		if r.NextRetryAt.Year() == 1970 {
			r.NextRetryAt = time.Time{}
		} else {
			r.NextRetryAt = r.NextRetryAt.UTC()
		}
		r.PlanTime = r.PlanTime.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iter: %v", err)
	}
	return out
}
