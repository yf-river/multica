package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/upstream"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpstreamTaskCannotWriteAnotherBatch(t *testing.T) {
	ctx := context.Background()
	agent := dbfx.Agent(t, "upstream scope", handlerTestRuntimeID(t), testutil.Cols{"kind": "system", "owner_id": testUserID})
	task := dbfx.Task(t, agent, testutil.Cols{"status": "running", "runtime_id": handlerTestRuntimeID(t)})
	other := dbfx.Task(t, agent, testutil.Cols{"status": "running", "runtime_id": handlerTestRuntimeID(t)})
	if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM upstream_sync_run WHERE task_id IN ($1,$2)`, task, other)
		testPool.Exec(ctx, `DELETE FROM upstream_sync_config WHERE singleton=1 AND agent_id=$1`, agent)
	})
	s := upstream.New(testPool)
	run, err := s.Start(ctx, task, agent, "manual")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, run, task string
		want            bool
	}{{"own batch", run.RunID, task, true}, {"other task", run.RunID, other, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := withURLParam(newRequest(http.MethodPost, "/api/upstream/runs/"+tc.run+"/finish", nil), "runId", tc.run)
			r.Header.Set("X-Actor-Source", "task_token")
			r.Header.Set("X-Task-ID", tc.task)
			r.Header.Set("X-Agent-ID", agent)
			w := httptest.NewRecorder()
			_, ok := testHandler.upstreamTaskScope(w, r)
			if ok != tc.want {
				t.Fatalf("scope=%v response=%d %s", ok, w.Code, w.Body.String())
			}
			if !tc.want && w.Code != http.StatusForbidden {
				t.Fatal("cross-task access did not return 403")
			}
		})
	}
}

// 同一个建议的并发请求只能提交一个任务，其他事务中的临时任务必须回滚。
func TestCreateUpstreamAbsorptionTaskConcurrent(t *testing.T) {
	ctx := context.Background()
	item, run := dbid.NewV7().String(), dbid.NewV7().String()
	title := "absorption-concurrency-" + item
	if _, err := testPool.Exec(ctx, `INSERT INTO upstream_sync_item(id,run_id,cluster_key,title,summary,rationale,verdict,confidence,presence) VALUES($1,$2,$4,$3,'摘要','与当前需求相关','review','medium','partial')`, item, run, title, item); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM upstream_sync_absorption_task WHERE item_id=$1`, item)
		testPool.Exec(ctx, `DELETE FROM upstream_sync_item WHERE id=$1`, item)
	})
	type response struct {
		code int
		body string
	}
	results := make(chan response, 4)
	for i := 0; i < 4; i++ {
		go func() {
			req := newRequest(http.MethodPost, "/api/upstream/items/"+item+"/absorption-task", map[string]any{})
			req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
			req = withURLParam(req, "itemId", item)
			rec := httptest.NewRecorder()
			testHandler.CreateUpstreamAbsorptionTask(rec, req)
			results <- response{rec.Code, rec.Body.String()}
		}()
	}
	var issue string
	created := 0
	for i := 0; i < 4; i++ {
		result := <-results
		if result.code != http.StatusCreated && result.code != http.StatusOK {
			t.Fatalf("request: %d %s", result.code, result.body)
		}
		var body struct {
			IssueID  string `json:"issue_id"`
			Existing bool   `json:"existing"`
		}
		if err := json.Unmarshal([]byte(result.body), &body); err != nil {
			t.Fatal(err)
		}
		if issue == "" {
			issue = body.IssueID
		}
		if issue != body.IssueID {
			t.Fatal("concurrent requests returned different issues")
		}
		if !body.Existing {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d tasks", created)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id=$1 AND title=$2`, testWorkspaceID, "评估上游改动："+title).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan tasks: %d %v", count, err)
	}
}
