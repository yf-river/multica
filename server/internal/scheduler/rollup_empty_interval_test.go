package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestRollupTaskUsageHourlySkipsAdvancedEmptyPrefix(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)
	ws, _, _, task := seedRollupFixture(t, pool)
	t.Cleanup(func() { cleanupRollupFixture(t, pool, ws) })
	if _, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,input_tokens,output_tokens,created_at,updated_at) VALUES($1,'p','advanced-empty-prefix',7,17,now()-interval '30 minutes',now()-interval '30 minutes')`, task); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at='1973-09-17' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(output_tokens),0) FROM task_usage_hourly WHERE workspace_id=$1`, ws).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 17 {
		t.Fatalf("advanced empty watermark did not reach actual usage: output=%d want17", total)
	}
	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(output_tokens),0) FROM task_usage_hourly WHERE workspace_id=$1`, ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != total {
		t.Fatalf("replay double counted usage: %d -> %d", total, after)
	}
}

func TestEnsureRollupEmptyIntervalTimeSemantics(t *testing.T) {
	for _, mode := range []string{"empty", "legacy-created", "updated-backfill", "dirty-only", "unclosed-window", "future-change", "valid-watermark"} {
		t.Run(mode, func(t *testing.T) {
			pool := integrationPool(t)
			ctx := context.Background()
			lockRollupSingleton(t, pool)
			ws, rt, ag, task := seedRollupFixture(t, pool)
			t.Cleanup(func() { cleanupRollupFixture(t, pool, ws) })
			// 每个子例仅清理其自有fixture；共享表的其它有效工作同样约束可跳区间。
			if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at='1973-09-17' WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "legacy-created":
				_, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p',$2,11,now()-interval '30 minutes',NULL)`, task, mode)
				if err != nil {
					t.Fatal(err)
				}
			case "updated-backfill":
				_, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p',$2,11,'2000-01-01',now()-interval '30 minutes')`, task, mode)
				if err != nil {
					t.Fatal(err)
				}
			case "dirty-only":
				_, err := pool.Exec(ctx, `INSERT INTO task_usage_hourly_dirty(bucket_hour,workspace_id,runtime_id,agent_id,provider,model,enqueued_at) VALUES('2000-01-01',$1,$2,$3,'p',$4,now()-interval '30 minutes')`, ws, rt, ag, mode)
				if err != nil {
					t.Fatal(err)
				}
			case "unclosed-window", "future-change":
				_, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p',$2,11,now(),CASE WHEN $2='future-change' THEN now()+interval '1 day' ELSE now() END)`, task, mode)
				if err != nil {
					t.Fatal(err)
				}
			case "valid-watermark":
				_, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p',$2,11,now()-interval '30 minutes',now()-interval '30 minutes');`, task, mode)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at=now()-interval '10 minutes' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
			}
			var before time.Time
			if err := pool.QueryRow(ctx, `SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id=1`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			ready, err := ensureTaskUsageRollupState(ctx, pool)
			if err != nil || !ready {
				t.Fatalf("ensure=%v,%v", ready, err)
			}
			var got, cap time.Time
			if err := pool.QueryRow(ctx, `SELECT watermark_at,now()-interval '5 minutes' FROM task_usage_hourly_rollup_state WHERE id=1`).Scan(&got, &cap); err != nil {
				t.Fatal(err)
			}
			expected := cap
			if mode == "legacy-created" || mode == "updated-backfill" || mode == "unclosed-window" || mode == "future-change" {
				if err := pool.QueryRow(ctx, `SELECT date_trunc('hour',COALESCE(updated_at,created_at)) FROM task_usage WHERE task_id=$1`, task).Scan(&expected); err != nil {
					t.Fatal(err)
				}
			} else if mode == "dirty-only" {
				if err := pool.QueryRow(ctx, `SELECT date_trunc('hour',enqueued_at) FROM task_usage_hourly_dirty WHERE workspace_id=$1`, ws).Scan(&expected); err != nil {
					t.Fatal(err)
				}
			}
			if expected.After(cap) {
				expected = cap
			}
			if mode == "valid-watermark" {
				if got.Before(before) {
					t.Fatalf("valid watermark rewound: %v -> %v", before, got)
				}
			} else if got.Before(expected.Add(-time.Second)) || got.After(expected.Add(time.Second)) {
				t.Fatalf("mode=%s wrong change-time boundary: got%v want%v", mode, got, expected)
			}

			if got.After(cap) {
				t.Fatalf("skipped into unclosed window: %v > %v", got, cap)
			}
			if mode == "unclosed-window" || mode == "future-change" {
				if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
					t.Fatal(err)
				}
				if countHourlyRowsForWorkspace(t, pool, ws) != 0 {
					t.Fatal("unclosed usage reported early")
				}
			}
		})
	}
}

