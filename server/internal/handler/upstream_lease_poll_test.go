package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
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

// 通过真实鉴权中间件验证本机 CLI 使用的三种凭证，避免手工注入 daemon context
// 的测试漏掉 JWT/PAT 登录后无法续租的真实入口。
func TestTaskLeasePollThroughDaemonAuth(t *testing.T) {
	for _, kind := range []string{"jwt", "pat", "mdt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			daemon := "lease-auth-" + dbid.NewV7().String()
			runtime := dbfx.Runtime(t, "lease auth", testutil.Cols{"daemon_id": daemon})
			agent := dbfx.Agent(t, "lease auth", runtime)
			issue := dbfx.Issue(t, "lease auth", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
			task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "running", "started_at": testutil.Raw("now()")})
			run := dbid.NewV7().String()
			if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_run(id,mode,trigger_source,task_id,status,limit_commits,lease_expires_at,started_at)
 VALUES($1,'incremental','manual',$2,'running',2,now()+interval '2 seconds',now())`, run, task); err != nil {
				t.Fatal(err)
			}
			dbfx.Cleanup(t, `DELETE FROM upstream_sync_run WHERE id=$1`, run)
			previous := testHandler.Upstream
			testHandler.Upstream = upstream.New(testPool)
			testHandler.Upstream.LeaseTTL = 2 * time.Second
			t.Cleanup(func() { testHandler.Upstream = previous })

			var token string
			var err error
			switch kind {
			case "jwt":
				token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": testUserID, "exp": time.Now().Add(time.Hour).Unix()}).SignedString(auth.JWTSecret())
			case "pat":
				token, err = auth.GeneratePATToken()
				id := dbid.NewV7().String()
				if err == nil {
					_, err = testPool.Exec(ctx, `INSERT INTO personal_access_token(id,user_id,name,token_hash,token_prefix) VALUES($1,$2,'lease auth',$3,'mul_test')`, id, testUserID, auth.HashToken(token))
				}
				dbfx.Cleanup(t, `DELETE FROM personal_access_token WHERE id=$1`, id)
			case "mdt":
				token, err = auth.GenerateDaemonToken()
				id := dbid.NewV7().String()
				if err == nil {
					_, err = testPool.Exec(ctx, `INSERT INTO daemon_token(id,token_hash,workspace_id,daemon_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, id, auth.HashToken(token), testWorkspaceID, daemon)
				}
				dbfx.Cleanup(t, `DELETE FROM daemon_token WHERE id=$1`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			endpoint := middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.GetTaskStatus))
			poll := func() {
				req := withURLParam(httptest.NewRequest("GET", "/status", nil), "taskId", task)
				req.Header.Set("Authorization", "Bearer "+token)
				// 客户端传入的机器身份不能替代真实鉴权与任务归属。
				req.Header.Set("X-Daemon-ID", "untrusted-client-value")
				w := httptest.NewRecorder()
				endpoint.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("poll: %d %s", w.Code, w.Body.String())
				}
			}
			start := time.Now()
			for time.Since(start) < 3*time.Second {
				poll()
				time.Sleep(400 * time.Millisecond)
			}
			var alive bool
			if err := testPool.QueryRow(ctx, `SELECT lease_expires_at>now() FROM upstream_sync_run WHERE id=$1`, run).Scan(&alive); err != nil || !alive {
				t.Fatalf("%s 身份的真实状态轮询没有维持有效租约: %v %v", kind, alive, err)
			}
			if kind != "mdt" {
				other := dbfx.User(t, "other runtime owner", "lease-"+dbid.NewV7().String()+"@example.test")
				var before, after time.Time
				if err := testPool.QueryRow(ctx, `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$2 WHERE id=$1`, runtime, other); err != nil {
					t.Fatal(err)
				}
				poll()
				if err := testPool.QueryRow(ctx, `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&after); err != nil || !after.Equal(before) {
					t.Fatalf("成员替别人的私有 runtime 续租: %v", err)
				}
				if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$2 WHERE id=$1`, runtime, testUserID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, task); err != nil {
				t.Fatal(err)
			}
			var before, after time.Time
			if err := testPool.QueryRow(ctx, `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&before); err != nil {
				t.Fatal(err)
			}
			poll()
			if err := testPool.QueryRow(ctx, `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&after); err != nil || !after.Equal(before) {
				t.Fatalf("终态任务被续租: %v", err)
			}
		})
	}
}

// 通过生产使用的 TaskService 取消入口验证终态消费者确实接入批次关闭，
// 与直接调用 CloseTaskRuns 的存储层测试提供不同证据。
func TestCancelTaskClosesUpstreamRun(t *testing.T) {
	ctx := context.Background()
	runtime := dbfx.Runtime(t, "cancel upstream")
	agent := dbfx.Agent(t, "cancel upstream", runtime)
	issue := dbfx.Issue(t, "cancel upstream", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "running"})
	run := dbid.NewV7().String()
	tag, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() == 1 {
		dbfx.Cleanup(t, `DELETE FROM upstream_sync_config WHERE singleton=1`)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_run(id,mode,trigger_source,task_id,status,limit_commits,lease_expires_at,started_at)
 VALUES($1,'incremental','manual',$2,'running',2,now()+interval '1 minute',now())`, run, task); err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM upstream_sync_run WHERE id=$1`, run)
	previous := testHandler.TaskService.UpstreamRuns
	testHandler.TaskService.UpstreamRuns = upstream.New(testPool)
	t.Cleanup(func() { testHandler.TaskService.UpstreamRuns = previous })
	if _, err := testHandler.TaskService.CancelTaskByUser(ctx, parseUUID(task)); err != nil {
		t.Fatal(err)
	}
	var status string
	var lease *time.Time
	if err := testPool.QueryRow(ctx, `SELECT status,lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&status, &lease); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || lease != nil {
		t.Fatalf("真实任务取消未关闭批次: %q %v", status, lease)
	}
}
