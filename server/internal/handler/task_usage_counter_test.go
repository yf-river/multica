package handler

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/prometheus/client_golang/prometheus"
)

func usageCounterFixture(t *testing.T) (*Handler, string, *prometheus.Registry) {
	t.Helper()
	if testHandler == nil {
		t.Fatal("需要本轮独占 PostgreSQL")
	}
	h := *testHandler
	h.TaskService = service.NewTaskService(h.Queries, h.TxStarter, h.Hub, h.Bus)
	h.TaskService.Metrics = obsmetrics.NewBusinessMetrics()
	registry := prometheus.NewRegistry()
	registry.MustRegister(h.TaskService.Metrics.Collectors()...)
	agent := dbfx.Agent(t, "usage counter fixture", testRuntimeID)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": dbfx.Issue(t, "usage counter fixture"), "status": "completed", "created_at": testutil.Raw("now()-interval '3 hours'")})
	return &h, task, registry
}
func counterUsage(output int64, executed string) map[string]any {
	u := map[string]any{"provider": "codex", "model": "gpt-5.4", "input_tokens": int64(4), "output_tokens": output, "cache_read_tokens": int64(8), "cache_write_tokens": int64(2), "cost_usd_ticks": int64(37)}
	if executed != "" {
		u["executed_at"] = executed
	}
	return u
}
func usageOutputCounter(t *testing.T, r *prometheus.Registry) float64 {
	t.Helper()
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != "multica_llm_tokens_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "token_type" && label.GetValue() == "output" {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}
func TestTaskUsageCounterReplayCorrectionAndHistory(t *testing.T) {
	h, task, r := usageCounterFixture(t)
	post := func(usage ...map[string]any) {
		testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": usage}), "taskId", task)).Want(200)
	}
	post(counterUsage(10, ""))
	post(counterUsage(10, ""))
	var original time.Time
	dbfx.QueryRow(t, `SELECT created_at FROM task_usage WHERE task_id=$1`, task).Scan(&original)
	post(counterUsage(20, ""))
	var output, cost int64
	var created time.Time
	dbfx.QueryRow(t, `SELECT output_tokens,cost_usd_ticks,created_at FROM task_usage WHERE task_id=$1`, task).Scan(&output, &cost, &created)
	if output != 20 || cost != 37 || !created.Equal(original) {
		t.Fatalf("更正账本不符：output=%d cost=%d time=%v", output, cost, created)
	}
	if got := usageOutputCounter(t, r); got != 10 {
		t.Fatalf("实时重放/更正重复累计：%v", got)
	}
	historical := dbfx.Task(t, dbfx.Agent(t, "historical counter", testRuntimeID), testutil.Cols{"runtime_id": testRuntimeID, "issue_id": dbfx.Issue(t, "usage counter fixture"), "status": "completed", "created_at": testutil.Raw("now()-interval '3 hours'")})
	executed := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	for i := 0; i < 2; i++ {
		testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{counterUsage(90, executed)}}), "taskId", historical)).Want(200)
	}
	// 之后丢失executed_at的重复报告也不能把既有历史行再灌进实时counter。
	testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{counterUsage(90, "")}}), "taskId", historical)).Want(200)
	dbfx.QueryRow(t, `SELECT created_at FROM task_usage WHERE task_id=$1`, historical).Scan(&created)
	expectedTime, err := time.Parse(time.RFC3339, executed)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Equal(expectedTime) || usageOutputCounter(t, r) != 10 {
		t.Fatalf("历史时间或counter被重放：%v %v", created, usageOutputCounter(t, r))
	}
}
func TestTaskUsageCounterConcurrentFirstReport(t *testing.T) {
	h, task, r := usageCounterFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{counterUsage(11, "")}}), "taskId", task)).Want(200)
		}()
	}
	wg.Wait()
	if got := usageOutputCounter(t, r); got != 11 {
		t.Fatalf("并发首报重复：%v", got)
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM task_usage WHERE task_id=$1`, task).Scan(&count)
	if count != 1 {
		t.Fatalf("并发重复用量行：%d", count)
	}
}
func TestTaskUsageFailedBatchDoesNotEmitCountersOrPartialRows(t *testing.T) {
	h, task, r := usageCounterFixture(t)
	// 同一key两份不同数值会在回读校验拒绝；整个事务及监控应一致不生效。
	testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{counterUsage(10, ""), counterUsage(11, "")}}), "taskId", task)).Want(500)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM task_usage WHERE task_id=$1`, task).Scan(&count)
	if count != 0 || usageOutputCounter(t, r) != 0 {
		t.Fatalf("失败批次残留行或counter：%d %v", count, usageOutputCounter(t, r))
	}
	testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{counterUsage(10, ""), counterUsage(10, "")}}), "taskId", task)).Want(200)
	if usageOutputCounter(t, r) != 10 {
		t.Fatal("同批相同key重复计数")
	}
}

type usageCounterBarrier struct {
	pool    *pgxpool.Pool
	entered atomic.Int32
	both    chan struct{}
}

func (b *usageCounterBarrier) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	n := b.entered.Add(1)
	if n == 2 {
		close(b.both)
	}
	if n <= 2 {
		select {
		case <-b.both:
		case <-ctx.Done():
			tx.Rollback(context.Background())
			return nil, ctx.Err()
		}
	}
	return tx, nil
}
func TestTaskUsageEmptyProviderDoesNotBorrowConnectionInsideTransaction(t *testing.T) {
	h, task, _ := usageCounterFixture(t)
	cfg := testPool.Config()
	cfg.MaxConns = 2
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h.DB = pool
	h.Queries = db.New(pool)
	h.TxStarter = &usageCounterBarrier{pool: pool, both: make(chan struct{})}
	h.TaskService = service.NewTaskService(h.Queries, h.TxStarter, h.Hub, h.Bus)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			u := counterUsage(12, "")
			u["provider"] = ""
			req := withURLParam(newRequest(http.MethodPost, "/usage", map[string]any{"usage": []map[string]any{u}}).WithContext(ctx), "taskId", task)
			testutil.Call(t, h.ReportTaskUsage, req).Want(200)
		}()
	}
	wg.Wait()
}
func TestTaskUsageLockUsesCanonicalTaskUUID(t *testing.T) {
	h, task, r := usageCounterFixture(t)
	held, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	if _, err = held.Exec(context.Background(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "task-usage:"+task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	body := map[string]any{"usage": []map[string]any{counterUsage(14, "")}}
	testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", body).WithContext(ctx), "taskId", strings.ToUpper(task))).Want(500)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM task_usage WHERE task_id=$1`, task).Scan(&count)
	if count != 0 || usageOutputCounter(t, r) != 0 {
		t.Fatal("大写UUID绕过了同一任务锁")
	}
	if err = held.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, h.ReportTaskUsage, withURLParam(newRequest(http.MethodPost, "/usage", body), "taskId", strings.ToUpper(task))).Want(200)
	if usageOutputCounter(t, r) != 14 {
		t.Fatal("锁释放后未正常记账")
	}
}
