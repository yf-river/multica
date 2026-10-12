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

// 日常写入、旧日期覆盖和恢复在外层事务中回滚。
func TestHealthDailyBMIAndOrder(t *testing.T) {
	if os.Getenv("MULTICA_HEALTH_DB_TEST") != "1" {
		t.Skip("需要显式启用数据库验证")
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
		t.Fatal("需要本人及工作区身份")
	}
	call := func(method string, path string, body any, run http.HandlerFunc, status int) map[string]json.RawMessage {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Header.Set("X-User-ID", user)
		r.Header.Set("X-Workspace-ID", workspace)
		out := map[string]json.RawMessage{}
		testutil.Call(t, run, r).Want(status).JSON(&out)
		return out
	}
	type overview struct {
		Revision int64                `json:"revision"`
		Current  []health.Observation `json:"current"`
		BMI      *health.BMI          `json:"computed_bmi"`
	}
	read := func() overview {
		out := call("GET", "/api/health/overview", nil, h.GetHealth, 200)
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var result overview
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := read()
	write := func(revision int64, values []health.Observation, status int) {
		call("POST", "/api/health/observations", map[string]any{"revision": revision, "observations": values}, h.SaveHealthObservation, status)
	}
	write(initial.Revision, []health.Observation{{Code: "height", Name: "身高", Value: "180", Unit: "cm", ObservedAt: "2025-01-01"}, {Code: "weight", Name: "体重", Value: "81", Unit: "kg", ObservedAt: "2026-01-01"}}, 200)
	first := read()
	if first.Revision != initial.Revision+1 || first.BMI == nil || first.BMI.Value != 25 || first.Current[0].Code != "height" || first.Current[1].Code != "weight" {
		t.Fatal("同批写入、计算 BMI 或批内顺序错误")
	}
	write(first.Revision, []health.Observation{{Code: "weight", Name: "体重", Value: "90", Unit: "kg", ObservedAt: "2024-01-01"}}, 200)
	second := read()
	if second.Revision != first.Revision+1 || second.Current[0].Code != "weight" || second.BMI == nil || second.BMI.Value != 27.8 || second.BMI.Weight.ObservedAt != "2024-01-01" {
		t.Fatal("旧日期手工录入必须置顶并联动 BMI")
	}
	write(second.Revision, []health.Observation{{Code: "weight", Name: "体重", Value: "0", Unit: "kg", ObservedAt: "2024-01-01"}}, 400)
	if read().Revision != second.Revision {
		t.Fatal("无效输入产生了版本")
	}
	write(first.Revision, []health.Observation{{Code: "weight", Name: "体重", Value: "91", Unit: "kg", ObservedAt: "2024-01-01"}}, 409)
	call("POST", "/api/health/revisions/restore", map[string]any{"revision": second.Revision, "target": first.Revision}, h.RestoreHealthRevision, 200)
	restored := read()
	if restored.BMI == nil || restored.BMI.Value != 25 || restored.Current[0].ID != first.Current[0].ID || restored.Current[1].ID != first.Current[1].ID {
		t.Fatal("恢复必须还原采用顺序并重新计算 BMI")
	}
	for i := 2; i < len(first.Current); i++ {
		if restored.Current[i].ID != first.Current[i].ID {
			t.Fatal("未涉及项目的顺序改变")
		}
	}
}
