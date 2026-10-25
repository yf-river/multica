package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/upstream"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 用真实 HTTP、成员鉴权和工作区中间件覆盖系统执行体的取消入口。
// TaskService 的取消测试本身不能发现 GetAgentInWorkspace 隐藏 system Agent 的问题。
func TestCancelUpstreamSystemTaskHTTP(t *testing.T) {
	ctx := context.Background()
	previous := testHandler.TaskService.UpstreamRuns
	testHandler.TaskService.UpstreamRuns = upstream.New(testPool)
	t.Cleanup(func() { testHandler.TaskService.UpstreamRuns = previous })
	tag, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() == 1 {
		dbfx.Cleanup(t, `DELETE FROM upstream_sync_config WHERE singleton=1`)
	}
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
	router.Post("/api/tasks/{taskId}/cancel", testHandler.CancelTaskByUser)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	cases := []struct {
		name    string
		kind    string
		key     string
		run     bool
		member  bool
		foreign bool
		want    int
		binding string
		queued  bool
	}{
		{"上游管理员", "system", upstream.ExecutorSystemKeyPrefix, true, false, false, 200, "", false},
		{"普通成员不能取消上游任务", "system", upstream.ExecutorSystemKeyPrefix, true, true, false, 403, "", false},
		{"工作区不可跨越", "system", upstream.ExecutorSystemKeyPrefix, true, false, true, 404, "", false},
		{"其它系统用途不开放", "system", "unrelated_system:", true, false, false, 404, "", false},
		{"没有同步批次不开放", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404, "", false},
		{"预启动任务管理员取消", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 200, "exact", false},
		{"排队任务管理员取消", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 200, "exact", true},
		{"预启动普通成员拒绝", "system", upstream.ExecutorSystemKeyPrefix, false, true, false, 403, "exact", false},
		{"错误自动化绑定拒绝", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404, "wrong_autopilot", false},
		{"错误执行体绑定拒绝", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404, "wrong_agent", false},
		{"错误运行时绑定拒绝", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404, "wrong_runtime", false},
		{"错误派发回链拒绝", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404, "wrong_task", false},
		{"普通开发任务兼容", "user", "", false, false, false, 200, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := dbfx.Runtime(t, "cancel HTTP")
			cols := testutil.Cols{"kind": tc.kind}
			if tc.key != "" {
				cols["system_key"] = tc.key + dbid.NewV7().String()
			}
			agent := dbfx.Agent(t, "cancel HTTP", runtime, cols)
			initialStatus := "running"
			if tc.queued {
				initialStatus = "queued"
			}
			task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": initialStatus})
			if tc.binding != "" {
				ap, ar := dbid.NewV7().String(), dbid.NewV7().String()
				_, err := testPool.Exec(ctx, `INSERT INTO autopilot(id,workspace_id,title,assignee_id,execution_mode,created_by_type,created_by_id) VALUES($1,$2,'cancel test',$3,'run_only','member',$4)`, ap, testWorkspaceID, agent, testUserID)
				if err != nil {
					t.Fatal(err)
				}
				dbfx.Cleanup(t, `DELETE FROM autopilot WHERE id=$1`, ap)
				linkedTask := task
				if tc.binding == "wrong_task" {
					linkedTask = dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "queued"})
				}
				_, err = testPool.Exec(ctx, `INSERT INTO autopilot_run(id,autopilot_id,source,status,task_id) VALUES($1,$2,'manual','running',$3)`, ar, ap, linkedTask)
				if err != nil {
					t.Fatal(err)
				}
				dbfx.Cleanup(t, `DELETE FROM autopilot_run WHERE id=$1`, ar)
				if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET autopilot_run_id=$2 WHERE id=$1`, task, ar); err != nil {
					t.Fatal(err)
				}
				boundAP, boundAgent, boundRuntime := ap, agent, runtime
				if tc.binding == "wrong_autopilot" {
					boundAP = dbid.NewV7().String()
				}
				if tc.binding == "wrong_agent" {
					boundAgent = dbid.NewV7().String()
				}
				if tc.binding == "wrong_runtime" {
					boundRuntime = dbid.NewV7().String()
				}
				if _, err = testPool.Exec(ctx, `UPDATE upstream_sync_config SET autopilot_id=$1,agent_id=$2,runtime_id=$3 WHERE singleton=1`, boundAP, boundAgent, boundRuntime); err != nil {
					t.Fatal(err)
				}
				dbfx.Cleanup(t, `UPDATE upstream_sync_config SET autopilot_id=NULL,agent_id=NULL,runtime_id=NULL WHERE singleton=1`)
			}
			run := ""
			if tc.run {
				run = dbid.NewV7().String()
				if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_run(id,mode,trigger_source,task_id,status,limit_commits,lease_expires_at,started_at)
 VALUES($1,'incremental','manual',$2,'running',2,now()+interval '1 minute',now())`, run, task); err != nil {
					t.Fatal(err)
				}
				dbfx.Cleanup(t, `DELETE FROM upstream_sync_run WHERE id=$1`, run)
			}
			user, workspace := testUserID, testWorkspaceID
			if tc.member {
				user = dbfx.User(t, "cancel member", dbid.NewV7().String()+"@example.test")
				dbfx.Member(t, workspace, user, "member")
			}
			if tc.foreign {
				workspace = dbfx.Workspace(t, "other cancel workspace", "cancel-"+dbid.NewV7().String())
				dbfx.Member(t, workspace, user, "owner")
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": user, "exp": time.Now().Add(time.Hour).Unix()}).SignedString(auth.JWTSecret())
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/tasks/"+task+"/cancel", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Workspace-ID", workspace)
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("HTTP %d，预期 %d: %s", response.StatusCode, tc.want, body)
			}
			var status string
			if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1`, task).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if tc.want == 200 && status != "cancelled" || tc.want != 200 && status != initialStatus {
				t.Fatalf("任务状态与取消权限不符: %s", status)
			}
			if run != "" {
				var lease *time.Time
				if err := testPool.QueryRow(ctx, `SELECT status,lease_expires_at FROM upstream_sync_run WHERE id=$1`, run).Scan(&status, &lease); err != nil {
					t.Fatal(err)
				}
				if tc.want == 200 && (status != "interrupted" || lease != nil) || tc.want != 200 && status != "running" {
					t.Fatalf("同步批次状态与取消权限不符: %s %v", status, lease)
				}
			}
		})
	}
}
