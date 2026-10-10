package health_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
)

// 所有写入都在外层事务中回滚，不初始化账号，不向真实用户留下测试指标。
func TestHealthDatabaseVersions(t *testing.T) {
	if os.Getenv("MULTICA_HEALTH_DB_TEST") != "1" {
		t.Skip("需要显式启用联调数据库验证")
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
	if user == "" || workspace == "" {
		t.Fatal("需要已授权用户与工作区 ID")
	}
	perform := func(method string, body any, run func(http.ResponseWriter, *http.Request), status int) map[string]json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/health", strings.NewReader(string(data)))
		r.Header.Set("X-User-ID", user)
		r.Header.Set("X-Workspace-ID", workspace)
		w := httptest.NewRecorder()
		run(w, r)
		if w.Code != status {
			t.Fatalf("health request status %d expected %d: %s", w.Code, status, w.Body.String())
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	readRevision := func() int64 {
		out := perform("GET", nil, h.GetHealth, 200)
		var revision int64
		json.Unmarshal(out["revision"], &revision)
		return revision
	}
	initial := readRevision()
	for i := 0; i < 12; i++ {
		perform("PATCH", map[string]any{"revision": initial + int64(i), "profile": map[string]any{"name": fmt.Sprintf("事务验证-%d", i), "sex": "男", "birth_date": "2000-07-26"}}, h.UpdateHealthProfile, 200)
	}
	versions := perform("GET", nil, h.GetHealthRevisions, 200)
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(versions["items"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 10 {
		t.Fatalf("history count=%d", len(items))
	}
	current := readRevision()
	perform("PATCH", map[string]any{"revision": initial, "profile": map[string]any{"name": "stale"}}, h.UpdateHealthProfile, 409)
	var target int64
	json.Unmarshal(items[4]["revision"], &target)
	perform("POST", map[string]any{"revision": current, "target": target}, h.RestoreHealthRevision, 200)
	after := perform("GET", nil, h.GetHealth, 200)
	var epoch int64
	json.Unmarshal(after["epoch"], &epoch)
	if epoch < 1 {
		t.Fatal("restore did not invalidate old task epoch")
	}
	if readRevision() != current+1 {
		t.Fatal("restore did not create a new version")
	}
	perform("POST", map[string]any{"revision": current + 1, "target": current}, h.RestoreHealthRevision, 200)
	restored := perform("GET", nil, h.GetHealth, 200)
	var profile map[string]string
	json.Unmarshal(restored["profile"], &profile)
	if profile["name"] != "事务验证-11" {
		t.Fatal("restore before restore was not retained")
	}
}
