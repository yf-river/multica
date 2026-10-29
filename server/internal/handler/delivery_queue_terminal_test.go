package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestDeliveryQueueTerminalNoticeMatchesPhase(t *testing.T) {
	if testHandler == nil {
		t.Fatal("isolated PostgreSQL is required")
	}
	publishedOld, _ := batchFixture(t)
	changesOld, _ := batchFixture(t)
	publishedPending, _ := batchFixture(t)
	changesPending, _ := batchFixture(t)
	active, _ := batchFixture(t)
	for _, f := range []struct{ id, phase string }{{publishedOld, "published"}, {changesOld, "needs_changes"}, {publishedPending, "published"}, {changesPending, "needs_changes"}} {
		dbfx.Exec(t, `UPDATE issue_delivery SET phase=$2 WHERE id=$1`, f.id, f.phase)
	}
	for _, f := range []struct {
		id, stage string
		delivered bool
	}{{publishedOld, "published", true}, {publishedOld, "batch_ready", false}, {changesOld, "needs_changes", true}, {changesOld, "batch_ready", false}, {publishedPending, "published", false}, {changesPending, "needs_changes", false}} {
		dbfx.Exec(t, `INSERT INTO delivery_notice(delivery_id,stage,comment_id,delivered_at) VALUES($1,$2,$3,CASE WHEN $4 THEN now() ELSE NULL END)`, f.id, f.stage, dbid.NewV7(), f.delivered)
	}
	var rows []map[string]any
	testutil.Call(t, testHandler.ListDeliveryQueue, newRequest(http.MethodGet, "/queue", nil)).Want(200).JSON(&rows)
	positions := map[string]int{}
	for i, row := range rows {
		positions[row["id"].(string)] = i
	}
	for _, id := range []string{publishedOld, changesOld} {
		if _, ok := positions[id]; ok {
			t.Errorf("stale batch_ready queues terminal delivery %s", id)
		}
	}
	for _, id := range []string{publishedPending, changesPending, active} {
		if _, ok := positions[id]; !ok {
			t.Errorf("eligible delivery missing: %s", id)
		}
	}
	if positions[active] >= positions[publishedPending] {
		t.Error("active delivery should precede terminal notice")
	}
	rows = nil
	testutil.Call(t, testHandler.ListDeliveryQueue, newRequest(http.MethodGet, "/queue?current="+publishedOld, nil)).Want(200).JSON(&rows)
	if len(rows) == 0 || rows[0]["id"] != publishedOld {
		t.Error("current delivery must be forced and prioritized")
	}
}
