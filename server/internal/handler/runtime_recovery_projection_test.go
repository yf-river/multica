package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type runtimeRecoveryRecorder struct{ workspaces []string }

func (r *runtimeRecoveryRecorder) NotifyRuntimeRecovered(_ context.Context, workspaceID string) {
	r.workspaces = append(r.workspaces, workspaceID)
}

func TestHeartbeatRecoveryRefreshesOnlyOnOfflineTransition(t *testing.T) {
	ctx := context.Background()
	id := dbfx.Runtime(t, "recovery refresh", testutil.Cols{"status": "offline"})
	row, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &runtimeRecoveryRecorder{}
	scheduler := NewPassthroughHeartbeatScheduler(testHandler.Queries)
	scheduler.RecoveryNotifier = recorder
	// 两个调用者持有同一份旧快照，只有实际恢复的那次发布刷新。
	for i := 0; i < 2; i++ {
		if err := scheduler.Schedule(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.workspaces) != 1 || recorder.workspaces[0] != testWorkspaceID {
		t.Fatalf("refreshes=%v", recorder.workspaces)
	}
}

func TestAgentReadinessRejectsPrivateRuntimeOwnerMismatch(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "readiness owner fence")
	other := dbfx.User(t, "runtime borrower", "readiness-borrower-"+runtimeID+"@test.local")
	dbfx.Member(t, testWorkspaceID, other, "member")
	id := dbfx.Agent(t, "runtime borrower agent", runtimeID, testutil.Cols{"owner_id": other})
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	lookup := service.RuntimeLookup{Queries: testHandler.Queries}
	verdict, err := service.AgentReadiness(ctx, lookup, agent)
	if err != nil || !verdict.Blocked() || verdict.Reason != dispatch.ReasonRuntimeAccessDenied {
		t.Fatalf("verdict=%+v err=%v", verdict, err)
	}
	if !service.RuntimeBlockedNeedsNotice(verdict.Reason) || !strings.Contains(service.RuntimeUnusableNotice(agent.Name, verdict), "Rebind") {
		t.Fatal("blocked trigger has no actionable notice")
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET visibility='public' WHERE id=$1`, runtimeID)
	verdict, err = service.AgentReadiness(ctx, lookup, agent)
	if err != nil || !verdict.Ready() {
		t.Fatalf("public runtime denied: %+v %v", verdict, err)
	}
}
