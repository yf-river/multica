package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func interactionRequest(method, taskID, id string, body any) *http.Request {
	req := testutil.JSONRequest(method, "/api/tasks/"+taskID+"/interactions", body)
	req = testutil.WithURLParams(req, "taskId", taskID, "interactionId", id)
	req.Header.Set("X-User-ID", testUserID)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
}

func interactionTask(t *testing.T) string {
	t.Helper()
	runtimeID := dbfx.Runtime(t, "interaction runtime", testutil.Cols{"provider": "codex"})
	agentID := dbfx.Agent(t, "interaction agent "+uuid.NewString(), runtimeID)
	issueID := dbfx.Issue(t, "interaction issue")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID, "status": "running"})
	dbfx.Cleanup(t, `DELETE FROM task_interaction WHERE task_id=$1`, taskID)
	return taskID
}

func createInteraction(t *testing.T, taskID, id string) protocol.TaskInteraction {
	t.Helper()
	prompt := protocol.InteractionPrompt{Kind: "question", Title: "Choose", Questions: []protocol.InteractionQuestion{{ID: "scope", Question: "Which scope?"}}}
	var result protocol.TaskInteraction
	testutil.Call(t, testHandler.CreateTaskInteraction, interactionRequest(http.MethodPost, taskID, id, map[string]any{"id": id, "provider": "codex", "prompt": prompt})).Want(200).JSON(&result)
	return result
}

func TestTaskInteractionDurableAnswerAndScope(t *testing.T) {
	taskID := interactionTask(t)
	id := uuid.NewString()
	first := createInteraction(t, taskID, id)
	second := createInteraction(t, taskID, id)
	if first.ID != second.ID || first.CreatedAt != second.CreatedAt {
		t.Fatal("retry replaced the original interaction")
	}
	var list struct {
		Interactions []protocol.TaskInteraction `json:"interactions"`
	}
	testutil.Call(t, testHandler.ListTaskInteractions, interactionRequest(http.MethodGet, taskID, "", nil)).Want(200).JSON(&list)
	if len(list.Interactions) != 1 || list.Interactions[0].Status != "pending" {
		t.Fatalf("pending interaction missing: %+v", list)
	}
	answer := protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"scope": {"current workspace"}}}
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(200)
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(200)
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, protocol.InteractionAnswer{Decision: "deny"})).Want(409)
	var row protocol.TaskInteraction
	testutil.Call(t, testHandler.GetTaskInteractionByDaemon, interactionRequest(http.MethodGet, taskID, id, nil)).Want(200).JSON(&row)
	if row.Status != "responded" || row.Response == nil || row.Response.Answers["scope"][0] != "current workspace" {
		t.Fatalf("answer did not round trip: %+v", row)
	}
	otherTask := interactionTask(t)
	testutil.Call(t, testHandler.GetTaskInteractionByDaemon, interactionRequest(http.MethodGet, otherTask, id, nil)).Want(404)
	req := interactionRequest(http.MethodGet, taskID, "", nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), uuid.NewString(), db.Member{}))
	testutil.Call(t, testHandler.ListTaskInteractions, req).Want(404)
}

func TestTaskInteractionTerminalAndMachineGuards(t *testing.T) {
	taskID := interactionTask(t)
	id := uuid.NewString()
	createInteraction(t, taskID, id)
	answer := protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"scope": {"one"}}}
	req := interactionRequest(http.MethodPost, taskID, id, answer)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.RespondTaskInteraction, req).Want(403)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, taskID)
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(409)
	var row protocol.TaskInteraction
	testutil.Call(t, testHandler.GetTaskInteractionByDaemon, interactionRequest(http.MethodGet, taskID, id, nil)).Want(200).JSON(&row)
	if row.Status != "expired" {
		t.Fatalf("terminal task still accepts input: %+v", row)
	}
	prompt := protocol.InteractionPrompt{Kind: "approval", Title: "Execute"}
	testutil.Call(t, testHandler.CreateTaskInteraction, interactionRequest(http.MethodPost, taskID, uuid.NewString(), map[string]any{"id": uuid.NewString(), "provider": "codex", "prompt": prompt})).Want(409)
}

func TestTaskInteractionRejectsChangedRequestAndInvalidAnswer(t *testing.T) {
	taskID := interactionTask(t)
	id := uuid.NewString()
	createInteraction(t, taskID, id)
	prompt := protocol.InteractionPrompt{Kind: "approval", Title: "Changed"}
	testutil.Call(t, testHandler.CreateTaskInteraction, interactionRequest(http.MethodPost, taskID, id, map[string]any{"id": id, "provider": "codex", "prompt": prompt})).Want(409)
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, protocol.InteractionAnswer{Decision: "allow"})).Want(400)
	testutil.Call(t, testHandler.ExpireTaskInteraction, interactionRequest(http.MethodPost, taskID, id, nil)).Want(200)
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, protocol.InteractionAnswer{Decision: "deny"})).Want(409)
}

func TestTaskInteractionLeaseCannotReviveExpiredRequest(t *testing.T) {
	taskID := interactionTask(t)
	id := uuid.NewString()
	createInteraction(t, taskID, id)
	dbfx.Exec(t, `UPDATE task_interaction SET expires_at=now()+interval '10 seconds' WHERE task_id=$1 AND id=$2`, taskID, id)
	var renewed protocol.TaskInteraction
	testutil.Call(t, testHandler.RenewTaskInteractionLease, interactionRequest(http.MethodPost, taskID, id, nil)).Want(200).JSON(&renewed)
	expires, err := time.Parse(time.RFC3339Nano, renewed.ExpiresAt)
	if err != nil || time.Until(expires) < 50*time.Second {
		t.Fatalf("lease not renewed: %+v", renewed)
	}
	dbfx.Exec(t, `UPDATE task_interaction SET expires_at=now()-interval '1 second' WHERE task_id=$1 AND id=$2`, taskID, id)
	testutil.Call(t, testHandler.RenewTaskInteractionLease, interactionRequest(http.MethodPost, taskID, id, nil)).Want(200).JSON(&renewed)
	if renewed.Status != "expired" {
		t.Fatal("orphan request revived")
	}
	answer := protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{"scope": {"old answer"}}}
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(409)
}
