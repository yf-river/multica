package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 系统执行体在运行时拆除时被整行删除，没有解绑步骤；它名下的自动化必须在删除前
// 一起暂停，否则状态仍是 active，调度会每天派发一条没有执行体的空跑。
func TestTeardownRuntimePausesSystemAgentAutopilots(t *testing.T) {
	ctx := context.Background()
	pool := newResolveOriginatorPool(t)
	bootstrap := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	ownerID := bootstrap.User(t, fmt.Sprintf("sync-owner-%d", suffix), fmt.Sprintf("sync-owner-%d@example.com", suffix))
	workspaceID := bootstrap.Workspace(t, fmt.Sprintf("sync-teardown-%d", suffix), fmt.Sprintf("sync-teardown-%d", suffix))
	fx := testutil.New(pool, workspaceID, ownerID)
	fx.Member(t, workspaceID, ownerID, "owner")

	runtimeID := fx.Runtime(t, "sync-runtime")
	agentID := fx.Agent(t, "上游观察员", runtimeID, testutil.Cols{
		"kind":       "system",
		"system_key": "upstream_sync:" + runtimeID,
	})
	pilotID := fx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id":    workspaceID,
		"title":           "上游同步（系统）",
		"description":     "",
		"assignee_id":     agentID,
		"assignee_type":   "agent",
		"status":          "active",
		"execution_mode":  "run_only",
		"created_by_type": "member",
		"created_by_id":   ownerID,
	})
	fx.Insert(t, "autopilot_trigger", testutil.Cols{
		"autopilot_id":    pilotID,
		"kind":            "schedule",
		"enabled":         true,
		"cron_expression": "0 9 * * *",
		"timezone":        "Asia/Shanghai",
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	res, err := TeardownRuntime(ctx, db.New(pool).WithTx(tx), util.MustParseUUID(runtimeID), RuntimeTeardownOptions{})
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	wantPilot := util.MustParseUUID(pilotID)
	paused := false
	for _, pilot := range res.PausedAutopilots {
		if pilot.ID == wantPilot {
			paused = pilot.Status == "paused"
		}
	}
	if !paused {
		t.Fatalf("system agent autopilot not reported as paused: %+v", res.PausedAutopilots)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM autopilot WHERE id=$1`, pilotID).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("autopilot status=%q err=%v", status, err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id=$1`, agentID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("system agent still present: %d %v", remaining, err)
	}
}
