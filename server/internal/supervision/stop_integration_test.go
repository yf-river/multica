package supervision_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIntSupervisionStopAllAndOne(t *testing.T) {
	if os.Getenv("MULTICA_SUPERVISION_INT_TEST") != "1" {
		t.Skip("requires fixed int")
	}
	raw, err := os.ReadFile("/data/ida/multica/.run/env/int-services.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]string
	if err = json.Unmarshal(raw, &cfg); err != nil || cfg["server"]["MULTICA_ENV_ID"] != "int" {
		t.Fatal("not int")
	}
	pool, err := pgxpool.New(context.Background(), cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "停止验收", "stop-"+stamp+"@example.test", dbfx.Cols{"account": "stop-" + stamp})
	f.WorkspaceID = f.Workspace(t, "停止验收", "stop-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "离线停止夹具", dbfx.Cols{"status": "offline"})
	agent := f.Agent(t, "停止夹具", runtime)
	f.InsertNoID(t, "supervision_config", dbfx.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "agent_id": agent, "enabled": true}, "workspace_id=$1", f.WorkspaceID)
	t.Cleanup(func() {
		for _, table := range []string{"supervision_action", "supervision_finding", "supervision_batch", "supervision_material", "supervision_subject", "supervision_config"} {
			f.Exec(t, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID)
		}
	})
	h := &handler.Handler{DB: pool, Queries: db.New(pool), TxStarter: pool, TaskService: service.NewTaskService(db.New(pool), pool, nil, nil)}
	for _, all := range []bool{false, true} {
		f.Exec(t, "UPDATE supervision_config SET enabled=true WHERE workspace_id=$1", f.WorkspaceID)
		issues := []string{}
		for i := 0; i < 3; i++ {
			id := f.Issue(t, fmt.Sprintf("stop %v %d", all, i))
			issues = append(issues, id)
			task := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "status": "queued", "context": map[string]any{"type": "quality_supervision"}})
			f.Insert(t, "supervision_batch", dbfx.Cols{"workspace_id": f.WorkspaceID, "issue_id": id, "target_version": 1, "task_id": task, "status": "running", "materials": dbfx.Raw("'[]'::jsonb")})
		}
		body := map[string]any{"stop": true}
		if !all {
			body["issue_id"] = issues[0]
		}
		r := dbfx.WithHeaders(dbfx.JSONRequest("POST", "/api/supervision/check", body), "X-User-ID", f.UserID)
		r = r.WithContext(middleware.SetMemberContext(r.Context(), f.WorkspaceID, db.Member{}))
		reply := dbfx.Call(t, h.CheckSupervision, r)
		if reply.Code != 200 {
			t.Fatalf("stop reply=%+v", reply)
		}
		if all {
			if n := f.Count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", f.WorkspaceID); n != 0 {
				t.Fatalf("left running %d", n)
			}
			if n := f.Count(t, "SELECT count(*) FROM supervision_config WHERE workspace_id=$1 AND enabled", f.WorkspaceID); n != 0 {
				t.Fatal("all-stop failed to pause")
			}
			if n := f.Count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1 AND next_attempt_at='infinity'", f.WorkspaceID); n != 1 {
				t.Fatal("all-stop permanently stopped tasks that should resume")
			}
		} else {
			if n := f.Count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", f.WorkspaceID); n != 2 {
				t.Fatal("single stop changed other tasks")
			}
			if n := f.Count(t, "SELECT count(*) FROM supervision_config WHERE workspace_id=$1 AND enabled", f.WorkspaceID); n != 1 {
				t.Fatal("single stop paused workspace")
			}
		}
	}
}
