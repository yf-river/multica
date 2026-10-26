package handler

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestChatRuntimeSelectionSealedInputAndPlanApproval(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "runtime selection", testutil.Cols{"provider": "codebuddy"})
	agentID := dbfx.Agent(t, "runtime selection agent", runtimeID)
	chatID := dbfx.ChatSession(t, agentID)
	base := time.Now().Add(-time.Hour)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"chat_session_id": chatID, "runtime_id": runtimeID, "status": "completed", "created_at": base})
	message := func(role, content string, step int, extra testutil.Cols) string {
		cols := testutil.Cols{"chat_session_id": chatID, "role": role, "content": content, "created_at": base.Add(time.Duration(step) * time.Second)}
		for key, value := range extra {
			cols[key] = value
		}
		return dbfx.Insert(t, "chat_message", cols)
	}
	check := func(id string, hasPlan, plan, goal bool) {
		t.Helper()
		got, err := testHandler.Queries.GetChatRuntimeSelection(ctx, db.GetChatRuntimeSelectionParams{ChatSessionID: parseUUID(chatID), InputMessageID: parseUUID(id)})
		if err != nil {
			t.Fatal(err)
		}
		if got.HasPlanMode != hasPlan || got.PlanMode != plan || got.NativeGoal != goal {
			t.Fatalf("selection: %+v; want hasPlan=%v plan=%v goal=%v", got, hasPlan, plan, goal)
		}
	}
	first := message("user", "ordinary", 1, nil)
	check(first, false, false, false)
	assistant := message("assistant", "/plan", 2, nil)
	check(assistant, false, false, false)
	plan := message("user", "/plan", 3, nil)
	check(plan, true, true, false)
	goal := message("user", "/goal finish work", 4, nil)
	future := message("user", "/plan off", 5, nil)
	check(goal, true, true, true)
	check(future, true, false, true)
	message("user", "/plan", 6, nil)
	approval := dbfx.Insert(t, "task_interaction", testutil.Cols{"id": uuid.NewString(), "task_id": taskID, "workspace_id": testWorkspaceID, "provider": "codebuddy", "kind": "plan", "request": `{"kind":"plan","title":"Exit plan"}`, "status": "responded", "response": `{"decision":"allow"}`, "responded_at": base.Add(7 * time.Second)})
	next := message("user", "implement", 8, nil)
	check(next, true, false, true)
	message("user", "/plan", 9, nil)
	if _, err := testPool.Exec(ctx, `UPDATE task_interaction SET response='{"decision":"deny"}',responded_at=$2 WHERE id=$1`, approval, base.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	queued := dbfx.Task(t, agentID, testutil.Cols{"chat_session_id": chatID, "runtime_id": runtimeID, "status": "queued", "created_at": base.Add(10 * time.Second)})
	queuedMessage := message("user", "/plan off", 10, testutil.Cols{"task_id": queued})
	last := message("user", "explain the plan", 11, nil)
	check(last, true, true, true)
	check(queuedMessage, true, false, true)
	other := dbfx.ChatSession(t, agentID)
	got, err := testHandler.Queries.GetChatRuntimeSelection(ctx, db.GetChatRuntimeSelectionParams{ChatSessionID: parseUUID(other), InputMessageID: parseUUID(last)})
	if err != nil || got.HasPlanMode || got.NativeGoal {
		t.Fatalf("selection crossed chat boundary: %+v error=%v", got, err)
	}
}

func TestChatRuntimeSelectionUsesConfirmedLiveCommands(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codebuddy")
	chatID := dbfx.ChatSession(t, f.agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=NULL,chat_session_id=$2 WHERE id=$1`, f.taskID, chatID)
	messageID := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chatID, "role": "user", "content": "continue", "task_id": f.taskID})
	registerRuntimeInput(t, f, claim, []string{"command"}, 200)
	check := func(hasPlan, plan, goal bool) {
		t.Helper()
		row, err := testHandler.Queries.GetChatRuntimeSelection(t.Context(), db.GetChatRuntimeSelectionParams{ChatSessionID: parseUUID(chatID), InputMessageID: parseUUID(messageID)})
		if err != nil || row.HasPlanMode != hasPlan || row.PlanMode != plan || row.NativeGoal != goal {
			t.Fatalf("live controls not restored: %+v error=%v", row, err)
		}
	}
	id := uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "command", "/plan", 201)
	check(false, false, false)
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200)
	check(false, false, false)
	ackRuntimeInput(t, f.taskID, id, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered"}, 200)
	check(true, true, false)
	id = uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "command", "/goal finish work", 201)
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200)
	ackRuntimeInput(t, f.taskID, id, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered"}, 200)
	check(true, true, true)
	id = uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "command", "/plan off", 201)
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200)
	ackRuntimeInput(t, f.taskID, id, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "failed", FailureReason: "delivery_unknown"}, 200)
	check(true, true, true)
}
