package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/health"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 使用现有本人报告，所有应用、回退和任务状态更新都在外层事务中回滚。
func TestHealthApplyExistingReportAndRestore(t *testing.T) {
	if os.Getenv("MULTICA_HEALTH_DB_TEST") != "1" {
		t.Skip("需要显式启用数据库验证")
	}
	reportID := os.Getenv("MULTICA_HEALTH_TEST_REPORT_ID")
	if reportID == "" {
		t.Skip("需要现有报告 ID")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	h := handler.New(db.New(tx), tx, nil, events.New(), nil, nil, nil, analytics.NoopClient{}, handler.Config{})
	user, workspace := os.Getenv("MULTICA_HEALTH_TEST_USER_ID"), os.Getenv("MULTICA_HEALTH_TEST_WORKSPACE_ID")
	call := func(method string, body any, run http.HandlerFunc, status int) map[string]json.RawMessage {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/health/reports/"+reportID, strings.NewReader(string(data)))
		r.Header.Set("X-User-ID", user)
		r.Header.Set("X-Workspace-ID", workspace)
		r = testutil.WithURLParams(r, "reportId", reportID)
		out := map[string]json.RawMessage{}
		testutil.Call(t, run, r).Want(status).JSON(&out)
		return out
	}
	read := func() (int64, []health.Observation) {
		out := call("GET", nil, h.GetHealth, 200)
		var revision int64
		var current []health.Observation
		json.Unmarshal(out["revision"], &revision)
		json.Unmarshal(out["current"], &current)
		return revision, current
	}
	initial, before := read()
	if len(before) != 102 && len(before) != 116 {
		t.Fatalf("既有报告应用前当前指标数异常: %d", len(before))
	}
	var original []byte
	if err := tx.QueryRow(ctx, `SELECT data FROM health_report_result WHERE report_id=$1 ORDER BY attempt DESC LIMIT 1`, reportID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	call("POST", map[string]any{"revision": initial}, h.ApplyHealthReport, 200)
	afterRevision, after := read()
	if len(after) != 116 {
		t.Fatalf("既有报告未整批应用 102+14 条提取记录: %d", len(after))
	}
	if len(before) == 102 && afterRevision != initial+1 {
		t.Fatal("首次整批应用没有生成一个版本")
	}
	if len(before) == 116 && afterRevision != initial {
		t.Fatal("已应用报告重复生成版本")
	}
	call("POST", map[string]any{"revision": afterRevision}, h.ApplyHealthReport, 200)
	if got, _ := read(); got != afterRevision {
		t.Fatal("重复应用产生额外版本")
	}
	if afterRevision != initial {
		call("POST", map[string]any{"revision": initial}, h.ApplyHealthReport, 409)
		call("POST", map[string]any{"target": initial, "revision": afterRevision}, h.RestoreHealthRevision, 200)
		restoredRevision, restored := read()
		if len(restored) != len(before) {
			t.Fatal("恢复没有还原原采用集合")
		}
		call("POST", map[string]any{"target": afterRevision, "revision": restoredRevision}, h.RestoreHealthRevision, 200)
		_, again := read()
		if len(again) != len(after) {
			t.Fatal("恢复前的当前版本无法恢复")
		}
	}
	var unchanged []byte
	if err := tx.QueryRow(ctx, `SELECT data FROM health_report_result WHERE report_id=$1 ORDER BY attempt DESC LIMIT 1`, reportID).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatal("原始提取结果被改写")
	}
}
