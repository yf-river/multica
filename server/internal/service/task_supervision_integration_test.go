package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestIntSupervisionInstructionSnapshot(t *testing.T) {
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
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "监督快照验收", "policy-"+stamp+"@example.test", dbfx.Cols{"account": "policy-" + stamp})
	f.WorkspaceID = f.Workspace(t, "监督快照验收", "policy-"+stamp)
	runtime := f.Runtime(t, "监督快照夹具", dbfx.Cols{"provider": "codebuddy", "status": "online", "daemon_id": "policy-" + stamp})
	agent := f.Agent(t, "监督快照夹具", runtime, dbfx.Cols{"instructions": "原始监督规则"})
	svc := NewTaskService(db.New(pool), pool, nil, nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	ws, _ := util.ParseUUID(f.WorkspaceID)
	owner, _ := util.ParseUUID(f.UserID)
	aid, _ := util.ParseUUID(agent)
	enqueue := func() (db.AgentTaskQueue, error) {
		return svc.EnqueueSupervisionTaskTx(ctx, db.New(tx), ws, owner, aid, dbid.NewV7(), `{"materials":[]}`, false)
	}
	first, err := enqueue()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE agent SET instructions='新的监督规则' WHERE id=$1", agent); err != nil {
		t.Fatal(err)
	}
	second, err := enqueue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		task db.AgentTaskQueue
		want string
	}{{first, "原始监督规则"}, {second, "新的监督规则"}} {
		stored, err := db.New(tx).GetAgentTask(ctx, item.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot SupervisionContext
		if err = json.Unmarshal(stored.Context, &snapshot); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(item.want))
		if snapshot.Instructions != item.want || snapshot.InstructionsSHA256 != hex.EncodeToString(hash[:]) {
			t.Fatalf("wrong frozen instructions: %+v", snapshot)
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE agent SET instructions='  ' WHERE id=$1", agent); err != nil {
		t.Fatal(err)
	}
	if _, err = enqueue(); err == nil || !strings.Contains(err.Error(), "指令为空") {
		t.Fatalf("empty policy accepted: %v", err)
	}
}
