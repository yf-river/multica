package scheduler

import (
	"context"
	"testing"
	"time"
)

// The hourly usage rollup used to fail silently in two ways, both of which left
// the workspace usage page at zero while `task_usage` kept growing:
//
//   - a missing `task_usage_hourly_rollup_state` row made the watermark UPDATE
//     match nothing, so every tick reported SUCCESS with zero rows;
//   - a row carrying the 1970-01-01 default advanced at most one day per tick,
//     which needs ~20k ticks to reach the present.
//
// The heal lives in the scheduler handler, not in a replaced SQL function: the
// publisher only auto-applies additive migrations, and a function-body rewrite
// would need a separate compatibility scheme. These tests drive the handler the
// same way the scheduler does.

func TestRollupTaskUsageHourlyRecreatesMissingStateRow(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)

	var original time.Time
	hadRow := true
	if err := pool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&original); err != nil {
		hadRow = false
	}
	t.Cleanup(func() {
		if !hadRow {
			_, _ = pool.Exec(ctx, `DELETE FROM task_usage_hourly_rollup_state WHERE id = 1`)
			return
		}
		_, _ = pool.Exec(ctx, `
			INSERT INTO task_usage_hourly_rollup_state (id, watermark_at)
			VALUES (1, $1)
			ON CONFLICT (id) DO UPDATE SET watermark_at = EXCLUDED.watermark_at`, original)
	})

	if _, err := pool.Exec(ctx, `DELETE FROM task_usage_hourly_rollup_state`); err != nil {
		t.Fatalf("delete rollup state: %v", err)
	}

	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatalf("rollup with missing state row must not fail: %v", err)
	}

	var stateRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&stateRows); err != nil {
		t.Fatalf("count rollup state: %v", err)
	}
	if stateRows != 1 {
		t.Fatalf("rollup must recreate the singleton state row, got %d rows", stateRows)
	}

	var watermark time.Time
	var lastErr *string
	if err := pool.QueryRow(ctx,
		`SELECT watermark_at, last_error FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&watermark, &lastErr); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if lastErr != nil && *lastErr != "" {
		t.Fatalf("rollup recorded an error: %s", *lastErr)
	}
	// A recreated row must be seeded from real usage data, never left at the
	// epoch default — that is the state that silently no-ops.
	if watermark.Year() < 2000 {
		t.Fatalf("watermark %s was not seeded from usage history", watermark)
	}
}

func TestRollupTaskUsageHourlyHealsEpochWatermark(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)

	var original time.Time
	if err := pool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&original); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx,
			`UPDATE task_usage_hourly_rollup_state SET watermark_at = $1 WHERE id = 1`, original)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO task_usage_hourly_rollup_state (id, watermark_at)
		VALUES (1, TIMESTAMPTZ '1970-01-01 00:00:00+00')
		ON CONFLICT (id) DO UPDATE SET watermark_at = TIMESTAMPTZ '1970-01-01 00:00:00+00'`); err != nil {
		t.Fatalf("reset watermark to epoch: %v", err)
	}

	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatalf("rollup with epoch watermark: %v", err)
	}

	var watermark time.Time
	if err := pool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&watermark); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	// A single tick must jump past the epoch to the oldest usage row; leaving it
	// inside 1970 means the heal did not run and the page stays empty.
	if watermark.Year() < 2000 {
		t.Fatalf("epoch watermark was not healed, still %s", watermark)
	}
}

// TestRollupTaskUsageHourlyHandlerCatchesUpBounded 覆盖应用层追赶：水位落后 3 天时一次
// tick 追上；落后 90 天时单次只推进 60 天（每次调用仍是一天窗口），第二次追上。
func TestRollupTaskUsageHourlyHandlerCatchesUpBounded(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)

	var original time.Time
	if err := pool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&original); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx,
			`UPDATE task_usage_hourly_rollup_state SET watermark_at = $1, last_error = NULL WHERE id = 1`, original)
	})

	ageDays := func() float64 {
		var sec float64
		if err := pool.QueryRow(ctx, `
			SELECT EXTRACT(EPOCH FROM (now() - watermark_at))
			  FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&sec); err != nil {
			t.Fatalf("read watermark age: %v", err)
		}
		return sec / 86400
	}
	park := func(behind string) {
		if _, err := pool.Exec(ctx, `
			UPDATE task_usage_hourly_rollup_state SET watermark_at = now() - $1::interval WHERE id = 1`, behind); err != nil {
			t.Fatalf("park watermark: %v", err)
		}
	}

	h := makeTaskUsageHourlyHandler(pool)

	park("3 days")
	if _, err := h(ctx, HandlerInput{}); err != nil {
		t.Fatalf("handler on 3-day backlog: %v", err)
	}
	if age := ageDays(); age > 0.02 {
		t.Fatalf("one tick from 3 days back must converge, still %.3f days behind", age)
	}

	park("90 days")
	if _, err := h(ctx, HandlerInput{}); err != nil {
		t.Fatalf("handler on 90-day backlog: %v", err)
	}
	if age := ageDays(); age < 29 || age > 31 {
		t.Fatalf("one tick from 90 days back must advance about 60 days, got %.3f days behind", age)
	}
	if _, err := h(ctx, HandlerInput{}); err != nil {
		t.Fatalf("second handler tick: %v", err)
	}
	if age := ageDays(); age > 0.02 {
		t.Fatalf("second tick must converge, still %.3f days behind", age)
	}
}

// TestRollupTaskUsageHourlyHandlerStopsWhenLockHeld 保证别的写入者持锁时本 tick 只试
// 一次就收手，不空转 60 次。
func TestRollupTaskUsageHourlyHandlerStopsWhenLockHeld(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	t.Cleanup(func() { blocker.Release() })
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(4246)`); err != nil {
		t.Fatalf("take advisory lock: %v", err)
	}
	t.Cleanup(func() { _, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(4246)`) })

	res, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{})
	if err != nil {
		t.Fatalf("handler while lock held: %v", err)
	}
	ticks, ok := res.Result["rollup_ticks"].(int)
	if !ok {
		t.Fatalf("expected rollup_ticks in result, got %#v", res.Result)
	}
	if ticks != 1 {
		t.Fatalf("handler must stop after one no-progress tick, got %d", ticks)
	}
}

// TestTaskUsageHourlyHandlerReleasesLockOnCancel 保证取消/超时后 4246 不会跟着连接回到
// 池子里。handler 固定一条连接并在归还前显式解锁，所以下一个 tick 仍能拿到锁。
func TestTaskUsageHourlyHandlerReleasesLockOnCancel(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = blockerTx.Rollback(context.Background())
		}
	}()
	if _, err := blockerTx.Exec(ctx, `
		SELECT 1 FROM task_usage_hourly_rollup_state WHERE id = 1 FOR UPDATE
	`); err != nil {
		t.Fatalf("lock rollup state row: %v", err)
	}

	// 2 秒后取消：函数卡在状态行 UPDATE 上，带着 4246 被中断。
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := makeTaskUsageHourlyHandler(pool)(runCtx, HandlerInput{}); err == nil {
		t.Fatalf("expected the cancelled tick to return an error")
	}
	_ = blockerTx.Rollback(context.Background())
	committed = true

	var got bool
	if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_lock(4246)`).Scan(&got); err != nil {
		t.Fatalf("probe advisory lock: %v", err)
	}
	if !got {
		t.Fatalf("advisory lock 4246 leaked: a later tick could not take it")
	}
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_unlock(4246)`); err != nil {
		t.Fatalf("release probe lock: %v", err)
	}
}
