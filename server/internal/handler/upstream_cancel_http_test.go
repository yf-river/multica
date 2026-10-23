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
	router.Use(middleware.Auth(testHandler.Queries, nil, nil))
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
	}{
		{"上游管理员", "system", upstream.ExecutorSystemKeyPrefix, true, false, false, 200},
		{"普通成员不能取消上游任务", "system", upstream.ExecutorSystemKeyPrefix, true, true, false, 403},
		{"工作区不可跨越", "system", upstream.ExecutorSystemKeyPrefix, true, false, true, 404},
		{"其它系统用途不开放", "system", "unrelated_system:", true, false, false, 404},
		{"没有同步批次不开放", "system", upstream.ExecutorSystemKeyPrefix, false, false, false, 404},
		{"普通开发任务兼容", "user", "", false, false, false, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := dbfx.Runtime(t, "cancel HTTP")
			cols := testutil.Cols{"kind": tc.kind}
			if tc.key != "" {
				cols["system_key"] = tc.key + dbid.NewV7().String()
			}
			agent := dbfx.Agent(t, "cancel HTTP", runtime, cols)
			task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running"})
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
			if tc.want == 200 && status != "cancelled" || tc.want != 200 && status != "running" {
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
