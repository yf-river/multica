package main

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDelegatedResultProjectionValidatesIdentityBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	f := dbfx.New(testPool, testWorkspaceID, testUserID)
	runtime := f.Runtime(t, "coordination projection")
	coordinator := f.Agent(t, "coordinator", runtime)
	worker := f.Agent(t, "worker", runtime)
	parent := f.Issue(t, "coordination", dbfx.Cols{"status": "in_progress", "assignee_type": "agent", "assignee_id": coordinator})
	child := f.Issue(t, "implementation")
	source := f.Task(t, coordinator, dbfx.Cols{"runtime_id": runtime, "issue_id": parent, "status": "completed", "originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human"})
	task := f.Task(t, worker, dbfx.Cols{"runtime_id": runtime, "issue_id": child, "status": "completed", "delegated_from_task_id": source, "originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "delegation"})
	q := db.New(testPool)
	svc := service.NewTaskService(q, testPool, nil, events.New())
	payload := map[string]any{"task_id": task, "issue_id": child, "agent_id": coordinator, "status": "completed"}
	event := events.Event{Type: protocol.EventTaskCompleted, WorkspaceID: testWorkspaceID, Payload: payload}
	if _, err := consumeDelegatedResult(ctx, q, event, svc); err == nil {
		t.Fatal("spoofed worker identity accepted")
	}
	payload["agent_id"] = worker
	event.WorkspaceID = "00000000-0000-0000-0000-000000000001"
	if _, err := consumeDelegatedResult(ctx, q, event, svc); err == nil {
		t.Fatal("cross-workspace event accepted")
	}
	event.WorkspaceID = testWorkspaceID
	for i := 0; i < 2; i++ {
		if _, err := consumeDelegatedResult(ctx, q, event, svc); err != nil {
			t.Fatal(err)
		}
	}
	n := f.Count(t, `SELECT count(*) FROM agent_task_queue WHERE trigger_evidence_kind='delegated_result' AND trigger_evidence_ref_id=$1`, task)
	if n != 1 {
		t.Fatalf("durable projection created %d successors", n)
	}
}
