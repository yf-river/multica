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
// These tests pin the self-healing behaviour introduced by migration 620.

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

	var rows int64
	if err := pool.QueryRow(ctx, `SELECT rollup_task_usage_hourly()`).Scan(&rows); err != nil {
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

	if err := pool.QueryRow(ctx, `SELECT rollup_task_usage_hourly()`).Scan(new(int64)); err != nil {
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
