package handler

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/upstream"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// TestGetTaskStatusRenewsUpstreamLeaseForOwningDaemon 覆盖真实调用链：daemon 的任务状态
// 轮询经 handler 续租本任务名下的批次；换一台 daemon、或任务行已经终态都不续租。
func TestGetTaskStatusRenewsUpstreamLeaseForOwningDaemon(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	daemon := "daemon-lease-" + dbid.NewV7().String()
	runtime := dbfx.Runtime(t, "upstream lease poll", testutil.Cols{"daemon_id": daemon})
	agent := dbfx.Agent(t, "upstream lease poll", runtime)
	issue := dbfx.Issue(t, "upstream lease poll", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "running", "started_at": testutil.Raw("now()")})
	run := dbid.NewV7().String()
	if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_run(id,mode,trigger_source,task_id,status,limit_commits,lease_expires_at,started_at)
 VALUES($1,'incremental','manual',$2,'running',5,now()+interval '1 minute',now())`, run, task); err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM upstream_sync_run WHERE id=$1`, run)

	previous := testHandler.Upstream
	testHandler.Upstream = upstream.New(testPool)
	t.Cleanup(func() { testHandler.Upstream = previous })

	lease := func() time.Time {
		t.Helper()
		var at time.Time
		if err := testPool.QueryRow(ctx, `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	poll := func(daemonID string) map[string]string {
		var status map[string]string
		testutil.Call(t, testHandler.GetTaskStatus,
			withURLParam(newDaemonTokenRequest("GET", "/status", nil, testWorkspaceID, daemonID), "taskId", task)).Want(200).JSON(&status)
		return status
	}

	before := lease()
	if got := poll(daemon); got["status"] != "running" {
		t.Fatalf("状态轮询返回 %v", got)
	}
	if after := lease(); !after.After(before) {
		t.Fatalf("本任务轮询没有续租: %s -> %s", before, after)
	}

	renewed := lease()
	if got := poll("daemon-other-" + dbid.NewV7().String()); got["status"] != "running" {
		t.Fatalf("状态轮询返回 %v", got)
	}
	if after := lease(); !after.Equal(renewed) {
		t.Fatalf("别的 daemon 改动了租约: %s -> %s", renewed, after)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled', completed_at=now() WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if got := poll(daemon); got["status"] != "cancelled" {
		t.Fatalf("终态任务的状态返回 %v", got)
	}
	if after := lease(); !after.Equal(renewed) {
		t.Fatalf("终态任务的批次仍被续租: %s -> %s", renewed, after)
	}
}
