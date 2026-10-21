package supervision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIntConcurrentDispatchAndLongHistory(t *testing.T) {
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
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "并发验收", "parallel-"+stamp+"@example.test", dbfx.Cols{"account": "parallel-" + stamp})
	f.WorkspaceID = f.Workspace(t, "并发验收", "parallel-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "隔离排队夹具", dbfx.Cols{"provider": "codebuddy", "status": "online", "daemon_id": "no-daemon-" + stamp})
	agent := f.Agent(t, "监督夹具", runtime, dbfx.Cols{"instructions": "只分析测试材料", "max_concurrent_tasks": 3})
	f.InsertNoID(t, "supervision_config", dbfx.Cols{"workspace_id": f.WorkspaceID, "agent_id": agent, "owner_id": f.UserID, "enabled": false}, "workspace_id=$1", f.WorkspaceID)
	t.Cleanup(func() {
		for _, table := range []string{"supervision_action", "supervision_finding", "supervision_batch", "supervision_material", "supervision_subject", "supervision_config"} {
			f.Exec(t, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID)
		}
	})
	issues := []string{}
	for i := 0; i < 4; i++ {
		issue := f.Issue(t, fmt.Sprintf("task %d", i))
		issues = append(issues, issue)
		f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "issue_id": issue, "status": "completed"})
	}
	source := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "issue_id": issues[0], "status": "completed"})
	long := f.Insert(t, "task_message", dbfx.Cols{"task_id": source, "seq": 1, "type": "text", "content": strings.Repeat("原文", 200000)})
	later := f.Insert(t, "task_message", dbfx.Cols{"task_id": source, "seq": 2, "type": "text", "content": "不能提前覆盖"})
	svc := New(pool, service.NewTaskService(db.New(pool), pool, nil, nil))
	svc.Authorize = func(context.Context, string, string, string) bool { return true }
	if err = svc.inventory(ctx, f.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	// 初始盘点尚未完成时，最近盘点时间不能阻止下一页。
	f.Exec(t, "DELETE FROM supervision_material WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, later)
	f.Exec(t, "UPDATE supervision_config SET inventory_complete=false,last_inventory_at=now() WHERE workspace_id=$1", f.WorkspaceID)
	if err = svc.inventory(ctx, f.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if f.Count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, later) != 1 {
		t.Fatal("initial inventory page waited for steady scan interval")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := svc.materials(ctx, tx, f.WorkspaceID, issues[0])
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pages {
		if p.ID == later {
			t.Fatal("later message supplied before long message completed")
		}
		if p.ID == long {
			found = true
			if p.End != 40000 {
				t.Fatalf("long page end=%d", p.End)
			}
		}
	}
	if !found {
		t.Fatal("no long message")
	}
	f.Exec(t, "UPDATE supervision_config SET enabled=true WHERE workspace_id=$1", f.WorkspaceID)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.dispatch(ctx, f.WorkspaceID) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	// 竞争调用允许跳过已锁定配置；调度轮中的连续补位负责填满剩余名额。
	for i := 0; i < 20; i++ {
		if err = svc.dispatch(ctx, f.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		if f.Count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", f.WorkspaceID) == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", f.WorkspaceID); n != 3 {
		t.Fatalf("active=%d want3", n)
	}
	if n := f.Count(t, "SELECT count(DISTINCT issue_id) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", f.WorkspaceID); n != 3 {
		t.Fatalf("distinct=%d", n)
	}
	// 新的服务实例模拟恢复；已有名额不能重新派发。
	resumed := New(pool, svc.Tasks)
	resumed.Authorize = svc.Authorize
	if err = resumed.dispatch(ctx, f.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1", f.WorkspaceID); n != 3 {
		t.Fatal("restart duplicated batches")
	}
}
