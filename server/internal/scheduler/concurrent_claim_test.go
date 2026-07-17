package scheduler

import (
	"context"
	"sync"
	"testing"
)

// Concurrent replicas must resolve one plan through the database uniqueness key.
func TestConcurrentClaimsSingleWinner(t *testing.T) {
	pool := integrationPool(t)
	job := newTestJobSpec(uniqueJobName(t, "concurrent_claim"))
	t.Cleanup(func() { cleanupExecutions(t, pool, job.Name) })

	ctx := context.Background()
	now, err := dbNow(ctx, pool)
	if err != nil {
		t.Fatalf("dbNow: %v", err)
	}
	planTime := FloorPlan(now, job.Cadence)

	const contenders = 8
	type result struct {
		runnerID string
		c        claim
		err      error
	}
	results := make([]result, contenders)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range contenders {
		i := i
		runnerID := "runner-" + string(rune('A'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := tryClaim(ctx, pool, job, ScopeGlobal, planTime, now, runnerID)
			results[i] = result{runnerID: runnerID, c: c, err: err}
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	conflicts := 0
	steals := 0
	var winner string
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("contender %s: %v", r.runnerID, r.err)
		}
		switch {
		case r.c.Won:
			wins++
			winner = r.runnerID
		case r.c.Stole:
			steals++
		case r.c.Conflicted:
			conflicts++
		}
	}
	if wins != 1 {
		t.Fatalf("expected exactly 1 fresh winner, got %d (conflicts=%d steals=%d)",
			wins, conflicts, steals)
	}
	if steals != 0 {
		t.Fatalf("a fresh insert race must not produce a stale steal, got %d steals", steals)
	}
	if conflicts != contenders-1 {
		t.Fatalf("expected %d conflicts, got %d", contenders-1, conflicts)
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sys_cron_executions WHERE job_name = $1 AND plan_time = $2
	`, job.Name, planTime).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected exactly 1 row in sys_cron_executions, got %d", rowCount)
	}

	var dbRunner string
	if err := pool.QueryRow(ctx, `
		SELECT runner_id FROM sys_cron_executions WHERE job_name = $1 AND plan_time = $2
	`, job.Name, planTime).Scan(&dbRunner); err != nil {
		t.Fatalf("scan winner: %v", err)
	}
	if dbRunner != winner {
		t.Fatalf("DB winner %q != local winner %q", dbRunner, winner)
	}
}