func TestRollupTaskUsageHourlySkipsEmptyMiddle(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)
	ws, _, _, task := seedRollupFixture(t, pool)
	t.Cleanup(func() { cleanupRollupFixture(t, pool, ws) })
	if _, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p','already-processed',3,'2020-01-01','2020-01-01'),($1,'p','new-after-gap',17,now()-interval '30 minutes',now()-interval '30 minutes')`, task); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at='2021-01-01' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(output_tokens),0) FROM task_usage_hourly WHERE workspace_id=$1 AND model='new-after-gap'`, ws).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 17 {
		t.Fatalf("already processed old row pinned empty middle: got%d want17", total)
	}
}

func TestEnsureRollupPreservesLateHistoricalWrite(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)
	ws, _, _, task := seedRollupFixture(t, pool)
	t.Cleanup(func() { cleanupRollupFixture(t, pool, ws) })
	if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at='1973-09-17' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	// 正常历史上报仅created_at属于旧日期，updated_at是这次写入的真实当前时间。
	if _, err := writer.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p','late-history',19,'2000-01-01',now())`, task); err != nil {
		t.Fatal(err)
	}
	ready, err := ensureTaskUsageRollupState(ctx, pool)
	if err != nil || !ready {
		t.Fatalf("ensure=%v,%v", ready, err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var visibleAfterWatermark bool
	if err := pool.QueryRow(ctx, `SELECT tu.updated_at>=s.watermark_at FROM task_usage tu CROSS JOIN task_usage_hourly_rollup_state s WHERE tu.task_id=$1 AND tu.model='late-history'`, task).Scan(&visibleAfterWatermark); err != nil {
		t.Fatal(err)
	}
	if !visibleAfterWatermark {
		t.Fatal("late committed historical report fell behind skipped watermark")
	}
	// 不手动改写时钟让未满5分钟的当前变更假装已经汇总。
}

func TestRollupOldDirtyKeySurvivesValidWatermark(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	lockRollupSingleton(t, pool)
	ws, _, _, task := seedRollupFixture(t, pool)
	t.Cleanup(func() { cleanupRollupFixture(t, pool, ws) })
	if _, err := pool.Exec(ctx, `INSERT INTO task_usage(task_id,provider,model,output_tokens,created_at,updated_at) VALUES($1,'p','old-dirty',23,'2020-01-01','2020-01-01')`, task); err != nil {
		t.Fatal(err)
	}
	// 独占测试库用原生窗口函数建立随后待清理的旧桶，产品环境不执行测试函数。
	if _, err := pool.Exec(ctx, `SELECT rollup_task_usage_hourly_window('2019-12-31','2020-01-02')`); err != nil {
		t.Fatal(err)
	}
	if countHourlyRowsForWorkspace(t, pool, ws) == 0 {
		t.Fatal("fixture old hourly bucket missing")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM task_usage WHERE task_id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_dirty SET enqueued_at='2020-01-02' WHERE workspace_id=$1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_usage_hourly_rollup_state SET watermark_at='2021-01-01' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := makeTaskUsageHourlyHandler(pool)(ctx, HandlerInput{}); err != nil {
		t.Fatal(err)
	}
	if countHourlyRowsForWorkspace(t, pool, ws) != 0 {
		t.Fatal("old dirty key was filtered out before stale hourly bucket cleanup")
	}
}
