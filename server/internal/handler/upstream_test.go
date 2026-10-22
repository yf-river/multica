package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/upstream"
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
