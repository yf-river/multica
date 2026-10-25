package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 1. Historical extraction can only be claimed by the run's exact runtime in its workspace.
func TestExecutionEvidencePendingOwnership(t *testing.T) {
	t.Setenv("MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE", "2026-01-01T00:00:00Z")
	f := testutil.New(testPool, "", testUserID)
	f.WorkspaceID = f.Workspace(t, "Evidence fixture", "evidence-fixture")
	runtime := f.Runtime(t, "owned", testutil.Cols{"provider": "codebuddy"})
	agent := f.Agent(t, "target", runtime)
	issue := f.Issue(t, "target")
	run := f.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "completed", "session_id": "exact-session"})
	f.InsertNoID(t, "supervision_config", testutil.Cols{"workspace_id": f.WorkspaceID, "owner_id": testUserID, "enabled": true, "investigation_enabled": true}, "workspace_id=$1", f.WorkspaceID)
	round := f.Insert(t, "supervision_round", testutil.Cols{"id": testutil.Raw("gen_random_uuid()"), "workspace_id": f.WorkspaceID, "issue_id": issue, "sequence": 1, "material_cutoff": testutil.Raw("now()")})
	f.Insert(t, "supervision_trace_material", testutil.Cols{"id": testutil.Raw("gen_random_uuid()"), "workspace_id": f.WorkspaceID, "issue_id": issue, "run_id": run, "round_id": round, "runtime": "codebuddy", "source": "execution", "status": "pending"})
	ctx := context.Background()
	foreign := db.AgentRuntime{ID: parseUUID(testRuntimeID), WorkspaceID: parseUUID(f.WorkspaceID)}
	if testHandler.pendingExecutionEvidence(ctx, foreign) != nil {
		t.Fatal("foreign runtime received task material")
	}
	owned := db.AgentRuntime{ID: parseUUID(runtime), WorkspaceID: parseUUID(f.WorkspaceID)}
	request := testHandler.pendingExecutionEvidence(ctx, owned)
	if request == nil || request.TaskID != run || request.SessionID != "exact-session" || request.WorkspaceID != f.WorkspaceID || request.NextSeq != 1 {
		t.Fatalf("request identity %+v", request)
	}
	if testHandler.pendingExecutionEvidence(ctx, owned) != nil {
		t.Fatal("same extraction dispatched again during its lease")
	}
	f.Exec(t, "UPDATE supervision_trace_material SET cursor=cursor-'extraction_requested_at' WHERE workspace_id=$1", f.WorkspaceID)
	f.Exec(t, "UPDATE supervision_config SET investigation_enabled=false WHERE workspace_id=$1", f.WorkspaceID)
	if testHandler.pendingExecutionEvidence(ctx, owned) != nil {
		t.Fatal("disabled investigation was bypassed")
	}
}
