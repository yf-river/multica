package supervision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestIntPersistenceAndResume(t *testing.T) {
	if os.Getenv("MULTICA_SUPERVISION_INT_TEST") != "1" {
		t.Skip("requires explicit fixed-int database validation")
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
		t.Fatal("not integration environment")
	}
	pool, err := pgxpool.New(ctx, cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "监督持久化验收", "supervision-"+stamp+"@example.test", dbfx.Cols{"account": "supervision-" + stamp})
	f.WorkspaceID = f.Workspace(t, "监督持久化验收", "supervision-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	f.InsertNoID(t, "supervision_config", dbfx.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "enabled": true}, "workspace_id=$1", f.WorkspaceID)
	issues := make([]string, 100)
	for i := range issues {
		issues[i] = f.Issue(t, fmt.Sprintf("监督持久化验收 %03d", i))
	}
	svc := New(pool, nil)
	if err = svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1", f.WorkspaceID); n != 100 {
		t.Fatalf("inventory=%d", n)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1 AND reviewed_version=desired_version AND last_checked_at IS NULL", f.WorkspaceID); n != 100 {
		t.Fatalf("no-run accounting=%d", n)
	}
	runtime := f.Runtime(t, "离线夹具", dbfx.Cols{"status": "offline"})
	agent := f.Agent(t, "夹具", runtime)
	sourceTask := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "issue_id": issues[0], "status": "completed", "completed_at": dbfx.Raw("now()")})
	note := f.Comment(t, issues[0], "/note 请监督者核验映射，不要直接派发。")
	large := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 20, "type": "text", "content": strings.Repeat("x", 10*1024*1024) + "END-OF-EVIDENCE"})
	late := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 19, "type": "text", "content": "迟到的较小序号"})
	_ = late
	review := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "status": "completed", "completed_at": dbfx.Raw("now()"), "context": dbfx.Raw(`'{"type":"quality_supervision"}'::jsonb`)})
	t.Cleanup(func() {
		for _, table := range []string{"supervision_action", "supervision_finding", "supervision_batch", "supervision_material", "supervision_subject", "supervision_config"} {
			if _, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID); e != nil {
				t.Errorf("cleanup %s: %v", table, e)
			}
		}
	})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chunks, readErr := svc.materials(ctx, tx, f.WorkspaceID, issues[0])
	_ = tx.Rollback(ctx)
	if readErr != nil {
		t.Fatal(readErr)
	}
	noteFound := false
	for _, m := range chunks {
		if len([]rune(m.Text)) > 8000 {
			t.Fatal("unbounded material body")
		}
		if m.ID == note {
			noteFound = m.Note
		}
	}
	if !noteFound {
		t.Fatal("supervisor note not marked in real batch materials")
	}
	var target, generation int64
	f.QueryRow(t, "SELECT desired_version FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0]).Scan(&target)
	f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND kind='comment' AND source_id=$2", f.WorkspaceID, note).Scan(&generation)
	page, err := svc.ReadEvidence(ctx, f.WorkspaceID, "comment", note, 0)
	if err != nil {
		t.Fatal(err)
	}
	material := Material{Evidence: Evidence{Kind: "comment", ID: note}, Generation: generation, Offset: 0, End: page.End, Length: page.Length}
	refs, _ := json.Marshal([]Material{material})
	batch := f.Insert(t, "supervision_batch", dbfx.Cols{"workspace_id": f.WorkspaceID, "issue_id": issues[0], "target_version": target, "task_id": review, "status": "running", "materials": refs})
	f.Exec(t, "UPDATE comment SET content=content||' 新要求：按用户最新决定核验。' WHERE id=$1", note)
	result := Result{Summary: "旧代次已读，仍有新要求待核验", Efficiency: "clear", Correctness: "uncertain", Gaps: []string{"待核验新要求"}, Covered: []Evidence{material.Evidence}, Findings: []Finding{}, Resolutions: []Resolution{}}
	if err = svc.Commit(ctx, f.WorkspaceID, batch, review, result); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND generation>reviewed_generation AND reviewed_offset=0", f.WorkspaceID, note); n != 1 {
		t.Fatal("stale callback consumed newer edit")
	}
	result.Summary = "重复回调不得覆盖"
	if err = svc.Commit(ctx, f.WorkspaceID, batch, review, result); err != nil {
		t.Fatal(err)
	}
	var saved string
	f.QueryRow(t, "SELECT summary FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0]).Scan(&saved)
	if saved == result.Summary {
		t.Fatal("duplicate callback overwrote result")
	}
	// 新进程对象从数据库恢复分片位置，不依赖上一轮内存。
	restarted := New(pool, nil)
	first, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Text) > 8000 || first.End >= first.Length || first.Length < 10*1024*1024 {
		t.Fatalf("invalid bounded page: end=%d length=%d", first.End, first.Length)
	}
	f.Exec(t, "UPDATE supervision_material SET reviewed_offset=$3 WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, large, first.End)
	next, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, first.End)
	if err != nil {
		t.Fatal(err)
	}
	if next.Offset != first.End || next.End <= next.Offset {
		t.Fatal("resume lost offset")
	}
	tail, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, first.Length-100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail.Text, "END-OF-EVIDENCE") {
		t.Fatal("long evidence tail lost")
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND kind='message' AND run_id=$2", f.WorkspaceID, sourceTask); n != 2 {
		t.Fatalf("late-message coverage=%d", n)
	}
	// 运行ID绑定验证独立于 JSON 结构校验。
	if err = svc.Commit(ctx, f.WorkspaceID, batch, util.UUIDToString(dbid.NewV7()), result); err == nil {
		t.Fatal("foreign task accepted")
	}
	t.Log("PASS: 100-task inventory; no-run distinction; note generation race; duplicate callbacks; independent restart; 10MB bounded reads and tail; late seq; task fence")
}
