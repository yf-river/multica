package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestDeliveryDiagnosticWindow(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=201", "limit=x", "since=yesterday", "after=id"} {
		if _, _, _, err := deliveryDiagnosticWindow(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
	n, since, after, err := deliveryDiagnosticWindow(httptest.NewRequest("GET", "/?limit=2&since=2026-10-24T01:00:00Z&after=abc", nil))
	if err != nil || n != 2 || since.IsZero() || after != "abc" {
		t.Fatalf("window: %v %v %v %v", n, since, after, err)
	}
}

func TestDeliveryDiagnosticSummary(t *testing.T) {
	now := time.Now()
	started := now.Add(-time.Minute)
	raw := json.RawMessage(`{"publication":{"state":"held","delivery_id":"other","expired":false},"slots":[]}`)
	result := deliveryDiagnosticSummary("approved", "", "mine", false, "", raw, &started, now)
	if result["phase_elapsed_ms"] != int64(60000) || result["holder_delivery_id"] != "other" {
		t.Fatal(result)
	}
	result = deliveryDiagnosticSummary("testing", "waiting_slot", "mine", false, "", raw, nil, now)
	if result["phase_elapsed_ms"] != nil || len(result["blocked_by"].([]string)) != 1 {
		t.Fatal(result)
	}
	events := []deliveryDiagnosticEvent{{At: started}, {At: now}}
	diagnosticIntervals(events)
	if *events[0].UntilNextMS != 60000 || events[1].UntilNextMS != nil {
		t.Fatal(events)
	}
}

func TestDeliveryDiagnosticsRealPostgres(t *testing.T) {
	issue := dbfx.Issue(t, "Delivery diagnostics")
	d := dbfx.Insert(t, "issue_delivery", testutil.Cols{"id": uuidToString(dbid.NewV7()), "workspace_id": testWorkspaceID, "issue_id": issue, "operation_id": uuidToString(dbid.NewV7()), "plan_id": uuidToString(dbid.NewV7()), "plan_snapshot": `{}`, "source_commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "source_branch": "feature/test", "base_commit": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "phase": "testing", "verification_comment_id": uuidToString(dbid.NewV7()), "release_comment_id": uuidToString(dbid.NewV7())})
	dbfx.Cleanup(t, "DELETE FROM verification_event WHERE delivery_id=$1", d)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordDeliveryProgress(ctx, tx, testWorkspaceID, d, "claim", "waiting_int", 7); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = testPool.QueryRow(ctx, "SELECT count(*) FROM verification_event WHERE delivery_id=$1", d).Scan(&count); err != nil || count != 0 {
		t.Fatalf("non-atomic event %d %v", count, err)
	}
	for i := 0; i < 4; i++ {
		tx, err = testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = recordDeliveryProgress(ctx, tx, testWorkspaceID, d, "claim", "waiting_int", 7); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	get := func(query string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("GET", "/api/issues/"+issue+"/deliveries/diagnostics?"+query, nil), "id", issue)
		testHandler.GetDeliveryDiagnostics(w, req)
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := get("limit=2")
	if first["truncated_older_events"] != true || len(first["events"].([]any)) != 2 {
		t.Fatal(first)
	}
	// 从早于全部事件的游标读取，必须按时间正向取页；不能跳到末尾丢掉中间事件。
	page := get("limit=2&since=2020-01-01T00:00:00Z")
	if page["has_more_newer_events"] != true {
		t.Fatal(page)
	}
	cursor := page["next_cursor"].(map[string]any)
	next := get("limit=2&since=" + url.QueryEscape(cursor["since"].(string)) + "&after=" + cursor["after"].(string))
	if next["has_more_newer_events"] != false || len(next["events"].([]any)) != 2 {
		t.Fatal(next)
	}
	if page["events"].([]any)[1].(map[string]any)["id"] == next["events"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("duplicate cursor")
	}
	// 非本任务交付不可读；查询不领取 owner、修改租约或推进交付。
	w := httptest.NewRecorder()
	testHandler.GetDeliveryDiagnostics(w, withURLParam(newRequest("GET", "/?delivery_id="+uuidToString(dbid.NewV7()), nil), "id", issue))
	if w.Code != 404 {
		t.Fatalf("foreign delivery status %d", w.Code)
	}
	var revision int64
	if err = testPool.QueryRow(ctx, "SELECT revision FROM issue_delivery WHERE id=$1", d).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("read changed delivery: %d %v", revision, err)
	}
}
