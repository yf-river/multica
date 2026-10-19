package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/supervision"
	dbfixture "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIntSupervisionInvestigationScopeAndPersistentBudget(t *testing.T) {
	if os.Getenv("MULTICA_SUPERVISION_INT_TEST") != "1" {
		t.Skip("requires explicit fixed-int validation")
	}
	ctx := context.Background()
	raw, err := os.ReadFile("/data/ida/multica/.run/env/int-services.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]string
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["server"]["MULTICA_ENV_ID"] != "int" {
		t.Fatal("not int")
	}
	pool, err := pgxpool.New(ctx, cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfixture.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "调查作用域验收", "investigation-"+stamp+"@example.test", dbfixture.Cols{"account": "investigation-" + stamp})
	f.WorkspaceID = f.Workspace(t, "调查验收", "investigation-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "调查夹具", dbfixture.Cols{"status": "offline", "daemon_id": "investigation-" + stamp})
	agent := f.Agent(t, "调查夹具", runtime)
	f.InsertNoID(t, "supervision_config", dbfixture.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "agent_id": agent, "enabled": true, "investigation_enabled": true}, "workspace_id=$1", f.WorkspaceID)
	issue := f.Issue(t, "调查目标")
	other := f.Issue(t, "另一业务任务")
	source := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "issue_id": issue, "status": "completed", "completed_at": dbfixture.Raw("now()")})
	message := f.Insert(t, "task_message", dbfixture.Cols{"task_id": source, "seq": 1, "type": "text", "content": "mapping evidence"})
	foreign := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "issue_id": other, "status": "completed", "completed_at": dbfixture.Raw("now()")})
	foreignMessage := f.Insert(t, "task_message", dbfixture.Cols{"task_id": foreign, "seq": 1, "type": "text", "content": "foreign issue"})
	review := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "status": "running"})
	batch := f.Insert(t, "supervision_batch", dbfixture.Cols{"workspace_id": f.WorkspaceID, "issue_id": issue, "task_id": review, "target_version": 1, "status": "running"})
	f.Exec(t, "UPDATE agent_task_queue SET context=jsonb_build_object('type','quality_supervision','batch_id',$2::text) WHERE id=$1", review, batch)
	t.Cleanup(func() {
		for _, table := range []string{"supervision_investigation", "supervision_material", "supervision_subject"} {
			_, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID)
			if e != nil {
				t.Error(e)
			}
		}
	})
	h := &Handler{DB: pool, TxStarter: pool, Queries: db.New(pool), Supervision: supervision.New(pool, nil)}
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		setTaskTokenActor(r, agent, review)
		r.Header.Set("X-User-ID", f.UserID)
		r.Header.Set("X-Workspace-ID", f.WorkspaceID)
		w := httptest.NewRecorder()
		h.ReadSupervisionInvestigationMaterial(w, r)
		return w
	}
	path := "/api/supervision/investigation/material?kind=message&id=" + message + "&offset=0&reason=verify"
	if w := call(path); w.Code != 200 {
		t.Fatalf("read=%d %s", w.Code, w.Body.String())
	}
	if w := call("/api/supervision/investigation/material?kind=message&id=" + foreignMessage + "&offset=0&reason=verify"); w.Code != 404 {
		t.Fatalf("foreign=%d", w.Code)
	}
	f.Exec(t, "UPDATE supervision_batch SET status='cancelled' WHERE id=$1", batch)
	if w := call(path); w.Code != 403 {
		t.Fatalf("cancelled=%d", w.Code)
	}
	f.Exec(t, "UPDATE supervision_batch SET status='running' WHERE id=$1", batch)
	f.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
	if w := call(path); w.Code != 403 {
		t.Fatalf("lost owner=%d", w.Code)
	}
	f.Exec(t, "UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
	f.Exec(t, "INSERT INTO supervision_investigation(workspace_id,issue_id,batch_id,task_id,request,result) SELECT $1,$2,$3,$4,'{}','{\"text\":\"\"}' FROM generate_series(1,39)", f.WorkspaceID, issue, batch, review)
	if w := call(path); w.Code != 409 {
		t.Fatalf("persistent budget=%d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/api/supervision/investigation", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	h.SaveSupervisionInvestigation(w, r)
	if w.Code != 403 {
		t.Fatalf("human headers accepted=%d", w.Code)
	}
}
