package meals_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/meals"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"os"
	"testing"
)

// 使用现有联调身份，测试变更和任务创建由外层事务整体回滚。
func TestMealsDatabaseIsolationAndRetry(t *testing.T) {
	if os.Getenv("MULTICA_MEALS_DB_TEST") != "1" {
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
	user, workspace := os.Getenv("MULTICA_MEALS_TEST_USER_ID"), os.Getenv("MULTICA_MEALS_TEST_WORKSPACE_ID")
	if user == "" || workspace == "" {
		t.Fatal("需要现有联调身份")
	}
	h := handler.New(db.New(tx), tx, nil, events.New(), nil, nil, nil, analytics.NoopClient{}, handler.Config{})
	type response struct {
		State    meals.State `json:"state"`
		Revision int64       `json:"revision"`
	}
	call := func(run http.HandlerFunc, body any, status int) response {
		t.Helper()
		request := testutil.WithHeaders(testutil.JSONRequest("POST", "/api/meals/actions", body), "X-User-ID", user, "X-Workspace-ID", workspace)
		var out response
		testutil.Call(t, run, request).Want(status).JSON(&out)
		return out
	}
	initial := call(h.GetMeals, nil, 200)
	quantity := 2.0
	c := meals.Command{Action: "entry_save", Revision: initial.Revision, RequestID: meals.ID(), Entry: meals.Entry{Name: "饮食事务验收", Date: "2098-10-13", Slot: "lunch", Portions: 2, Mode: "home", Foods: []meals.Food{{Name: "鸡蛋", Quantity: &quantity, Unit: "个", Basis: "个数", Origin: "manual"}}}}
	saved := call(h.MealAction, c, 200)
	replay := call(h.MealAction, c, 200)
	if replay.Revision != saved.Revision || len(replay.State.Entries) != len(saved.State.Entries) {
		t.Fatal("request retry wrote another meal")
	}
	c.RequestID = meals.ID()
	call(h.MealAction, c, 409)
	saved = call(h.MealAction, meals.Command{Action: "shopping_generate", Week: meals.Week(c.Entry.Date), Revision: saved.Revision, RequestID: meals.ID()}, 200)
	if len(saved.State.Shopping) == 0 {
		t.Fatal("shopping not persisted")
	}
	cross := testutil.WithHeaders(testutil.JSONRequest("GET", "/api/meals", nil), "X-User-ID", user, "X-Workspace-ID", meals.ID())
	testutil.Call(t, h.GetMeals, cross).Want(404)
	c = meals.Command{Action: "batch_save", Revision: saved.Revision, RequestID: meals.ID(), Batch: meals.Batch{Name: "饮食事务批次", Portions: 3, MadeAt: "2026-10-11", Storage: "frozen", PlannedDates: []string{}}}
	stock := call(h.MealAction, c, 200)
	batch := stock.State.Batches[len(stock.State.Batches)-1]
	take := meals.Command{Action: "batch_use", Revision: stock.Revision, RequestID: meals.ID(), ID: batch.ID, Use: meals.Use{Date: "2026-10-11", Portions: 2}}
	used := call(h.MealAction, take, 200)
	call(h.MealAction, take, 200)
	take.RequestID = meals.ID()
	take.Revision = used.Revision
	call(h.MealAction, take, 400)
	link := meals.Command{Action: "task_link", Revision: used.Revision, RequestID: meals.ID(), Week: "2098-10-13"}
	a := call(h.MealAction, link, 200)
	b := call(h.MealAction, link, 200)
	if len(a.State.Tasks) != len(b.State.Tasks) {
		t.Fatal("task linkage duplicated")
	}
}
